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

// installPinStep is the step of release.yaml's e2e-gate that installs the
// aval release integrations/github/run pins; pinDecl matches the lines its
// sed and grep read.
const installPinStep = `version=$(sed -n 's/^ *DESIRED_VERSION: *//p' integrations/github/run/action.yml)
sha256=$(grep -o 'linux_amd64=[0-9a-f]\{64\}' integrations/github/run/action.yml)
if ! [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ && "$sha256" =~ ^linux_amd64=[0-9a-f]{64}$ ]]; then
  echo "::error title=no release pin::integrations/github/run/action.yml must pin DESIRED_VERSION and a linux_amd64 sha256 once"
  exit 1
fi
cosign=$(command -v cosign)
gh=$(command -v gh)
PATH="/usr/bin:/bin:${cosign%/*}:${gh%/*}" AVAL_INSTALL_DIR="$RUNNER_TEMP/aval" USE_SUDO=false \
  /bin/bash scripts/get-aval.sh --version "$version" --sha256 "$sha256" --require-verify --require-provenance
echo "$RUNNER_TEMP/aval" >> "$GITHUB_PATH"`

// gatePolicy and gateVerify: the gate reads main's policy, never the
// checkout's, and requires runner level, as main does (#214, #216).
const (
	gatePolicy = `git show origin/main:.github/aval/policy.yaml > "$RUNNER_TEMP/policy.yaml"`
	gateVerify = `aval verify -f aval.yaml --test aval-e2e --policy "$RUNNER_TEMP/policy.yaml" --min-trust-level runner`
)

var pinDecl = regexp.MustCompile(`(?m)^ *DESIRED_VERSION: *v[0-9]+\.[0-9]+\.[0-9]+$|linux_amd64=[0-9a-f]{64}`)

// releaseGateProblems (#234, #216): release.yaml's e2e-gate verifies the e2e
// attestation with the aval release integrations/github/run pins, the one
// dogfood-publish signed it with through integrations/github/publish
// (TestActionsInstallTheSameRelease keeps the two pins equal), because a
// binary built from the tag can compute another fingerprint manifest version
// (aval-fingerprint/vN) and miss on every release. The gate reads the pin
// from action.yml (installPinStep), so release.yaml declares none of its own;
// action.yml declares it once, on the lines installPinStep reads. The gate
// holds a read-only token, installs the release after cosign and before any
// aval step, builds no aval, passes no token to aval, and release needs it.
func releaseGateProblems(release, action []byte) ([]string, error) {
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
	var problems []string
	if m := pinDecl.FindAllString(string(action), -1); len(m) != 2 || !strings.Contains(m[0], "DESIRED_VERSION") {
		problems = append(problems, "integrations/github/run/action.yml must pin DESIRED_VERSION and then one linux_amd64 sha256, as installPinStep reads them")
	}
	envs := []map[string]string{wf.Env}
	for _, j := range wf.Jobs {
		envs = append(envs, j.Env)
		for _, s := range j.Steps {
			envs = append(envs, s.Env)
		}
	}
	for _, env := range envs {
		for _, k := range []string{"DESIRED_VERSION", "AVAL_VERSION", "AVAL_SHA256"} {
			if _, ok := env[k]; ok {
				problems = append(problems, "release.yaml declares its own "+k+"; e2e-gate must read the run action's pin")
			}
		}
	}
	gate := wf.Jobs["e2e-gate"]
	problems = append(problems, tokenEnvProblems("a job or workflow env", wf.Env, gate.Env)...)
	if gate.If != "startsWith(github.ref, 'refs/tags/v')" {
		problems = append(problems, "e2e-gate runs on "+gate.If)
	}
	if !maps.Equal(gate.Permissions, map[string]string{"contents": "read"}) {
		problems = append(problems, "e2e-gate: permissions must be exactly contents read")
	}
	if !slices.Contains(needs.Jobs["release"].Needs, "e2e-gate") {
		problems = append(problems, "release must need e2e-gate")
	}
	problems = append(problems, gateStepProblems(gate.Steps)...)
	slices.Sort(problems)
	return problems, nil
}

// tokenEnvProblems reports each variable in envs that passes a token to aval.
func tokenEnvProblems(where string, envs ...map[string]string) []string {
	var problems []string
	for _, env := range envs {
		for k, v := range env {
			if strings.Contains(v, "token") || strings.Contains(v, "secrets.") {
				problems = append(problems, "e2e-gate: "+k+" in "+where+" reaches aval")
			}
		}
	}
	return problems
}

// gateStepProblems: cosign, then installPinStep, then the verify step, which
// reads main's policy, runs gateVerify with no token in its environment, and
// is the only step that runs aval.
func gateStepProblems(steps []step) []string {
	var problems []string
	cosign, installed, verify := false, false, false
	for _, s := range steps {
		run := strings.TrimSpace(s.Run)
		switch {
		case strings.HasPrefix(s.Uses, "sigstore/cosign-installer@"):
			cosign = true
		case run == installPinStep:
			if !cosign {
				problems = append(problems, "e2e-gate: installs aval before cosign")
			}
			installed = true
		case strings.Contains(run, "make build") || strings.Contains(run, "bin/aval") || strings.Contains(run, "go build"):
			problems = append(problems, "e2e-gate: builds aval instead of using the pinned release")
		case strings.Contains(run, gateVerify) && strings.HasPrefix(run, gatePolicy+"\n"):
			if !installed {
				problems = append(problems, "e2e-gate: runs aval before installing the pinned release")
			}
			verify = true
			problems = append(problems, tokenEnvProblems("the verify step", s.Env)...)
		case strings.Contains(run, "aval "):
			problems = append(problems, "e2e-gate: runs aval outside the verify step: "+run)
		}
	}
	if !installed {
		problems = append(problems, "e2e-gate: does not install the run action's pinned release")
	}
	if !verify {
		problems = append(problems, "e2e-gate: never reads main's policy and runs "+gateVerify)
	}
	return problems
}

func TestReleaseGateVerifiesWithPinnedAval(t *testing.T) {
	release, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	problems, err := releaseGateProblems(release, []byte(readFile(t, "integrations/github/run/action.yml")))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestReleaseGateCheckCatchesDrift(t *testing.T) {
	sha := strings.Repeat("a", 64)
	action := "runs:\n  steps:\n    - id: install\n      env:\n        DESIRED_VERSION: v0.2.0\n        AVAL_SHA256: >-\n          linux_amd64=" + sha + "\n          darwin_arm64=" + sha + "\n"
	indent := func(s, pre string) string { return pre + strings.ReplaceAll(s, "\n", "\n"+pre) }
	install := "      - run: |\n" + indent(installPinStep, "          ") + "\n"
	const (
		cosign  = "      - uses: sigstore/cosign-installer@x\n"
		tagged  = "    if: startsWith(github.ref, 'refs/tags/v')\n"
		readTok = "    permissions: {contents: read}\n"
		release = "  release: {needs: [e2e-gate]}\n"
	)
	verify := "      - run: |\n" + indent(gatePolicy+"\n"+gateVerify+" -o json > r.json", "          ") + "\n"
	rel := func(header, perms, steps, rest string) string {
		return header + "jobs:\n  e2e-gate:\n" + tagged + perms + "    steps:\n" + steps + rest
	}
	good := rel("", readTok, cosign+install+verify, release)
	if p, err := releaseGateProblems([]byte(good), []byte(action)); err != nil || len(p) != 0 {
		t.Fatalf("valid workflows reported %v (%v)", p, err)
	}
	checkoutPolicy := strings.Replace(verify, "git show origin/main:.github/aval/policy.yaml >", "cp .github/aval/policy.yaml", 1)
	bad := map[string][2]string{
		"release pins its own version": {rel("env: {DESIRED_VERSION: v0.1.0}\n", readTok, cosign+install+verify, release), action},
		"step pins its own sha":        {rel("", readTok, cosign+"      - env:\n          AVAL_SHA256: "+sha+"\n        run: install\n"+install+verify, release), action},
		"verifies with bin/aval":       {rel("", readTok, cosign+install+"      - run: make build && ./bin/aval verify -f aval.yaml --test aval-e2e\n", release), action},
		"no install":                   {rel("", readTok, cosign+verify, release), action},
		"install before cosign":        {rel("", readTok, install+cosign+verify, release), action},
		"verify before install":        {rel("", readTok, cosign+verify+install, release), action},
		"no verify":                    {rel("", readTok, cosign+install, release), action},
		"checkout policy":              {rel("", readTok, cosign+install+checkoutPolicy, release), action},
		"policy minimum":               {rel("", readTok, cosign+install+strings.Replace(verify, " --min-trust-level runner", "", 1), release), action},
		"another aval step":            {rel("", readTok, cosign+install+verify+"      - run: aval verify -f aval.yaml --test aval-e2e\n", release), action},
		"token reaches aval":           {rel("", readTok, cosign+install+strings.Replace(verify, "      - run: |\n", "      - env: {GH_TOKEN: '${{ github.token }}'}\n        run: |\n", 1), release), action},
		"job env token":                {strings.Replace(good, "    steps:\n", "    env: {GH_TOKEN: '${{ github.token }}'}\n    steps:\n", 1), action},
		"write token":                  {rel("", "    permissions: {contents: write}\n", cosign+install+verify, release), action},
		"release does not need gate":   {rel("", readTok, cosign+install+verify, "  release: {}\n"), action},
		"action pins no version":       {good, strings.Replace(action, "DESIRED_VERSION: v0.2.0", "DESIRED_VERSION: latest", 1)},
		"action pins two sha256s":      {good, action + "          linux_amd64=" + sha + "\n"},
		"action pins no linux sha256":  {good, strings.Replace(action, "linux_amd64=", "linux_386=", 1)},
	}
	for name, c := range bad {
		if p, err := releaseGateProblems([]byte(c[0]), []byte(c[1])); err != nil || len(p) == 0 {
			t.Errorf("%s: no problem reported (%v)", name, err)
		}
	}
}
