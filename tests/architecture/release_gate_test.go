package architecture

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// readPinStep is the step of release.yaml's e2e-gate that takes the aval
// release ci.yaml pins; pinDecl matches the lines its sed reads.
const readPinStep = `version=$(sed -n 's/^  AVAL_VERSION: *//p' .github/workflows/ci.yaml)
sha256=$(sed -n 's/^  AVAL_SHA256: *//p' .github/workflows/ci.yaml)
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ && "$sha256" =~ ^[0-9a-f]{64}$ ]]; then
  echo "::error title=no release pin::ci.yaml must declare AVAL_VERSION and AVAL_SHA256 once at workflow level"
  exit 1
fi
{ echo "AVAL_VERSION=$version"; echo "AVAL_SHA256=$sha256"; } >> "$GITHUB_ENV"`

var pinDecl = regexp.MustCompile(`(?m)^  (AVAL_VERSION|AVAL_SHA256): *(.*)$`)

// releaseGateProblems (#234): release.yaml's e2e-gate verifies the e2e
// attestation with the aval release ci.yaml pins, the one dogfood-publish
// signed it with, because a binary built from the tag can compute another
// fingerprint manifest version (aval-fingerprint/vN) and miss on every
// release. The gate reads the pin from ci.yaml (readPinStep), so release.yaml
// declares none of its own and the two cannot pin different versions; ci.yaml
// declares it exactly once, on the lines readPinStep reads, with the values
// its jobs use. The gate holds a read-only token, installs the release as
// every release job does (releaseJobProblems) after reading the pin, passes
// no token to aval, and release needs it.
func releaseGateProblems(release, ci []byte) ([]string, error) {
	var ciWF struct {
		Env map[string]string `yaml:"env"`
	}
	if err := yaml.Unmarshal(ci, &ciWF); err != nil {
		return nil, err
	}
	var wf struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			permJob `yaml:",inline"`
			Env     map[string]string `yaml:"env"`
		} `yaml:"jobs"`
	}
	var needs workflow
	if err := yaml.Unmarshal(release, &wf); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(release, &needs); err != nil {
		return nil, err
	}
	problems := ciPinProblems(ci, ciWF.Env)
	envs := []map[string]string{wf.Env}
	for _, j := range wf.Jobs {
		envs = append(envs, j.Env)
		for _, s := range j.Steps {
			envs = append(envs, s.Env)
		}
	}
	for _, env := range envs {
		for _, k := range []string{"AVAL_VERSION", "AVAL_SHA256"} {
			if _, ok := env[k]; ok {
				problems = append(problems, "release.yaml declares its own "+k+"; e2e-gate must read ci.yaml's")
			}
		}
	}
	gate := wf.Jobs["e2e-gate"]
	if gate.If != "startsWith(github.ref, 'refs/tags/v')" {
		problems = append(problems, "e2e-gate runs on "+gate.If)
	}
	if !maps.Equal(gate.Permissions, map[string]string{"contents": "read"}) {
		problems = append(problems, "e2e-gate: permissions must be exactly contents read")
	}
	if !slices.Contains(needs.Jobs["release"].Needs, "e2e-gate") {
		problems = append(problems, "release must need e2e-gate")
	}
	problems = append(problems, releaseJobProblems("e2e-gate", gate.Steps)...)
	problems = append(problems, gateStepProblems(gate.Steps)...)
	slices.Sort(problems)
	return problems, nil
}

// ciPinProblems: ci.yaml declares each pin exactly once, on the lines
// readPinStep reads, with the value its jobs use (env).
func ciPinProblems(ci []byte, env map[string]string) []string {
	var problems []string
	decls := map[string][]string{}
	for _, m := range pinDecl.FindAllStringSubmatch(string(ci), -1) {
		decls[m[1]] = append(decls[m[1]], m[2])
	}
	for _, k := range []string{"AVAL_VERSION", "AVAL_SHA256"} {
		if len(decls[k]) != 1 || decls[k][0] != env[k] || env[k] == "" {
			problems = append(problems, "ci.yaml must declare "+k+" exactly once, at workflow level, as release.yaml's e2e-gate reads it")
		}
	}
	return problems
}

// gateStepProblems: the gate reads the pin before install.sh and runs aval
// verify on the e2e contract with no token in its environment.
func gateStepProblems(steps []step) []string {
	var problems []string
	pin, installed, verify := false, false, false
	for _, s := range steps {
		run := strings.TrimSpace(s.Run)
		switch {
		case run == readPinStep:
			pin = !installed
		case run == installScript:
			installed = true
		case strings.Contains(run, "aval verify -f aval.e2e.yaml"):
			verify = true
			for k, v := range s.Env {
				if strings.Contains(v, "token") || strings.Contains(v, "secrets.") {
					problems = append(problems, "e2e-gate: the verify step passes "+k+" to aval")
				}
			}
		}
	}
	if !pin {
		problems = append(problems, "e2e-gate: does not read ci.yaml's pin before install.sh")
	}
	if !verify {
		problems = append(problems, "e2e-gate: never runs aval verify -f aval.e2e.yaml")
	}
	return problems
}

func TestReleaseGateVerifiesWithPinnedAval(t *testing.T) {
	release, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	problems, err := releaseGateProblems(release, readCI(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestReleaseGateCheckCatchesDrift(t *testing.T) {
	sha := strings.Repeat("a", 64)
	ci := "env:\n  AVAL_VERSION: 0.2.0\n  AVAL_SHA256: " + sha + "\njobs: {}\n"
	indent := func(s, pre string) string { return pre + strings.ReplaceAll(s, "\n", "\n"+pre) }
	pinStep := "      - run: |\n" + indent(readPinStep, "          ") + "\n"
	const (
		cosign  = "      - uses: sigstore/cosign-installer@x\n"
		install = "      - run: .github/aval/install.sh\n"
		verify  = "      - run: aval verify -f aval.e2e.yaml --policy .github/aval/main.yaml\n"
		tagged  = "    if: startsWith(github.ref, 'refs/tags/v')\n"
		readTok = "    permissions: {contents: read}\n"
		release = "  release: {needs: [e2e-gate]}\n"
	)
	rel := func(header, perms, steps, rest string) string {
		return header + "jobs:\n  e2e-gate:\n" + tagged + perms + "    steps:\n" + steps + rest
	}
	good := rel("", readTok, cosign+pinStep+install+verify, release)
	if p, err := releaseGateProblems([]byte(good), []byte(ci)); err != nil || len(p) != 0 {
		t.Fatalf("valid workflows reported %v (%v)", p, err)
	}
	bad := map[string][2]string{
		"release pins its own version": {rel("env: {AVAL_VERSION: 0.1.0}\n", readTok, cosign+pinStep+install+verify, release), ci},
		"step pins its own sha":        {rel("", readTok, cosign+pinStep+"      - env:\n          AVAL_SHA256: "+sha+"\n        run: .github/aval/install.sh\n"+verify, release), ci},
		"verifies with bin/aval":       {rel("", readTok, cosign+pinStep+install+"      - run: make build && ./bin/aval verify -f aval.e2e.yaml\n", release), ci},
		"no pin read":                  {rel("", readTok, cosign+install+verify, release), ci},
		"pin read after install":       {rel("", readTok, cosign+install+pinStep+verify, release), ci},
		"no install":                   {rel("", readTok, cosign+pinStep+verify, release), ci},
		"no verify":                    {rel("", readTok, cosign+pinStep+install, release), ci},
		"token reaches aval":           {rel("", readTok, cosign+pinStep+install+"      - env: {GH_TOKEN: '${{ github.token }}'}\n        run: aval verify -f aval.e2e.yaml\n", release), ci},
		"write token":                  {rel("", "    permissions: {contents: write}\n", cosign+pinStep+install+verify, release), ci},
		"release does not need gate":   {rel("", readTok, cosign+pinStep+install+verify, "  release: {}\n"), ci},
		"ci declares version twice":    {good, ci + "other:\n  AVAL_VERSION: 0.3.0\n"},
		"ci pin only in a job":         {good, "env: {}\njobs:\n  a:\n    env:\n      AVAL_VERSION: 0.2.0\n      AVAL_SHA256: " + sha + "\n"},
		"ci pin in flow style":         {good, "env: {AVAL_VERSION: 0.2.0, AVAL_SHA256: " + sha + "}\n"},
	}
	for name, c := range bad {
		if p, err := releaseGateProblems([]byte(c[0]), []byte(c[1])); err != nil || len(p) == 0 {
			t.Errorf("%s: no problem reported (%v)", name, err)
		}
	}
}
