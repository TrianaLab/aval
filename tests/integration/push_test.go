package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"

	"github.com/TrianaLab/aval/internal/attest"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// pushEnv is the aval binary, an in-process registry repository, a key the
// policy trusts and a project with a TestSet of tests a and b that declare
// the env var AVAL_T210.
type pushEnv struct {
	t                        *testing.T
	bin, repo, key, pol, dir string
}

func newPushEnv(t *testing.T) pushEnv {
	t.Helper()
	e := pushEnv{t: t, bin: filepath.Join(t.TempDir(), "aval"), dir: t.TempDir()}
	if out, err := exec.Command("go", "build", "-o", e.bin, "../../cmd/aval").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	e.repo = strings.TrimPrefix(srv.URL, "http://") + "/acme/push-test-aval"
	keys := t.TempDir()
	if _, err := attest.WriteKeyPair(filepath.Join(keys, "aval"), false); err != nil {
		t.Fatal(err)
	}
	e.key, e.pol = filepath.Join(keys, "aval.key"), filepath.Join(keys, "policy.yaml")
	files := map[string]string{
		e.pol:                                "apiVersion: aval.trianalab.io/v1\nkind: Policy\nminTrustLevel: developer\nidentities:\n  - publicKey: aval.pub\n    trustLevel: developer\n",
		filepath.Join(e.dir, ".git", "HEAD"): "ref: refs/heads/main\n",
		filepath.Join(e.dir, "in.txt"):       "in\n",
		filepath.Join(e.dir, "aval.yaml"): `apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults: {scope: {source: [in.txt]}, cacheability: hermetic, environment: {env: [AVAL_T210]}}
tests:
- {metadata: {name: a}, runner: {command: ["true"]}}
- {metadata: {name: b}, runner: {command: ["true"]}}
`,
	}
	for p, data := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// aval runs the binary in the project with AVAL_T210=env and returns its
// combined output and exit code.
func (e pushEnv) aval(env string, args ...string) (string, int) {
	e.t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(e.bin, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = e.dir, &out, &out
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.t.TempDir(), "DOCKER_CONFIG=" + e.t.TempDir(), "AVAL_T210=" + env}
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		e.t.Fatal(err)
	}
	return out.String(), cmd.ProcessState.ExitCode()
}

// fingerprint is the fingerprint in the report aval run wrote for test.
func (e pushEnv) fingerprint(test string) string {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.dir, ".aval", test, "report.json"))
	var r evidence.Report
	if err == nil {
		err = json.Unmarshal(raw, &r)
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Fingerprint
}

// published reports whether the registry holds a result for fp.
func (e pushEnv) published(fp string) bool {
	e.t.Helper()
	_, err := registry.Lookup(context.Background(), e.repo, fp)
	if err != nil && !errors.Is(err, evidence.ErrNotFound) {
		e.t.Fatal(err)
	}
	return err == nil
}

// refused pushes test a with test b's files, then with a changed declared
// env value, and fails unless each exits 1.
func (e pushEnv) refused(push []string) {
	e.t.Helper()
	other := filepath.Join(".aval", "b")
	for name, args := range map[string][]string{
		"report of b":            {"--report-file", filepath.Join(other, "report.json")},
		"result of b":            {"--result-file", filepath.Join(other, "result.json")},
		"result and report of b": {"--result-file", filepath.Join(other, "result.json"), "--report-file", filepath.Join(other, "report.json")},
	} {
		if out, code := e.aval("1", append(push, args...)...); code != 1 || !strings.Contains(out, `for test "b"`) {
			e.t.Fatalf("%s: exit %d, want 1\n%s", name, code, out)
		}
	}
	// False hit: the declared env value changed between run and push.
	if out, code := e.aval("2", push...); code != 1 || !strings.Contains(out, "run the test again") {
		e.t.Fatalf("changed env: exit %d, want 1\n%s", code, out)
	}
}

// Issue #210: the aval binary against an in-process OCI registry. A result
// and a report forged for test a from test b's files, and a result whose
// declared env value changed after the run, are refused (exit 1) and leave
// nothing in the registry; the next lookup for the changed inputs misses.
// The genuine files of a publish and verify.
func TestPushTestRefusesForgedReport(t *testing.T) {
	e := newPushEnv(t)
	for _, n := range []string{"a", "b"} {
		if out, code := e.aval("1", "run", "--test", n); code != 0 {
			t.Fatalf("run %s: exit %d\n%s", n, code, out)
		}
	}
	fpA, fpB := e.fingerprint("a"), e.fingerprint("b")
	push := []string{"push", "--registry", e.repo, "--key", e.key, "--test", "a"}
	e.refused(push)
	if e.published(fpA) || e.published(fpB) {
		t.Fatal("a refused push published a result")
	}
	if out, code := e.aval("1", push...); code != 0 || !strings.Contains(out, "pushed "+e.repo+"@sha256:") {
		t.Fatalf("own files: exit %d\n%s", code, out)
	}
	if !e.published(fpA) || e.published(fpB) {
		t.Fatal("the genuine push did not publish exactly a's result")
	}
	verify := []string{"verify", "--test", "a", "--registry", e.repo, "--policy", e.pol}
	// The changed env is a miss (exit 4), never a hit.
	for env, want := range map[string]int{"1": 0, "2": 4} {
		if out, code := e.aval(env, verify...); code != want {
			t.Fatalf("verify with AVAL_T210=%s: exit %d, want %d\n%s", env, code, want, out)
		}
	}
}
