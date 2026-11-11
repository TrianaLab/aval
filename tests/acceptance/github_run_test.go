package acceptance

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"
)

// runAction is the action directory, relative to this package.
const runAction = "../../integrations/github/run"

// fakeContents is the GitHub contents API for one file of acme/app: it
// serves policy (404 when empty) for the request with token tok and records
// every query.
type fakeContents struct {
	mu      sync.Mutex
	policy  string
	queries []string
}

func (f *fakeContents) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, r.URL.Path+"?"+r.URL.RawQuery)
	if r.URL.Path != "/repos/acme/app/contents/.github/aval/policy.yaml" || r.Header.Get("Authorization") != "Bearer tok" ||
		r.Header.Get("Accept") != "application/vnd.github.raw+json" || f.policy == "" {
		http.NotFound(w, r)
		return
	}
	_, _ = io.WriteString(w, f.policy)
}

func (f *fakeContents) set(policy string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policy, f.queries = policy, nil
}

type actionRun struct {
	out, summary string
	code         int
	outputs      map[string]string
}

// action runs the action's guard.sh and run.sh in dir as the composite
// action does, with the environment of a GitHub-hosted runner for event on
// ref. The job sets AVAL_REGISTRY and AVAL_POLICY to the checkout's policy:
// the action must ignore both.
func action(t *testing.T, dir, api, repo, event, ref string, env ...string) actionRun {
	t.Helper()
	tmp := t.TempDir()
	outputs, summary := filepath.Join(tmp, "output"), filepath.Join(tmp, "summary")
	abs, err := filepath.Abs(runAction)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := 0
	for _, script := range []string{"guard.sh", "run.sh"} {
		cmd := exec.Command("/bin/bash", filepath.Join(abs, script))
		cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &out, &out
		cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+t.TempDir(), "AVAL="+aval,
			"INPUT_TEST=story", "INPUT_FILE=aval.yaml", "INPUT_POLICY=.github/aval/policy.yaml",
			"DEFAULT_BRANCH=main", "GH_TOKEN=tok", "GITHUB_API_URL="+api, "GITHUB_REPOSITORY=acme/app",
			"GITHUB_SERVER_URL=https://github.com", "GITHUB_RUN_ID=1",
			"GITHUB_WORKFLOW_REF=acme/app/.github/workflows/ci.yaml@"+ref,
			"GITHUB_EVENT_NAME="+event, "GITHUB_REF="+ref, "RUNNER_TEMP="+tmp,
			"GITHUB_OUTPUT="+outputs, "GITHUB_STEP_SUMMARY="+summary,
			"AVAL_REGISTRY="+repo, "AVAL_POLICY="+filepath.Join(dir, ".github", "aval", "policy.yaml"))
		cmd.Env = append(cmd.Env, env...)
		var exit *exec.ExitError
		if err := cmd.Run(); errors.As(err, &exit) {
			code = exit.ExitCode()
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	r := actionRun{out: out.String(), code: code, outputs: map[string]string{}}
	b, _ := os.ReadFile(summary)
	r.summary = string(b)
	b, _ = os.ReadFile(outputs)
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			r.outputs[k] = v
		}
	}
	return r
}

// actionStory is a project, an in-process registry and a fake contents API
// for the aval run action (#211). The default branch's policy trusts CI at
// runner level and lists developers below it; the checkout's copy (the PR's
// edit) makes developer results reusable.
type actionStory struct {
	dir, root, repo, mainPolicy string
	ciKey, devKey               string
	api                         *httptest.Server
	contents                    *fakeContents
}

func newActionStory(t *testing.T) *actionStory {
	t.Helper()
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	s := &actionStory{repo: strings.TrimPrefix(srv.URL, "http://") + "/acme/app-aval", contents: &fakeContents{}}
	s.api = httptest.NewServer(s.contents)
	t.Cleanup(s.api.Close)
	keys := t.TempDir()
	var ciPub, devPub string
	s.ciKey, ciPub = keyPair(t, keys, "ci")
	s.devKey, devPub = keyPair(t, keys, "dev")
	s.mainPolicy = "apiVersion: aval.trianalab.io/v1\nkind: Policy\nregistry: " + s.repo + "\nminTrustLevel: runner\nidentities:\n" +
		"  - publicKey: " + ciPub + "\n    trustLevel: runner\n  - publicKey: " + devPub + "\n    trustLevel: developer\n"
	prPolicy := strings.Replace(s.mainPolicy, "minTrustLevel: runner", "minTrustLevel: developer", 1)
	runner := strings.Replace(contract, `["true"]`, `["sh", "-c", "echo ran >> ../runs.log"]`, 1)
	s.root = writeProject(t, map[string]string{"app/aval.yaml": runner, "app/src/a.txt": "a\n", "app/.github/aval/policy.yaml": prPolicy})
	s.dir = filepath.Join(s.root, "app")
	s.contents.set(s.mainPolicy)
	return s
}

func (s *actionStory) runs() int {
	b, _ := os.ReadFile(filepath.Join(s.root, "runs.log"))
	return strings.Count(string(b), "ran\n")
}

func (s *actionStory) publish(t *testing.T, key string) {
	t.Helper()
	if out, errOut, code := avalIn(t, s.dir, "run", "-f", "aval.yaml", "--test", "story", "--registry", s.repo, "--push", "--key", key, "--no-reuse"); code != 0 {
		t.Fatalf("publish: exit %d\n%s\n%s", code, out, errOut)
	}
}

func (s *actionStory) write(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, "src", "a.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (s *actionStory) pr(t *testing.T, env ...string) actionRun {
	t.Helper()
	return action(t, s.dir, s.api.URL, s.repo, "pull_request", "refs/pull/1/merge", env...)
}

func (s *actionStory) want(t *testing.T, name string, r actionRun, code, ranTimes int, outcome string, out ...string) {
	t.Helper()
	ok := r.code == code && s.runs() == ranTimes && r.outputs["outcome"] == outcome
	for _, o := range out {
		ok = ok && strings.Contains(r.out, o)
	}
	if !ok {
		t.Fatalf("%s: exit %d, %d runs, outcome %q; want %d, %d, %q and %q\n%s", name, r.code, s.runs(), r.outputs["outcome"], code, ranTimes, outcome, out, r.out)
	}
}

// Hit, the artifact on a default-branch push only, no policy on the default
// branch (nothing reused), miss, a policy edited in the PR (ignored) and a
// tampered attestation (exit 5, annotation, failed step).
func TestGitHubRunActionStory(t *testing.T) {
	s := newActionStory(t)
	s.publish(t, s.ciKey)
	pr := s.pr(t)
	s.want(t, "hit", pr, 0, 1, "verified", "trust policy: .github/aval/policy.yaml from the default branch (main)")
	if pr.outputs["upload"] != "false" || !strings.Contains(pr.summary, "### aval story: `verified` passed (exit 0)") {
		t.Fatalf("hit: upload %q, summary %q", pr.outputs["upload"], pr.summary)
	}
	if q := s.contents.queries; len(q) != 1 || q[0] != "/repos/acme/app/contents/.github/aval/policy.yaml?ref=main" {
		t.Fatalf("contents API queries %q, want the policy at ref=main", q)
	}
	for ref, upload := range map[string]string{"refs/heads/main": "true", "refs/heads/feature": "false"} {
		if r := action(t, s.dir, s.api.URL, s.repo, "push", ref); r.code != 0 || r.outputs["upload"] != upload {
			t.Fatalf("push to %s: exit %d, upload %q, want %s\n%s", ref, r.code, r.outputs["upload"], upload, r.out)
		}
	}
	if _, err := os.Stat(filepath.Join(s.dir, ".aval", "story", "report.json")); err != nil {
		t.Fatalf("no report in the artifact directory: %v", err)
	}

	// The job's AVAL_REGISTRY and AVAL_POLICY name a registry and a policy:
	// ignored.
	s.contents.set("")
	s.want(t, "no policy", s.pr(t), 0, 2, "miss",
		"::warning title=aval run::no trust policy at .github/aval/policy.yaml in the default branch (main, HTTP 404): nothing is reused")

	s.contents.set(s.mainPolicy)
	s.write(t, "b\n")
	s.want(t, "miss", s.pr(t), 0, 3, "miss")

	// A developer result is reusable under the PR's policy only.
	s.publish(t, s.devKey)
	s.want(t, "policy edited in the PR", s.pr(t), 0, 5, "miss")

	s.write(t, "a\n")
	tamper(t, s.repo, s.dir, func() {})
	r := s.pr(t)
	s.want(t, "tampered attestation", r, 5, 6, "verification-failed", "::error title=aval SECURITY::story: ")
	if !strings.Contains(r.summary, "`verification-failed`") || !strings.Contains(r.summary, "(exit 5)") {
		t.Fatalf("tampered: summary %q", r.summary)
	}
}

// Events that run PR code with the base repository's credentials, and a job
// that can sign, are refused before anything runs.
func TestGitHubRunActionRefuses(t *testing.T) {
	s := newActionStory(t)
	for name, env := range map[string][]string{
		"pull_request_target": {"GITHUB_EVENT_NAME=pull_request_target"},
		"workflow_run":        {"GITHUB_EVENT_NAME=workflow_run"},
		"id-token: write":     {"ACTIONS_ID_TOKEN_REQUEST_URL=https://token.example/x"},
	} {
		if r := s.pr(t, env...); r.code != 1 || s.runs() != 0 || len(s.contents.queries) != 0 || !strings.Contains(r.out, "::error title=aval run::") {
			t.Fatalf("%s: exit %d, %d runs\n%s", name, r.code, s.runs(), r.out)
		}
	}
}

// A default-branch policy that trusts a refs/pull/ subject at or above
// minTrustLevel gets a warning.
func TestGitHubRunActionWarnsOnPullRequestTrust(t *testing.T) {
	s := newActionStory(t)
	const warn = "::warning title=aval policy::"
	keyless := "  - issuer: https://token.actions.githubusercontent.com\n    subjectRegexp: %s\n    trustLevel: %s\n"
	for name, tc := range map[string]struct {
		subject, level string
		warns          bool
	}{
		"refs/pull at runner":   {`https://github\.com/acme/app/\.github/workflows/ci\.yaml@refs/pull/.*`, "runner", true},
		"any ref at runner":     {`'https://github\.com/acme/app/\.github/workflows/ci\.yaml@refs/.*'`, "runner", true},
		"refs/pull below min":   {`https://github\.com/acme/app/\.github/workflows/ci\.yaml@refs/pull/.*`, "developer", false},
		"default branch only":   {`"https://github\\.com/acme/app/\\.github/workflows/ci\\.yaml@refs/heads/main"`, "runner", false},
		"quoted any ref (YAML)": {`"https://github\\.com/acme/app/.*"`, "isolated", true},
	} {
		s.contents.set(s.mainPolicy + fmt.Sprintf(keyless, tc.subject, tc.level))
		if r := s.pr(t); strings.Contains(r.out, warn) != tc.warns {
			t.Errorf("%s: warning %v, want %v\n%s", name, !tc.warns, tc.warns, r.out)
		}
	}
}

// shadow: true runs the test beside a verified result, and a bad value is
// refused before anything runs (#216).
func TestGitHubRunActionShadow(t *testing.T) {
	s := newActionStory(t)
	s.publish(t, s.ciKey)
	s.want(t, "shadow", s.pr(t, "INPUT_SHADOW=true"), 0, 2, "verified")
	if r := s.pr(t, "INPUT_SHADOW=yes"); r.code != 2 || s.runs() != 2 || !strings.Contains(r.out, `shadow: "yes" must be true or false`) {
		t.Fatalf("shadow: yes: exit %d, %d runs\n%s", r.code, s.runs(), r.out)
	}
}

// recordingAval wraps aval in a script that logs each command line, so a
// test sees the --min-trust-level the action passes.
type recordingAval struct{ env, log string }

func newRecordingAval(t *testing.T) recordingAval {
	t.Helper()
	dir := t.TempDir()
	r := recordingAval{log: filepath.Join(dir, "args.log")}
	script := filepath.Join(dir, "aval")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$*\" >> '"+r.log+"'\nexec '"+aval+"' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.env = "AVAL=" + script
	return r
}

// minTrust is the --min-trust-level of the logged aval run, "" without the
// flag and "no run" when aval run was not called; it clears the log.
func (r recordingAval) minTrust(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(r.log)
	_ = os.Remove(r.log)
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || f[0] != "run" || f[1] != "-f" {
			continue
		}
		if i := slices.Index(f, "--min-trust-level"); i >= 0 && i+1 < len(f) {
			return f[i+1]
		}
		return ""
	}
	return "no run"
}

// The action raises the minimum to runner on the default branch, so main
// never goes green on a developer result that aval push would not publish
// (#251). The highest of the input, the job's AVAL_MIN_TRUST_LEVEL and the
// policy's minimum wins; the action never passes one below the policy's.
func TestGitHubRunActionMinTrustLevel(t *testing.T) {
	s := newActionStory(t)
	rec := newRecordingAval(t)
	policy := func(min string) string {
		return strings.Replace(s.mainPolicy, "minTrustLevel: runner", "minTrustLevel: "+min, 1)
	}
	s.contents.set(policy("developer"))
	s.publish(t, s.devKey)
	runs := s.runs()
	for _, tc := range []struct {
		name, policy, event, ref string
		env                      []string
		code                     int
		min, outcome             string
		ran                      bool
	}{
		{"pull request", "developer", "pull_request", "refs/pull/1/merge", nil, 0, "", "verified", false},
		{"push to another branch", "developer", "push", "refs/heads/feature", nil, 0, "", "verified", false},
		// A developer result on main is a miss, never a verification failure.
		{"push to the default branch", "developer", "push", "refs/heads/main", nil, 0, "runner", "miss", true},
		{"schedule on the default branch", "developer", "schedule", "refs/heads/main", nil, 0, "runner", "miss", true},
		{"renamed default branch", "developer", "push", "refs/heads/trunk", []string{"DEFAULT_BRANCH=trunk"}, 0, "runner", "miss", true},
		{"opt out", "developer", "push", "refs/heads/main", []string{"INPUT_MIN_TRUST_LEVEL=developer"}, 0, "developer", "verified", false},
		{"explicit runner on a PR", "developer", "pull_request", "refs/pull/1/merge", []string{"INPUT_MIN_TRUST_LEVEL=runner"}, 0, "runner", "miss", true},
		{"job isolated beats auto", "developer", "pull_request", "refs/pull/1/merge", []string{"AVAL_MIN_TRUST_LEVEL=isolated"}, 0, "isolated", "miss", true},
		{"job isolated beats runner on main", "developer", "push", "refs/heads/main", []string{"AVAL_MIN_TRUST_LEVEL=isolated"}, 0, "isolated", "miss", true},
		{"job developer is raised to the policy's", "runner", "pull_request", "refs/pull/1/merge", []string{"AVAL_MIN_TRUST_LEVEL=developer"}, 0, "runner", "miss", true},
		{"input developer is raised to the policy's", "runner", "push", "refs/heads/main", []string{"INPUT_MIN_TRUST_LEVEL=developer"}, 0, "runner", "miss", true},
		// Not runner below the policy's isolated, which aval would refuse (exit 2).
		{"policy isolated on the default branch", "isolated", "push", "refs/heads/main", nil, 0, "isolated", "miss", true},
		// An unparsed minimum leaves the requested level to aval, which
		// refuses one below the policy's: fail closed.
		{"unparsed policy minimum", "!!str isolated", "push", "refs/heads/main", nil, 2, "runner", "unknown", false},
		{"bogus input", "developer", "pull_request", "refs/pull/1/merge", []string{"INPUT_MIN_TRUST_LEVEL=bogus"}, 2, "no run", "", false},
		{"bogus job level", "developer", "pull_request", "refs/pull/1/merge", []string{"AVAL_MIN_TRUST_LEVEL=bogus"}, 2, "no run", "", false},
	} {
		s.contents.set(policy(tc.policy))
		if tc.ran {
			runs++
		}
		r := action(t, s.dir, s.api.URL, s.repo, tc.event, tc.ref, append([]string{rec.env}, tc.env...)...)
		s.want(t, tc.name, r, tc.code, runs, tc.outcome)
		if got := rec.minTrust(t); got != tc.min {
			t.Errorf("%s: --min-trust-level %q, want %q\n%s", tc.name, got, tc.min, r.out)
		}
		switch {
		case tc.min == "no run":
			if len(s.contents.queries) != 0 || !strings.Contains(r.out, "::error title=aval run::") {
				t.Errorf("%s: policy fetched or no annotation\n%s", tc.name, r.out)
			}
		case tc.code == 0:
			want := cmp.Or(tc.min, tc.policy)
			if !strings.Contains(r.out, "trust minimum: "+want+"\n") || !strings.Contains(r.summary, "Trust minimum: "+want+"\n") {
				t.Errorf("%s: effective minimum %q not logged\n%s\n%s", tc.name, want, r.out, r.summary)
			}
		}
	}

	// A default branch name with shell metacharacters, glob characters or a
	// refs/heads/ prefix never makes another ref count as the default branch.
	s.contents.set(policy("developer"))
	for _, tc := range []struct{ branch, ref string }{
		{"ma*", "refs/heads/main"},
		{"m?in", "refs/heads/main"},
		{"[m]ain", "refs/heads/main"},
		{"*", "refs/heads/main"},
		{"$(touch pwned)main", "refs/heads/main"},
		{"`touch pwned`main", "refs/heads/main"},
		{"main; touch pwned", "refs/heads/main"},
		{"main\n", "refs/heads/main"},
		{" main", "refs/heads/main"},
		{"refs/heads/main", "refs/heads/main"},
		{"heads/main", "refs/heads/main"},
		{"main", "refs/heads/main/x"},
		{"main", "refs/heads/mainx"},
		{"main", "refs/tags/main"},
		{"main", "refs/heads/refs/heads/main"},
		{"", "refs/heads/"},
	} {
		r := action(t, s.dir, s.api.URL, s.repo, "push", tc.ref, rec.env, "DEFAULT_BRANCH="+tc.branch)
		if got := rec.minTrust(t); r.code != 0 || got != "" || r.outputs["upload"] != "false" {
			t.Errorf("default branch %q, ref %q: exit %d, --min-trust-level %q, upload %q; want no raise\n%s", tc.branch, tc.ref, r.code, got, r.outputs["upload"], r.out)
		}
	}
	if _, err := os.Stat(filepath.Join(s.dir, "pwned")); err == nil {
		t.Error("the default branch name ran a command")
	}
}
