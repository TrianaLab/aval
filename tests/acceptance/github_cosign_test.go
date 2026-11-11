package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// actionStepOf returns the env and run script of the step with id in the
// run action's action.yml.
func actionStepOf(t *testing.T, id string) (map[string]string, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runAction, "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		Runs struct {
			Steps []struct {
				ID  string            `yaml:"id"`
				Env map[string]string `yaml:"env"`
				Run string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &a); err != nil {
		t.Fatal(err)
	}
	for _, s := range a.Runs.Steps {
		if s.ID == id {
			return s.Env, s.Run
		}
	}
	t.Fatalf("no step %s", id)
	return nil, ""
}

// stepEnv is env as KEY=value pairs, after the test's own environment.
func stepEnv(env map[string]string, extra ...string) []string {
	out := os.Environ()
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return append(out, extra...)
}

// getCosign runs integrations/github/get-cosign.sh as the cosign step does,
// with the step's env and the release assets under base. It returns the
// output, RUNNER_TEMP, the step outputs and the exit error.
func getCosign(t *testing.T, env map[string]string, base string) (out, tmp, outputs string, err error) {
	t.Helper()
	tmp = t.TempDir()
	file := filepath.Join(tmp, "github-output")
	cmd := exec.Command("/bin/bash", filepath.Join(runAction, "..", "get-cosign.sh"))
	cmd.Env = stepEnv(env, "COSIGN_BASE_URL="+base, "RUNNER_TEMP="+tmp, "GITHUB_OUTPUT="+file)
	b, err := cmd.CombinedOutput()
	o, _ := os.ReadFile(file)
	return string(b), tmp, string(o), err
}

// The cosign step installs only a binary with the sha256 pinned for this
// runner (#248). A tampered download, or a runner with no pin, fails and
// installs nothing.
func TestGitHubActionCosignIsPinned(t *testing.T) {
	asset := "cosign-" + runtime.GOOS + "-" + runtime.GOARCH
	body := "#!/bin/sh\necho cosign\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3.0.6/"+asset {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	env, _ := actionStepOf(t, "cosign")
	if env["COSIGN_VERSION"] != "v3.0.6" {
		t.Fatalf("this test serves v3.0.6, the action pins %s: update it", env["COSIGN_VERSION"])
	}

	// The action's own pins: the served binary is not the real cosign.
	if out, tmp, o, err := getCosign(t, env, srv.URL); err == nil || !strings.Contains(out, "sha256 mismatch") {
		t.Fatalf("tampered binary: err %v, output %s", err, out)
	} else if _, statErr := os.Stat(filepath.Join(tmp, "aval-cosign-v3.0.6")); !os.IsNotExist(statErr) || o != "" {
		t.Fatalf("tampered binary installed (stat %v, outputs %q)", statErr, o)
	}

	sum := sha256.Sum256([]byte(body))
	good := map[string]string{"COSIGN_VERSION": "v3.0.6", "COSIGN_SHA256": "cosign-plan9-amd64=" + strings.Repeat("0", 64) + " " + asset + "=" + hex.EncodeToString(sum[:])}
	out, tmp, o, err := getCosign(t, good, srv.URL)
	if err != nil {
		t.Fatalf("pinned binary: %v\n%s", err, out)
	}
	dir := filepath.Join(tmp, "aval-cosign-v3.0.6")
	if got, _ := os.ReadFile(filepath.Join(dir, "cosign")); string(got) != body || o != "dir="+dir+"\n" {
		t.Fatalf("installed %q, outputs %q", got, o)
	}

	for name, pins := range map[string]string{
		"no pin for this runner": "cosign-plan9-amd64=" + hex.EncodeToString(sum[:]),
		"pinned twice":           good["COSIGN_SHA256"] + " " + asset + "=" + hex.EncodeToString(sum[:]),
		"malformed pin":          asset + "=" + strings.ToUpper(hex.EncodeToString(sum[:])),
	} {
		out, tmp, o, err := getCosign(t, map[string]string{"COSIGN_VERSION": "v3.0.6", "COSIGN_SHA256": pins}, srv.URL)
		if _, statErr := os.Stat(filepath.Join(tmp, "aval-cosign-v3.0.6")); err == nil || !os.IsNotExist(statErr) || o != "" {
			t.Errorf("%s: err %v, stat %v, outputs %q\n%s", name, err, statErr, o, out)
		}
	}
}

// The install step runs get-aval.sh with the private cosign, never a cosign
// that is earlier on the job's PATH or beside gh (#248). An empty cosign dir
// fails before get-aval.sh runs.
func TestGitHubActionInstallUsesPrivateCosign(t *testing.T) {
	env, run := actionStepOf(t, "install")
	tmp := t.TempDir()
	mark := filepath.Join(tmp, "ran")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// get-aval.sh, as the step finds it from GITHUB_ACTION_PATH, only calls cosign.
	actionPath := filepath.Join(tmp, "repo", "integrations", "github", "run")
	write(filepath.Join(tmp, "repo", "scripts", "get-aval.sh"), "cosign\n")
	if err := os.MkdirAll(actionPath, 0o755); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(tmp, "aval-cosign-v3.0.6")
	write(filepath.Join(private, "cosign"), "#!/bin/bash\necho private >> \"$MARK\"\n")
	shadow := filepath.Join(tmp, "shadow")
	write(filepath.Join(shadow, "cosign"), "#!/bin/bash\necho shadow >> \"$MARK\"\n")
	write(filepath.Join(shadow, "gh"), "#!/bin/bash\n")

	step := func(cosignDir string) (string, error) {
		env["COSIGN_DIR"] = cosignDir
		cmd := exec.Command("/bin/bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", run)
		cmd.Env = stepEnv(env, "PATH="+shadow+":/usr/bin:/bin", "MARK="+mark, "RUNNER_TEMP="+tmp,
			"GITHUB_ACTION_PATH="+actionPath, "GITHUB_OUTPUT="+filepath.Join(tmp, "out"))
		out, err := cmd.CombinedOutput()
		ran, _ := os.ReadFile(mark)
		_ = os.Remove(mark)
		if err != nil {
			err = fmt.Errorf("%w\n%s", err, out)
		}
		return string(ran), err
	}
	if ran, err := step(private); err != nil || ran != "private\n" {
		t.Fatalf("ran %q (want private), err %v", ran, err)
	}
	for _, dir := range []string{"", "relative", filepath.Join(tmp, "missing")} {
		if ran, err := step(dir); err == nil || ran != "" {
			t.Errorf("COSIGN_DIR %q: ran %q, err %v", dir, ran, err)
		}
	}
}
