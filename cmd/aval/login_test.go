package main

import (
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLogin replaces the terminal check and the interactive login; calls
// counts the logins.
func fakeLogin(t *testing.T, terminal bool, token string, err error) (calls *int) {
	t.Helper()
	it, l := isTerminal, login
	t.Cleanup(func() { isTerminal, login = it, l })
	calls = new(int)
	isTerminal = func() bool { return terminal }
	login = func() (string, error) { *calls++; return token, err }
	return calls
}

// #130: without --key or $SIGSTORE_ID_TOKEN, a terminal (or --login) signs
// keyless with the token of the interactive login, taken after the test ran.
func TestInteractiveLogin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		flags    []string
	}{
		{"terminal", true, nil},
		{"--login without terminal", false, []string{"--login"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signed := fakePublish(t, nil, nil)
			calls := fakeLogin(t, tc.terminal, "token", nil)
			argv, ran := marker(t, 0)
			c := contractWith(t, "hermetic", argv...)
			result := filepath.Join(t.TempDir(), "r.json")
			t.Setenv(idTokenEnv, "")
			code, stdout, stderr := runCLI(append([]string{"run", "-f", c, "--registry", "reg.example/r", "--push", "--result-file", result}, tc.flags...)...)
			if code != ExitOK || !ran() || *calls != 1 || len(*signed) != 1 || !strings.Contains(stdout, "pushed") {
				t.Fatalf("run: exit %d, ran, %d logins, %d signed, stdout %q, stderr %q", code, *calls, len(*signed), stdout, stderr)
			}
			code, _, stderr = runCLI(append([]string{"push", "-f", c, "--registry", "reg.example/r", "--result-file", result}, tc.flags...)...)
			if code != ExitOK || *calls != 2 || len(*signed) != 2 {
				t.Fatalf("push: exit %d, %d logins, stderr %q", code, *calls, stderr)
			}
		})
	}
}

// $SIGSTORE_ID_TOKEN and --key keep working without a login; --login
// excludes both; a failed login publishes nothing.
func TestInteractiveLoginNotUsed(t *testing.T) {
	fakePublish(t, nil, nil) // sets $SIGSTORE_ID_TOKEN
	calls := fakeLogin(t, true, "", errors.New("closed"))
	c := writeContract(t, "hermetic")
	result := filepath.Join(t.TempDir(), "r.json")
	if code, _, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--push", "--result-file", result); code != ExitOK || *calls != 0 {
		t.Fatalf("token: exit %d, %d logins, stderr %q", code, *calls, stderr)
	}
	for _, cmd := range []string{"run", "push"} {
		t.Setenv(idTokenEnv, "token") // run unsets it before runner.command
		args := []string{cmd, "-f", c, "--registry", "reg.example/r", "--login", "--result-file", result}
		if cmd == "run" {
			args = append(args, "--push")
		}
		if code, _, stderr := runCLI(args...); code != ExitUsage || !strings.Contains(stderr, "--login and $"+idTokenEnv+" exclude each other") || *calls != 0 {
			t.Errorf("%s token --login: exit %d, stderr %q", cmd, code, stderr)
		}
	}
	key, _ := devKey(t, t.TempDir(), "k")
	for _, cmd := range []string{"run", "push"} {
		args := []string{cmd, "-f", c, "--registry", "reg.example/r", "--key", key, "--login", "--result-file", result}
		if cmd == "run" {
			args = append(args, "--push")
		}
		if code, _, stderr := runCLI(args...); code != ExitUsage || !strings.Contains(stderr, "--key and --login exclude each other") {
			t.Errorf("%s --key --login: exit %d, stderr %q", cmd, code, stderr)
		}
	}
	signed := fakePublish(t, nil, nil)
	t.Setenv(idTokenEnv, "")
	if code, _, stderr := runCLI("push", "-f", c, "--registry", "reg.example/r", "--result-file", result); code != ExitError || !strings.Contains(stderr, "interactive Sigstore login: closed") || len(*signed) != 0 {
		t.Fatalf("failed login: exit %d, stderr %q", code, stderr)
	}
}

// run --login without --push would sign nothing: a usage error, no login.
func TestRunLoginNeedsPush(t *testing.T) {
	calls := fakeLogin(t, true, "", nil)
	c := writeContract(t, "hermetic")
	if code, _, stderr := runCLI("run", "-f", c, "--login", "--result-file", filepath.Join(t.TempDir(), "r.json")); code != ExitUsage || !strings.Contains(stderr, "--login needs --push") || *calls != 0 {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

// The production seams: the terminal check answers, and the login uses the
// issuer from $SIGSTORE_OIDC_ISSUER (here one that is gone, so it fails).
func TestLoginSeams(t *testing.T) {
	_ = isTerminal()
	gone := httptest.NewServer(nil)
	gone.Close()
	t.Setenv(oidcIssuerEnv, gone.URL)
	if _, err := login(); err == nil {
		t.Error("login against a closed issuer succeeded")
	}
}
