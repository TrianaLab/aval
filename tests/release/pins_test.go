package release

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// actionFiles install the pinned aval release (#211, #212). The pin-actions
// job of release.yaml moves their pins after each release (#213), so pinning
// an action by commit SHA pins the binary too.
var actionFiles = []string{
	filepath.Join("..", "..", "integrations", "github", "run", "action.yml"),
	filepath.Join("..", "..", "integrations", "github", "publish", "action.yml"),
}

// pinPlatforms are the runners the actions support: one sha256 each.
var pinPlatforms = []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"}

// releaseBase is where a published release's files live.
const releaseBase = "https://github.com/TrianaLab/aval/releases/download/"

// actionPins returns DESIRED_VERSION and the <os>_<arch> -> sha256 entries of
// AVAL_SHA256 from the install step (id: install) of an action.yml.
func actionPins(data []byte) (string, map[string]string, error) {
	var a struct {
		Runs struct {
			Steps []struct {
				ID  string            `yaml:"id"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &a); err != nil {
		return "", nil, err
	}
	for _, s := range a.Runs.Steps {
		if s.ID != "install" {
			continue
		}
		pins := map[string]string{}
		for _, f := range strings.Fields(s.Env["AVAL_SHA256"]) {
			p, sum, ok := strings.Cut(f, "=")
			if !ok || pins[p] != "" {
				return "", nil, fmt.Errorf("AVAL_SHA256: malformed or duplicate entry %q", f)
			}
			pins[p] = sum
		}
		return s.Env["DESIRED_VERSION"], pins, nil
	}
	return "", nil, fmt.Errorf("no install step")
}

// pinProblems: every supported platform, and nothing else, is pinned to the
// sha256 that the release's checksums.txt lists for its archive.
func pinProblems(version string, pins, checksums map[string]string) []string {
	var problems []string
	for _, p := range pinPlatforms {
		archive := "aval_" + strings.TrimPrefix(version, "v") + "_" + p + ".tar.gz"
		switch want := checksums[archive]; {
		case want == "":
			problems = append(problems, fmt.Sprintf("%s: checksums.txt of %s does not list %s", p, version, archive))
		case pins[p] != want:
			problems = append(problems, fmt.Sprintf("%s: pinned %q, checksums.txt of %s says %s", p, pins[p], version, want))
		}
	}
	for p := range pins {
		if !slices.Contains(pinPlatforms, p) {
			problems = append(problems, "unsupported platform pinned: "+p)
		}
	}
	slices.Sort(problems)
	return problems
}

// fetchChecksums downloads checksums.txt of a published release. A version
// with no release, or a release with no checksums.txt, is an error.
func fetchChecksums(t *testing.T, version string) map[string]string {
	t.Helper()
	return fetchSums(t, releaseBase+version+"/"+checksumsFile)
}

// fetchSums downloads and parses a sha256sum-style file. Anything but HTTP
// 200 is an error: the actions pin a version that was never released.
func fetchSums(t *testing.T, url string) map[string]string {
	t.Helper()
	c := http.Client{Timeout: time.Minute}
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: HTTP %d: the actions pin a version that was never released", url, resp.StatusCode)
	}
	path := filepath.Join(t.TempDir(), checksumsFile)
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return parseChecksums(t, path)
}

// The run and publish actions pin the same published release, and each
// per-platform sha256 is the one that release's checksums.txt lists. A pin
// moved to a version with no checksums.txt, or to a sha256 the release does
// not list, fails here (ci-release runs it on every PR that changes code).
// Needs network: it downloads the pinned release's checksums.txt.
func TestActionPinsMatchReleaseChecksums(t *testing.T) {
	var version string
	var pins map[string]string
	for i, f := range actionFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		v, p, err := actionPins(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if i > 0 && (v != version || fmt.Sprint(p) != fmt.Sprint(pins)) {
			t.Fatalf("%s pins %s %v, %s pins %s %v", f, v, p, actionFiles[0], version, pins)
		}
		version, pins = v, p
	}
	for _, p := range pinProblems(version, pins, fetchChecksums(t, version)) {
		t.Error(p)
	}
}

func TestPinCheckCatchesDrift(t *testing.T) {
	sums := map[string]string{}
	pins := map[string]string{}
	for i, p := range pinPlatforms {
		sum := strings.Repeat(fmt.Sprint(i), 64)
		sums["aval_1.2.0_"+p+".tar.gz"], pins[p] = sum, sum
	}
	if got := pinProblems("v1.2.0", pins, sums); len(got) != 0 {
		t.Fatalf("matching pins reported %v", got)
	}
	cases := map[string]func(v *string, p map[string]string){
		// Downgrading DESIRED_VERSION alone: the old release's checksums.txt
		// does not list these archives.
		"other version": func(v *string, _ map[string]string) { *v = "v1.1.0" },
		"wrong sha256":  func(_ *string, p map[string]string) { p["linux_amd64"] = strings.Repeat("f", 64) },
		"platform gone": func(_ *string, p map[string]string) { delete(p, "darwin_arm64") },
		"extra entry":   func(_ *string, p map[string]string) { p["windows_amd64"] = strings.Repeat("0", 64) },
	}
	for name, mutate := range cases {
		v, p := "v1.2.0", map[string]string{}
		for k, s := range pins {
			p[k] = s
		}
		mutate(&v, p)
		if got := pinProblems(v, p, sums); len(got) == 0 {
			t.Errorf("%s: no problem reported", name)
		}
	}
	for name, yml := range map[string]string{
		"no install step": "runs:\n  steps:\n    - id: run\n",
		"duplicate entry": "runs:\n  steps:\n    - id: install\n      env: {AVAL_SHA256: a=1 a=2}\n",
		"no equals":       "runs:\n  steps:\n    - id: install\n      env: {AVAL_SHA256: abc}\n",
		"not yaml":        "runs: [\n",
	} {
		if _, _, err := actionPins([]byte(yml)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// scripts/pin-actions.sh, which the release workflow runs, rewrites both
// actions to a new release so that the check above passes, keeps everything
// else byte for byte, and changes nothing when checksums.txt lacks a
// platform or the tag is malformed.
func TestPinActionsScript(t *testing.T) {
	dir := t.TempDir()
	files, before := copyActions(t, dir)
	var lines []string
	sums := map[string]string{}
	for i, p := range append([]string{"windows_amd64"}, pinPlatforms...) {
		name := "aval_9.8.7_" + p + ".tar.gz"
		sums[name] = strings.Repeat(fmt.Sprintf("%x", i+10), 64)
		lines = append(lines, sums[name]+"  "+name, sums[name]+"  "+name+".sbom.json")
	}
	full := filepath.Join(dir, "checksums.txt")
	partial := filepath.Join(dir, "partial.txt")
	writeLines(t, full, lines)
	writeLines(t, partial, lines[:len(lines)-2])

	for _, args := range [][]string{{"v9.8.7", partial}, {"9.8.7", full}, {"v9.8.7; rm -rf /", full}} {
		if out, err := pinActions(append(args, files...)...); err == nil {
			t.Errorf("%v: succeeded: %s", args, out)
		}
		for i, f := range files {
			if got, _ := os.ReadFile(f); string(got) != string(before[i]) {
				t.Errorf("%v: changed %s", args, f)
			}
		}
	}

	if out, err := pinActions(append([]string{"v9.8.7", full}, files...)...); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for i, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		v, pins, err := actionPins(data)
		if err != nil {
			t.Fatal(err)
		}
		if v != "v9.8.7" {
			t.Errorf("%s: DESIRED_VERSION %q", f, v)
		}
		for _, p := range pinProblems(v, pins, sums) {
			t.Errorf("%s: %s", f, p)
		}
		if changed := diffLines(before[i], data); changed != len(pinPlatforms)+1 {
			t.Errorf("%s: %d lines changed, want %d (version and one sha256 per platform)", f, changed, len(pinPlatforms)+1)
		}
	}
}

// copyActions copies actionFiles into dir and returns the copies and their
// contents.
func copyActions(t *testing.T, dir string) ([]string, [][]byte) {
	t.Helper()
	var files []string
	var data [][]byte
	for i, f := range actionFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, fmt.Sprintf("action%d.yml", i))
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			t.Fatal(err)
		}
		files, data = append(files, dst), append(data, b)
	}
	return files, data
}

func pinActions(args ...string) ([]byte, error) {
	return exec.Command("bash", append([]string{filepath.Join("..", "..", "scripts", "pin-actions.sh")}, args...)...).CombinedOutput()
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// diffLines counts the lines that differ between two texts of equal length
// in lines, or -1.
func diffLines(a, b []byte) int {
	la, lb := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	if len(la) != len(lb) {
		return -1
	}
	n := 0
	for i := range la {
		if la[i] != lb[i] {
			n++
		}
	}
	return n
}

// cosignChecksums is where a cosign release lists the sha256 of its assets.
const cosignChecksums = "https://github.com/sigstore/cosign/releases/download/%s/cosign_checksums.txt"

// cosignPins returns COSIGN_VERSION and the cosign-<os>-<arch> -> sha256
// entries of COSIGN_SHA256 from the cosign step (id: cosign) of an action.yml.
func cosignPins(data []byte) (string, map[string]string, error) {
	var a struct {
		Runs struct {
			Steps []struct {
				ID  string            `yaml:"id"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &a); err != nil {
		return "", nil, err
	}
	for _, s := range a.Runs.Steps {
		if s.ID != "cosign" {
			continue
		}
		pins := map[string]string{}
		for _, f := range strings.Fields(s.Env["COSIGN_SHA256"]) {
			asset, sum, ok := strings.Cut(f, "=")
			if !ok || pins[asset] != "" {
				return "", nil, fmt.Errorf("COSIGN_SHA256: malformed or duplicate entry %q", f)
			}
			pins[asset] = sum
		}
		return s.Env["COSIGN_VERSION"], pins, nil
	}
	return "", nil, fmt.Errorf("no cosign step")
}

// cosignPinProblems: every supported platform, and nothing else, is pinned to
// the sha256 that cosign_checksums.txt lists for its binary.
func cosignPinProblems(pins, sums map[string]string) []string {
	var problems []string
	want := map[string]bool{}
	for _, p := range pinPlatforms {
		asset := "cosign-" + strings.Replace(p, "_", "-", 1)
		want[asset] = true
		switch sum := sums[asset]; {
		case sum == "":
			problems = append(problems, asset+": not in cosign_checksums.txt")
		case pins[asset] != sum:
			problems = append(problems, fmt.Sprintf("%s: pinned %q, cosign_checksums.txt says %s", asset, pins[asset], sum))
		}
	}
	for asset := range pins {
		if !want[asset] {
			problems = append(problems, "unsupported platform pinned: "+asset)
		}
	}
	slices.Sort(problems)
	return problems
}

// The run and publish actions pin the same cosign release (#248), and each
// per-platform sha256 is the one its cosign_checksums.txt lists. Moving the
// pin is a manual step (the comment above the cosign step says how); this
// test fails a pin that does not match the release. Needs network.
func TestCosignPinMatchesRelease(t *testing.T) {
	var version string
	var pins map[string]string
	for i, f := range actionFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		v, p, err := cosignPins(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if i > 0 && (v != version || fmt.Sprint(p) != fmt.Sprint(pins)) {
			t.Fatalf("%s pins cosign %s %v, %s pins %s %v", f, v, p, actionFiles[0], version, pins)
		}
		version, pins = v, p
	}
	for _, p := range cosignPinProblems(pins, fetchSums(t, fmt.Sprintf(cosignChecksums, version))) {
		t.Error(p)
	}
}

func TestCosignPinCheckCatchesDrift(t *testing.T) {
	sums, pins := map[string]string{"cosign-windows-amd64.exe": strings.Repeat("9", 64)}, map[string]string{}
	for i, a := range []string{"cosign-linux-amd64", "cosign-linux-arm64", "cosign-darwin-amd64", "cosign-darwin-arm64"} {
		sums[a] = strings.Repeat(fmt.Sprint(i), 64)
		pins[a] = sums[a]
	}
	if got := cosignPinProblems(pins, sums); len(got) != 0 {
		t.Fatalf("matching pins reported %v", got)
	}
	for name, mutate := range map[string]func(p, s map[string]string){
		"wrong sha256":     func(p, _ map[string]string) { p["cosign-linux-amd64"] = strings.Repeat("f", 64) },
		"platform gone":    func(p, _ map[string]string) { delete(p, "cosign-darwin-arm64") },
		"extra entry":      func(p, _ map[string]string) { p["cosign-windows-amd64.exe"] = strings.Repeat("9", 64) },
		"release lacks it": func(_, s map[string]string) { delete(s, "cosign-linux-arm64") },
	} {
		p, s := maps.Clone(pins), maps.Clone(sums)
		mutate(p, s)
		if got := cosignPinProblems(p, s); len(got) == 0 {
			t.Errorf("%s: no problem reported", name)
		}
	}
	for name, yml := range map[string]string{
		"no cosign step":  "runs:\n  steps:\n    - id: install\n",
		"duplicate entry": "runs:\n  steps:\n    - id: cosign\n      env: {COSIGN_SHA256: a=1 a=2}\n",
		"no equals":       "runs:\n  steps:\n    - id: cosign\n      env: {COSIGN_SHA256: abc}\n",
		"not yaml":        "runs: [\n",
	} {
		if _, _, err := cosignPins([]byte(yml)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
