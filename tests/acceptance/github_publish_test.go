package acceptance

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"
)

// publishAction is the action directory, relative to this package.
const publishAction = "../../integrations/github/publish"

// publishSet is a TestSet of three tests, a, b and c.
const publishSet = `apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults:
  scope: {source: ["src/**"]}
  cacheability: hermetic
tests:
  - metadata: {name: "{n}"}
    runner: {command: ["true"]}
    matrix: {n: [a, b, c]}
`

// fakeOIDC is the Actions OIDC endpoint: each request with the job's
// request token and audience sigstore gets a new token oidc-<n>.
type fakeOIDC struct {
	mu sync.Mutex
	n  int
}

func (f *fakeOIDC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer req-tok" || r.URL.Query().Get("audience") != "sigstore" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	f.n++
	_, _ = fmt.Fprintf(w, `{"value":"oidc-%d"}`, f.n)
}

// publishStory is a project with a TestSet, an in-process registry, the
// contents API serving a policy whose registry is that registry, and the
// OIDC endpoint. The action's binary is a wrapper that logs each call with
// the OIDC token and registry credentials it got, then signs push with a
// key: the fake token cannot reach Fulcio.
type publishStory struct {
	dir, repo, policy, artifacts, log string
	api, oidc                         *httptest.Server
	contents                          *fakeContents
}

func newPublishStory(t *testing.T) *publishStory {
	t.Helper()
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	s := &publishStory{repo: strings.TrimPrefix(srv.URL, "http://") + "/acme/app-aval", contents: &fakeContents{}, artifacts: t.TempDir()}
	s.api = httptest.NewServer(s.contents)
	t.Cleanup(s.api.Close)
	s.oidc = httptest.NewServer(&fakeOIDC{})
	t.Cleanup(s.oidc.Close)
	keys := t.TempDir()
	key, pub := keyPair(t, keys, "ci")
	s.policy = "apiVersion: aval.trianalab.io/v1\nkind: Policy\nregistry: " + s.repo + " # from the default branch\nminTrustLevel: runner\nidentities:\n" +
		"  - publicKey: " + pub + "\n    trustLevel: runner\n"
	s.contents.set(s.policy)
	s.dir = writeProject(t, map[string]string{"aval.yaml": publishSet, "src/a.txt": "a\n", ".github/aval/policy.yaml": s.policy})
	s.log = filepath.Join(keys, "calls.log")
	wrapper := fmt.Sprintf(`#!/bin/bash
creds=$(cat "$DOCKER_CONFIG/config.json" 2>/dev/null || echo none)
echo "${SIGSTORE_ID_TOKEN:-none} $creds $*" >> %q
unset SIGSTORE_ID_TOKEN
if [ "$1" = push ]; then exec %q "$@" --key %q; fi
exec %q "$@"
`, s.log, aval, key, aval)
	if err := os.WriteFile(filepath.Join(keys, "aval"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *publishStory) wrapper() string { return filepath.Join(filepath.Dir(s.log), "aval") }

// suite runs test as the run action does, without a policy (a miss), and
// returns its .aval/<test>/ directory.
func (s *publishStory) suite(t *testing.T, test string, args ...string) string {
	t.Helper()
	if out, errOut, code := avalIn(t, s.dir, append([]string{"run", "--test", test}, args...)...); code != 0 {
		t.Fatalf("run %s: exit %d\n%s\n%s", test, code, out, errOut)
	}
	return filepath.Join(s.dir, ".aval", test)
}

// artifact copies the files of src into the artifact aval-<name>, after
// edit (when not nil) rewrote the report. Missing files are skipped.
func (s *publishStory) artifact(t *testing.T, name, src string, edit func(map[string]any)) {
	t.Helper()
	dst := filepath.Join(s.artifacts, "aval-"+name)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"result.json", "report.json", "record.json"} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if errors.Is(err, os.ErrNotExist) {
			continue // a reused result records no result.json
		} else if err != nil {
			t.Fatal(err)
		}
		if f == "report.json" && edit != nil {
			var r map[string]any
			if err := json.Unmarshal(b, &r); err != nil {
				t.Fatal(err)
			}
			edit(r)
			b, _ = json.Marshal(r)
		}
		if err := os.WriteFile(filepath.Join(dst, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type publishRun struct {
	out, summary string
	code         int
	calls        []string
}

// publish runs guard.sh and publish.sh in the project as the composite
// action does, on a push to main, with the artifacts downloaded so far.
func (s *publishStory) publish(t *testing.T, env ...string) publishRun {
	t.Helper()
	tmp := t.TempDir()
	summary := filepath.Join(tmp, "summary")
	abs, err := filepath.Abs(publishAction)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(s.log)
	var out bytes.Buffer
	code := 0
	for _, script := range []string{"guard.sh", "publish.sh"} {
		cmd := exec.Command("/bin/bash", filepath.Join(abs, script))
		cmd.Dir, cmd.Stdout, cmd.Stderr = s.dir, &out, &out
		cmd.Env = append(os.Environ(), "AVAL="+s.wrapper(), "ARTIFACTS="+s.artifacts,
			"INPUT_FILE=aval.yaml", "INPUT_POLICY=.github/aval/policy.yaml", "INPUT_STATS=true",
			"REGISTRY_USERNAME=bot", "REGISTRY_PASSWORD=pw", "ALLOW_UNPROTECTED=false",
			"DEFAULT_BRANCH=main", "GH_TOKEN=tok", "GITHUB_API_URL="+s.api.URL, "GITHUB_REPOSITORY=acme/app",
			"GITHUB_EVENT_NAME=push", "GITHUB_REF=refs/heads/main", "RUNNER_TEMP="+tmp, "GITHUB_STEP_SUMMARY="+summary,
			"ACTIONS_ID_TOKEN_REQUEST_URL="+s.oidc.URL+"/token?api-version=2.0", "ACTIONS_ID_TOKEN_REQUEST_TOKEN=req-tok",
			"SIGSTORE_ID_TOKEN=job-token", "AVAL_REGISTRY=evil.example/x", "AVAL_POLICY=/nonexistent")
		cmd.Env = append(cmd.Env, env...)
		var exit *exec.ExitError
		if err := cmd.Run(); errors.As(err, &exit) {
			code = exit.ExitCode()
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	r := publishRun{out: out.String(), code: code}
	b, _ := os.ReadFile(summary)
	r.summary = string(b)
	b, _ = os.ReadFile(s.log)
	r.calls = strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(b) == 0 {
		r.calls = nil
	}
	return r
}

// verify is aval verify for test against the registry and a local copy of
// the policy: 0 when a result for the current inputs is published.
func (s *publishStory) verify(t *testing.T, test string) int {
	t.Helper()
	_, _, code := avalIn(t, s.dir, "verify", "--test", test, "--registry", s.repo, "--policy", ".github/aval/policy.yaml")
	return code
}

func (s *publishStory) reset(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(s.artifacts); err != nil {
		t.Fatal(err)
	}
}

// pushes returns the push calls among calls.
func pushes(calls []string) []string {
	var p []string
	for _, c := range calls {
		if strings.Contains(c, " push -f aval.yaml --test ") {
			p = append(p, c)
		}
	}
	return p
}

// Every artifact is pushed with a fresh token, a forged artifact is refused
// without stopping the others, the job's AVAL_REGISTRY, AVAL_POLICY and
// SIGSTORE_ID_TOKEN are ignored, and run records follow successful pushes.
func TestGitHubPublishActionStory(t *testing.T) {
	s := newPublishStory(t)
	a, c := s.suite(t, "a"), s.suite(t, "c")
	s.artifact(t, "a", a, nil)
	s.artifact(t, "b", c, nil) // forged: aval-b holds test c
	s.artifact(t, "c", c, nil)
	r := s.publish(t)
	if r.code != 1 || !strings.Contains(r.out, "::error title=aval publish::b: aval push exited 1") {
		t.Fatalf("forged artifact: exit %d, want 1\n%s", r.code, r.out)
	}
	p := pushes(r.calls)
	auth := base64.StdEncoding.EncodeToString([]byte("bot:pw"))
	host := strings.SplitN(s.repo, "/", 2)[0]
	for i, call := range p {
		if !strings.HasPrefix(call, fmt.Sprintf("oidc-%d ", i+1)) || !strings.Contains(call, `{"auths":{"`+host+`":{"auth":"`+auth+`"}}}`) ||
			!strings.Contains(call, "--registry "+s.repo+" ") {
			t.Errorf("push %d: %q, want token oidc-%d, the registry credentials and the policy's registry", i, call, i+1)
		}
	}
	if len(p) != 3 {
		t.Fatalf("%d pushes, want 3 (one per artifact)\n%s", len(p), strings.Join(r.calls, "\n"))
	}
	for test, want := range map[string]int{"a": 0, "b": 4, "c": 0} {
		if got := s.verify(t, test); got != want {
			t.Errorf("verify %s: exit %d, want %d", test, got, want)
		}
	}
	if stats := strings.Count(strings.Join(r.calls, "\n"), " stats publish --registry "+s.repo+" --source-repo acme/app "); stats != 2 {
		t.Errorf("%d run records published, want 2 (a and c)", stats)
	}
	if !strings.Contains(r.summary, "| a | exit 0 |") || !strings.Contains(r.summary, "| b | exit 1 |") {
		t.Errorf("summary %q", r.summary)
	}

	// A result the suite reused is skipped: nothing is signed for it.
	s.reset(t)
	s.artifact(t, "a", s.suite(t, "a", "--registry", s.repo, "--policy", ".github/aval/policy.yaml"), nil)
	r = s.publish(t)
	if r.code != 0 || !strings.Contains(r.out, "nothing to push: a reused a verified result") {
		t.Fatalf("reused: exit %d\n%s", r.code, r.out)
	}
}

// Exit 5 from any push fails the step with 5, even when a later artifact
// fails with 1, and the other artifacts are still pushed.
func TestGitHubPublishActionSecurityEvent(t *testing.T) {
	s := newPublishStory(t)
	a, b := s.suite(t, "a"), s.suite(t, "b")
	s.artifact(t, "a", a, func(r map[string]any) { r["outcome"] = "verification-failed" })
	s.artifact(t, "b", b, nil)
	s.artifact(t, "c", b, nil) // forged
	r := s.publish(t)
	if r.code != 5 || !strings.Contains(r.out, "::error title=aval SECURITY::a: ") || len(pushes(r.calls)) != 3 {
		t.Fatalf("exit %d, %d pushes; want 5 and 3\n%s", r.code, len(pushes(r.calls)), r.out)
	}
	if s.verify(t, "a") != 4 || s.verify(t, "b") != 0 {
		t.Fatal("a must not be published and b must be")
	}
}

// Refs and events that must not sign, a job without id-token and a missing
// or registry-less policy are refused before anything is pushed.
func TestGitHubPublishActionRefuses(t *testing.T) {
	s := newPublishStory(t)
	s.artifact(t, "a", s.suite(t, "a"), nil)
	noRegistry := strings.Replace(s.policy, "registry: ", "# registry: ", 1)
	for name, tc := range map[string]struct {
		policy string
		env    []string
		code   int
		out    string
	}{
		"feature branch":      {s.policy, []string{"GITHUB_REF=refs/heads/feature"}, 1, "only the default branch (main) publishes"},
		"pull request":        {s.policy, []string{"GITHUB_EVENT_NAME=pull_request", "GITHUB_REF=refs/pull/1/merge"}, 1, "only the default branch"},
		"pull_request_target": {s.policy, []string{"GITHUB_EVENT_NAME=pull_request_target", "ALLOW_UNPROTECTED=true"}, 1, "refusing to publish on pull_request_target"},
		"workflow_run":        {s.policy, []string{"GITHUB_EVENT_NAME=workflow_run"}, 1, "refusing to publish on workflow_run"},
		"no id-token":         {s.policy, []string{"ACTIONS_ID_TOKEN_REQUEST_URL="}, 1, "this job has no OIDC token"},
		"bad allow value":     {s.policy, []string{"ALLOW_UNPROTECTED=yes"}, 2, "must be true or false"},
		"no policy":           {"", nil, 1, "no trust policy at .github/aval/policy.yaml in the default branch (main, HTTP 404)"},
		"no registry":         {noRegistry, nil, 1, "has no valid registry field"},
	} {
		s.contents.set(tc.policy)
		if r := s.publish(t, tc.env...); r.code != tc.code || len(r.calls) != 0 || !strings.Contains(r.out, tc.out) {
			t.Errorf("%s: exit %d, %d calls; want %d, none and %q\n%s", name, r.code, len(r.calls), tc.code, tc.out, r.out)
		}
	}
	s.contents.set(s.policy)
	if r := s.publish(t, "GITHUB_REF=refs/heads/feature", "ALLOW_UNPROTECTED=true"); r.code != 0 || len(pushes(r.calls)) != 1 {
		t.Fatalf("allow-unprotected: exit %d\n%s", r.code, r.out)
	}
	s.reset(t)
	if r := s.publish(t); r.code != 0 || !strings.Contains(r.out, "no aval-* artifacts") {
		t.Fatalf("no artifacts: exit %d\n%s", r.code, r.out)
	}
	s.artifact(t, "A_b", s.suite(t, "a"), nil)
	if r := s.publish(t); r.code != 2 || len(pushes(r.calls)) != 0 || !strings.Contains(r.out, "is not a valid metadata.name") {
		t.Fatalf("no artifacts: exit %d\n%s", r.code, r.out)
	}
}

// outputsSet is a TestSet whose test cov writes the declared output
// coverage.out.
const outputsSet = `apiVersion: aval.trianalab.io/v1
kind: TestSet
tests:
  - metadata: {name: cov}
    runner: {command: ["sh", "-c", "printf cov > coverage.out"]}
    scope: {source: ["src/**"]}
    cacheability: hermetic
    outputs: [coverage.out]
`

// outputsStory is a publish story whose contract is outputsSet. Its
// artifact func runs the run action for cov on a push to main, copies
// .aval/cov/ to the artifact aval-cov, lets edit change it, then deletes
// coverage.out from the project, as the publish job's checkout has none.
func outputsStory(t *testing.T) (*publishStory, func(t *testing.T, edit func(dst string))) {
	t.Helper()
	s := newPublishStory(t)
	if err := os.WriteFile(filepath.Join(s.dir, "aval.yaml"), []byte(outputsSet), 0o644); err != nil {
		t.Fatal(err)
	}
	cov := filepath.Join(s.dir, "coverage.out")
	return s, func(t *testing.T, edit func(dst string)) {
		t.Helper()
		s.reset(t)
		_ = os.Remove(cov)
		if r := action(t, s.dir, s.api.URL, s.repo, "push", "refs/heads/main", "INPUT_TEST=cov"); r.code != 0 || r.outputs["upload"] != "true" {
			t.Fatalf("run action: exit %d\n%s", r.code, r.out)
		}
		dst := filepath.Join(s.artifacts, "aval-cov")
		if err := os.CopyFS(dst, os.DirFS(filepath.Join(s.dir, ".aval", "cov"))); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(filepath.Join(dst, "outputs", "coverage.out")); err != nil || string(b) != "cov" {
			t.Fatalf("artifact outputs/coverage.out: %q %v", b, err)
		}
		if edit != nil {
			edit(dst)
		}
		if err := os.Remove(cov); err != nil {
			t.Fatal(err)
		}
	}
}

// The run action carries a test's outputs in its artifact (#236): the
// publish action pushes them from there, in a checkout without them, the
// attestation verifies, and a later run reuses it and restores them.
func TestGitHubActionsCarryOutputs(t *testing.T) {
	s, artifact := outputsStory(t)
	artifact(t, nil)
	if r := s.publish(t); r.code != 0 || len(pushes(r.calls)) != 1 || !strings.Contains(pushes(r.calls)[0], "--outputs-dir "+filepath.Join(s.artifacts, "aval-cov", "outputs")) {
		t.Fatalf("publish: exit %d\n%s\n%s", r.code, r.out, strings.Join(r.calls, "\n"))
	}
	if code := s.verify(t, "cov"); code != 0 {
		t.Fatalf("verify cov: exit %d, want 0", code)
	}
	if out, errOut, code := avalIn(t, s.dir, "run", "--test", "cov", "--registry", s.repo, "--policy", ".github/aval/policy.yaml"); code != 0 || !strings.Contains(out, "restored coverage.out") {
		t.Fatalf("reuse: exit %d\n%s\n%s", code, out, errOut)
	}
	if b, err := os.ReadFile(filepath.Join(s.dir, "coverage.out")); err != nil || string(b) != "cov" {
		t.Fatalf("restored coverage.out: %q %v", b, err)
	}
}

// An artifact with anything outside its layout, an outputs/ entry that
// is not a declared output, or no outputs/ for a contract that declares
// outputs (never the checkout's), is refused for that test with exit 1 and
// nothing is pushed.
func TestGitHubActionsRefuseBadOutputs(t *testing.T) {
	s, artifact := outputsStory(t)
	put := func(rel, body string) func(string) {
		return func(dst string) {
			p := filepath.Join(dst, filepath.FromSlash(rel))
			if err := errors.Join(os.MkdirAll(filepath.Dir(p), 0o755), os.WriteFile(p, []byte(body), 0o644)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, tc := range map[string]struct {
		edit func(string)
		want string
	}{
		"file outside the layout": {put("evil.sh", "x"), "artifact aval-cov holds evil.sh, which is not part of the artifact layout"},
		"undeclared output":       {put("outputs/notes.txt", "x"), "notes.txt, which is not a declared output"},
		"output escaping by symlink": {func(dst string) {
			p := filepath.Join(dst, "outputs", "coverage.out")
			if err := errors.Join(os.Remove(p), os.Symlink("/etc/hosts", p)); err != nil {
				t.Fatal(err)
			}
		}, "coverage.out is not a regular file"},
	} {
		artifact(t, tc.edit)
		if r := s.publish(t); r.code != 1 || !strings.Contains(r.out, tc.want) || s.verify(t, "cov") != 4 {
			t.Fatalf("%s: exit %d, want 1, ~%q and nothing published\n%s", name, r.code, tc.want, r.out)
		}
	}

	// No outputs/: the checkout's coverage.out is never signed instead.
	artifact(t, func(dst string) { _ = os.RemoveAll(filepath.Join(dst, "outputs")) })
	if err := os.WriteFile(filepath.Join(s.dir, "coverage.out"), []byte("committed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := s.publish(t); r.code != 1 || !strings.Contains(r.out, "outputs: no such file or directory") || s.verify(t, "cov") != 4 {
		t.Fatalf("no outputs/: exit %d, want 1 and nothing published\n%s", r.code, r.out)
	}
}

// oldAval is real as a release without --outputs-dir (v0.8.1): its help
// does not list the flag and it rejects it as cobra does.
func oldAval(t *testing.T, real string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "aval")
	script := fmt.Sprintf(`#!/bin/bash
for a in "$@"; do
  if [ "$a" = --outputs-dir ]; then echo 'Error: unknown flag: --outputs-dir' >&2; exit 2; fi
done
if [ "${2:-}" = --help ]; then %q "$@" | grep -v -- --outputs-dir; exit 0; fi
exec %q "$@"
`, real, real)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// With a pinned release that has no --outputs-dir (v0.8.1), the actions
// still run and publish a test without outputs, and refuse a test with
// outputs (exit 1, naming the release it needs) instead of running it
// without carrying them or publishing the checkout's files (#236).
func TestGitHubActionsOldReleaseOutputs(t *testing.T) {
	s, _ := outputsStory(t)
	set := outputsSet + `  - metadata: {name: plain}
    runner: {command: ["true"]}
    scope: {source: ["src/**"]}
    cacheability: hermetic
`
	if err := os.WriteFile(filepath.Join(s.dir, "aval.yaml"), []byte(set), 0o644); err != nil {
		t.Fatal(err)
	}
	old := oldAval(t, aval)
	r := action(t, s.dir, s.api.URL, s.repo, "push", "refs/heads/main", "INPUT_TEST=cov", "AVAL="+old)
	if _, err := os.Stat(filepath.Join(s.dir, ".aval", "cov", "result.json")); r.code != 1 || !strings.Contains(r.out, "cov declares outputs and the installed aval has no run --outputs-dir") ||
		!strings.Contains(r.out, "aval v0.8.2 or later") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run action cov: exit %d, want 1 and no run (%v)\n%s", r.code, err, r.out)
	}
	if r := action(t, s.dir, s.api.URL, s.repo, "push", "refs/heads/main", "INPUT_TEST=plain", "AVAL="+old); r.code != 0 || r.outputs["upload"] != "true" {
		t.Fatalf("run action plain: exit %d\n%s", r.code, r.out)
	}
	s.artifact(t, "plain", filepath.Join(s.dir, ".aval", "plain"), nil)
	s.artifact(t, "cov", s.suite(t, "cov"), nil)
	if err := os.Remove(filepath.Join(s.dir, "coverage.out")); err != nil {
		t.Fatal(err)
	}
	// The publish wrapper signs with a key; the old release wraps it.
	p := s.publish(t, "AVAL="+oldAval(t, s.wrapper()))
	if p.code != 1 || !strings.Contains(p.out, "cov declares outputs and the installed aval has no push --outputs-dir") || len(pushes(p.calls)) != 1 {
		t.Fatalf("publish: exit %d, want 1 and one push\n%s\n%s", p.code, p.out, strings.Join(p.calls, "\n"))
	}
	if strings.Contains(pushes(p.calls)[0], "--outputs-dir") {
		t.Fatalf("push of plain passed --outputs-dir to a release without it: %s", pushes(p.calls)[0])
	}
	if s.verify(t, "plain") != 0 || s.verify(t, "cov") != 4 {
		t.Fatal("want plain published and cov not")
	}
}
