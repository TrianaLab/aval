package acceptance

import (
	"bytes"
	"cmp"
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

// credStory is a run action story against a registry that needs basic auth
// bot:s3cr3t-pw (#250). AVAL is a wrapper that records the AVAL_DOCKER_CONFIG
// and DOCKER_CONFIG aval gets and the former's config.json, then runs aval,
// or stops when WRAP_ONLY is set (registries the test must not dial). With
// WRAP_OLD, its run --help is empty, as from a release without
// AVAL_DOCKER_CONFIG. The
// test command records its environment and tries to read the credential.
type credStory struct {
	*actionStory
	job, log string
}

const credUser, credPassword = "bot", "s3cr3t-pw"

func newCredStory(t *testing.T) *credStory {
	t.Helper()
	reg := ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != credUser || p != credPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="aval"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	s := &credStory{actionStory: &actionStory{repo: strings.TrimPrefix(srv.URL, "http://") + "/acme/app-aval", contents: &fakeContents{}}}
	s.api = httptest.NewServer(s.contents)
	t.Cleanup(s.api.Close)
	keys := t.TempDir()
	key, pub := keyPair(t, keys, "ci")
	s.ciKey = key
	s.mainPolicy = "apiVersion: aval.trianalab.io/v1\nkind: Policy\nregistry: " + s.repo + "\nminTrustLevel: runner\nidentities:\n" +
		"  - publicKey: " + pub + "\n    trustLevel: runner\n"
	runner := strings.Replace(contract, `["true"]`, `["sh", "-c", "echo ran >> ../runs.log; env > ../env.log; cat \"$AVAL_DOCKER_CONFIG/config.json\" \"$DOCKER_CONFIG/config.json\" > ../creds.log 2>&1 || true"]`, 1)
	s.root = writeProject(t, map[string]string{"app/aval.yaml": runner, "app/src/a.txt": "a\n"})
	s.dir = filepath.Join(s.root, "app")
	s.contents.set(s.mainPolicy)

	// The job's own login, for another host: tests keep it.
	s.job = t.TempDir()
	if err := os.WriteFile(filepath.Join(s.job, "config.json"), []byte(`{"auths":{"other.example":{"auth":"b3RoZXI6eA=="}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.log = filepath.Join(keys, "aval")
	wrapper := fmt.Sprintf(`#!/bin/bash
[ "$*" != "run --help" ] || { [ -z "${WRAP_OLD:-}" ] || exit 0; exec %[2]q "$@"; }
printf '%%s' "${AVAL_DOCKER_CONFIG-unset}" > %[1]q.dir
printf '%%s' "$DOCKER_CONFIG" > %[1]q.docker
/bin/cp "$AVAL_DOCKER_CONFIG/config.json" %[1]q.json 2> /dev/null || echo none > %[1]q.json
[ -z "${WRAP_ONLY:-}" ] || exit 0
exec %[2]q "$@"
`, s.log, aval)
	if err := os.WriteFile(filepath.Join(keys, "wrapper"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}

	// Publish with credentials, outside the action.
	creds := t.TempDir()
	auth := base64.StdEncoding.EncodeToString([]byte(credUser + ":" + credPassword))
	host := strings.SplitN(s.repo, "/", 2)[0]
	if err := os.WriteFile(filepath.Join(creds, "config.json"), []byte(`{"auths":{"`+host+`":{"auth":"`+auth+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(aval, "run", "-f", "aval.yaml", "--test", "story", "--registry", s.repo, "--push", "--key", key, "--no-reuse")
	cmd.Dir, cmd.Env = s.dir, append(os.Environ(), "DOCKER_CONFIG="+creds)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("publish: %v\n%s", err, out)
	}
	return s
}

// run runs the action as on a pull request, with the job's DOCKER_CONFIG,
// the wrapper as AVAL and a job token, and returns the AVAL_DOCKER_CONFIG
// aval got ("unset" when it had none) and the auths of its config.json (nil
// when it had none). aval's DOCKER_CONFIG is always the job's.
func (s *credStory) run(t *testing.T, env ...string) (actionRun, string, map[string]map[string]string) {
	t.Helper()
	_ = os.Remove(s.log + ".dir")
	env = append([]string{"DOCKER_CONFIG=" + s.job, "AVAL=" + filepath.Join(filepath.Dir(s.log), "wrapper"), "GH_TOKEN=tok"}, env...)
	r := action(t, s.dir, s.api.URL, s.repo, "pull_request", "refs/pull/1/merge", env...)
	dir, err := os.ReadFile(s.log + ".dir")
	if err != nil {
		t.Fatalf("aval did not run: %v\n%s", err, r.out)
	}
	if docker, _ := os.ReadFile(s.log + ".docker"); string(docker) != s.job {
		t.Fatalf("aval ran with DOCKER_CONFIG %s, want the job's %s", docker, s.job)
	}
	var cfg struct {
		Auths map[string]map[string]string `json:"auths"`
	}
	b, _ := os.ReadFile(s.log + ".json")
	_ = json.Unmarshal(b, &cfg)
	auth := base64.StdEncoding.EncodeToString([]byte(credUser + ":" + credPassword))
	for _, secret := range []string{credPassword, auth} {
		if strings.Contains(r.out, secret) || strings.Contains(r.summary, secret) {
			t.Fatalf("the log or the step summary holds the registry password or its base64\n%s\n%s", r.out, r.summary)
		}
	}
	return r, string(dir), cfg.Auths
}

// With registry-username and registry-password, aval runs with an
// AVAL_DOCKER_CONFIG of the step that adds an entry for the policy registry's
// host only to the job's auths, and reads the private registry: verified.
// The directory is gone after the step. Without the inputs there is no AVAL_DOCKER_CONFIG and the
// registry refuses the read: the test runs, an error outcome, never
// verification-failed (invariant 4). The test never sees GH_TOKEN. The story
// already ran the test once to publish it.
func TestGitHubRunActionRegistryCredentials(t *testing.T) {
	s := newCredStory(t)
	host := strings.SplitN(s.repo, "/", 2)[0]
	auth := base64.StdEncoding.EncodeToString([]byte(credUser + ":" + credPassword))
	inputs := []string{"REGISTRY_USERNAME=" + credUser, "REGISTRY_PASSWORD=" + credPassword}

	r, dir, auths := s.run(t, inputs...)
	s.want(t, "with credentials", r, 0, 1, "verified", "registry credentials: for "+host+" only")
	if dir == s.job || len(auths) != 2 || auths[host]["auth"] != auth || auths["other.example"]["auth"] != "b3RoZXI6eA==" {
		t.Fatalf("with credentials: DOCKER_CONFIG %s, auths %v; want a step directory with the job's auths plus %s", dir, auths, host)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the step's AVAL_DOCKER_CONFIG %s is still there after the step: %v", dir, err)
	}

	r, dir, auths = s.run(t)
	s.want(t, "anonymous", r, 0, 2, "error")
	if dir != "unset" || auths != nil {
		t.Fatalf("anonymous: AVAL_DOCKER_CONFIG %s, auths %v; want none", dir, auths)
	}
	env, err := os.ReadFile(filepath.Join(s.root, "env.log"))
	if err != nil || strings.Contains(string(env), "GH_TOKEN=") || strings.Contains(string(env), "REGISTRY_PASSWORD=") {
		t.Fatalf("runner.command sees GH_TOKEN or the registry password (%v)", err)
	}

	// Only one of the inputs: anonymous, with a warning.
	r, dir, _ = s.run(t, "REGISTRY_USERNAME="+credUser)
	if dir != "unset" || !strings.Contains(r.out, "set both registry-username and registry-password") {
		t.Fatalf("username only: AVAL_DOCKER_CONFIG %s\n%s", dir, r.out)
	}

	// An aval without AVAL_DOCKER_CONFIG gets no credentials, with a warning.
	r, dir, _ = s.run(t, append(inputs, "WRAP_OLD=1")...)
	if dir != "unset" || !strings.Contains(r.out, "the installed aval has no AVAL_DOCKER_CONFIG") {
		t.Fatalf("old aval: AVAL_DOCKER_CONFIG %s\n%s", dir, r.out)
	}
}

// The test, run beside the verified result (shadow), keeps the job's
// DOCKER_CONFIG, gets no AVAL_DOCKER_CONFIG and cannot read the credential
// aval used (#250).
func TestGitHubRunActionTestCannotReadCredentials(t *testing.T) {
	s := newCredStory(t)
	auth := base64.StdEncoding.EncodeToString([]byte(credUser + ":" + credPassword))
	r, _, _ := s.run(t, "REGISTRY_USERNAME="+credUser, "REGISTRY_PASSWORD="+credPassword, "INPUT_SHADOW=true")
	s.want(t, "shadow with credentials", r, 0, 2, "verified")
	env, err := os.ReadFile(filepath.Join(s.root, "env.log"))
	creds, _ := os.ReadFile(filepath.Join(s.root, "creds.log"))
	if err != nil || strings.Contains(string(env), "AVAL_DOCKER_CONFIG=") || !strings.Contains(string(env), "DOCKER_CONFIG="+s.job+"\n") ||
		strings.Contains(string(creds), auth) || strings.Contains(string(creds), credPassword) || !strings.Contains(string(creds), "other.example") {
		t.Fatalf("the test sees AVAL_DOCKER_CONFIG, lost the job's DOCKER_CONFIG or read the credential (%v)\n%s", err, creds)
	}
}

// A policy with no valid registry adds no credentials, and a valid one
// gets them for its own host only, whatever the path or port looks like.
func TestGitHubRunActionRegistryCredentialsHostOnly(t *testing.T) {
	s := newCredStory(t)
	inputs := []string{"REGISTRY_USERNAME=" + credUser, "REGISTRY_PASSWORD=" + credPassword, "WRAP_ONLY=1"}
	policy := func(registry string) string {
		return strings.Replace(s.mainPolicy, "registry: "+s.repo+"\n", registry, 1)
	}
	for name, tc := range map[string]struct {
		policy, host string // host "" means no credentials
	}{
		"no policy":          {"", ""},
		"no registry field":  {policy(""), ""},
		"empty registry":     {policy("registry:\n"), ""},
		"scheme":             {policy("registry: https://ghcr.io/x\n"), ""},
		"userinfo":           {policy("registry: ghcr.io@evil.example/x\n"), ""},
		"no path":            {policy("registry: ghcr.io\n"), ""},
		"space":              {policy("registry: \"ghcr.io /x\"\n"), ""},
		"nested only":        {policy("auth:\n  registry: ghcr.io/x\n"), ""},
		"path trick":         {policy("registry: evil.example/ghcr.io/x\n"), "evil.example"},
		"suffix trick":       {policy("registry: ghcr.io.evil.example/x\n"), "ghcr.io.evil.example"},
		"port":               {policy("registry: 'ghcr.io:8443/x'\n"), "ghcr.io:8443"},
		"comment after path": {policy("registry: ghcr.io/x # evil.example\n"), "ghcr.io"},
	} {
		s.contents.set(tc.policy)
		r, dir, auths := s.run(t, inputs...)
		if tc.host == "" {
			if dir != "unset" || !strings.Contains(r.out, "::warning title=aval run::") {
				t.Errorf("%s: AVAL_DOCKER_CONFIG %s, auths %v; want none and a warning\n%s", name, dir, auths, r.out)
			}
			continue
		}
		if _, ok := auths[tc.host]; dir == "unset" || len(auths) != 2 || !ok {
			t.Errorf("%s: auths %v; want the job's plus %s only", name, auths, tc.host)
		}
	}
}

// lookup-only: true verifies without running, uploading or writing
// anything, under the default branch's policy (#252); runs() counts only
// the publishes. A miss succeeds the step; exit 5 fails it with the SECURITY
// annotation, never mapped to miss.
func TestGitHubRunActionLookupOnly(t *testing.T) {
	s := newActionStory(t)
	s.publish(t, s.ciKey)
	if err := os.RemoveAll(filepath.Join(s.dir, ".aval")); err != nil {
		t.Fatal(err)
	}
	lookup := "INPUT_LOOKUP_ONLY=true"
	s.want(t, "verified", s.pr(t, lookup), 0, 1, "verified", "trust policy: .github/aval/policy.yaml from the default branch (main)")
	r := action(t, s.dir, s.api.URL, s.repo, "push", "refs/heads/main", lookup)
	s.want(t, "verified on main", r, 0, 1, "verified")
	if r.outputs["upload"] != "false" || !strings.Contains(r.summary, "### aval lookup story: `verified`") {
		t.Fatalf("lookup on main: upload %q, summary %q", r.outputs["upload"], r.summary)
	}
	if _, err := os.Stat(filepath.Join(s.dir, ".aval")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup wrote .aval: %v", err)
	}

	s.want(t, "below AVAL_MIN_TRUST_LEVEL", s.pr(t, lookup, "AVAL_MIN_TRUST_LEVEL=isolated"), 0, 1, "miss")
	s.contents.set("")
	s.want(t, "no policy", s.pr(t, lookup), 0, 1, "miss", "::warning title=aval run::no trust policy")
	s.contents.set(s.mainPolicy)

	// The checkout's policy would trust this developer result: ignored.
	s.write(t, "b\n")
	s.want(t, "no attestation", s.pr(t, lookup), 0, 1, "miss")
	s.publish(t, s.devKey)
	s.want(t, "policy edited in the PR", s.pr(t, lookup), 0, 2, "miss")

	s.write(t, "a\n")
	tamper(t, s.repo, s.dir, func() {})
	r = s.pr(t, lookup)
	s.want(t, "tampered attestation", r, 5, 2, "verification-failed", "::error title=aval SECURITY::story: ")

	for name, env := range map[string][]string{
		"bad value":   {"INPUT_LOOKUP_ONLY=yes"},
		"with shadow": {lookup, "INPUT_SHADOW=true"},
	} {
		if r := s.pr(t, env...); r.code != 2 || s.runs() != 2 || !strings.Contains(r.out, "::error title=aval run::lookup-only: ") {
			t.Fatalf("%s: exit %d, %d runs\n%s", name, r.code, s.runs(), r.out)
		}
	}
}
