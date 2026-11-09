package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ggcr "github.com/google/go-containerregistry/pkg/registry"

	"github.com/TrianaLab/aval/internal/demo"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// The docs site's "Try it" terminal (internal/demo, compiled to WebAssembly)
// says what the real CLI says. The same story runs through the demo and
// through the aval binary over the demo's files, with the demo's key, the
// demo's policy and an in-process registry: every line matches except the
// registry digests, the test's own output and the signature error, which
// comes from Sigstore in the CLI and from the stdlib verifier in the demo.
// The fingerprints match exactly: fingerprint.ComputeFS over the in-memory
// project equals fingerprint.Compute on disk.
func TestBrowserDemoMatchesCLI(t *testing.T) {
	// tamper swaps in a fresh registry, so the handler is behind a pointer.
	var reg atomic.Pointer[http.Handler]
	fresh := func() { h := ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))); reg.Store(&h) }
	fresh()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*reg.Load()).ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/greet-aval"
	d := demo.New()
	d.Repo = repo
	dir := t.TempDir()
	sync := func() {
		for name, body := range d.Files() {
			if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	sync()
	if err := os.WriteFile(filepath.Join(dir, "throwaway.key"), d.KeyPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	cli := func(line string) []string {
		args := strings.Fields(strings.TrimPrefix(line, "aval "))
		if args[0] == "explain" {
			return args
		}
		args = append(args, "--registry", repo, "--policy", "policy.yaml")
		if args[0] == "run" {
			args = append(args, "--push", "--key", "throwaway.key")
		}
		return args
	}
	for _, line := range []string{
		"aval run", "aval run", "aval verify", "aval verify -o json", "aval run --explain", "aval explain", "aval explain --digest",
		"edit README.md", "aval run", "edit aval.yaml", "aval verify", "edit greet.go", "aval verify", "aval run",
		"tamper", "aval verify", "aval verify -o json", "aval run", "aval verify",
	} {
		var dOut, dErr bytes.Buffer
		dCode := d.Exec(line, &dOut, &dErr)
		if !strings.HasPrefix(line, "aval ") {
			if dCode != 0 {
				t.Fatalf("%s: exit %d: %s", line, dCode, dErr.String())
			}
			if line == "tamper" {
				tamper(t, repo, dir, fresh)
			}
			sync()
			continue
		}
		out, errOut, code := avalIn(t, dir, cli(line)...)
		if code != dCode {
			t.Fatalf("%s: demo exit %d, CLI exit %d\nCLI stdout:\n%s\nCLI stderr:\n%s", line, dCode, code, out, errOut)
		}
		if strings.HasSuffix(line, "-o json") {
			if a, b := comparableReport(t, dOut.String()), comparableReport(t, out); a != b {
				t.Errorf("%s: demo report\n%s\nCLI report\n%s", line, a, b)
			}
		} else if a, b := normalize(dOut.String()), normalize(out); a != b {
			t.Errorf("%s: stdout differs\n--- demo\n%s--- CLI\n%s", line, a, b)
		}
		if a, b := normalize(dErr.String()), normalize(errOut); a != b {
			t.Errorf("%s: stderr differs\n--- demo\n%s--- CLI\n%s", line, a, b)
		}
	}
}

// avalIn runs the binary in dir.
func avalIn(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := exec.Command(aval, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &out, &errOut
	cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+t.TempDir())
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out.String(), errOut.String(), 0
	case errors.As(err, &exit):
		return out.String(), errOut.String(), exit.ExitCode()
	}
	t.Fatal(err)
	return "", "", 0
}

var (
	refDigest = regexp.MustCompile(`@sha256:[0-9a-f]{64}`)
	goTest    = regexp.MustCompile(`(?m)^ok  \texample\.com/greet\t.*$`)
	sigReason = regexp.MustCompile(`signature: .*? \(trust policy`)
)

func normalize(s string) string {
	s = refDigest.ReplaceAllString(s, "@sha256:<digest>")
	s = goTest.ReplaceAllString(s, "ok  example.com/greet")
	return sigReason.ReplaceAllString(s, "signature: <reason> (trust policy")
}

// comparableReport decodes a JSON verify report and blanks what differs
// between a browser and a machine: digests, times, host and platform.
func comparableReport(t *testing.T, raw string) string {
	t.Helper()
	var r evidence.Report
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	if p := r.Predicate; p != nil {
		p.StartedAt, p.FinishedAt, p.Runner.Identity = time.Time{}, time.Time{}, ""
		delete(p.Execution.Environment, "os")
		delete(p.Execution.Environment, "arch")
	}
	out, _ := json.MarshalIndent(r, "", "  ")
	return normalize(string(out))
}

// tamper does to the registry what the demo's tamper does: the
// attestations for the current fingerprint are replaced by copies whose
// signed result says failed, with the original signature. fresh empties the
// registry, which then holds only the copies.
func tamper(t *testing.T, repo, dir string, fresh func()) {
	t.Helper()
	fp, _, code := avalIn(t, dir, "run", "--fingerprint-only")
	fp = strings.TrimSpace(fp)
	if code != 0 {
		t.Fatalf("fingerprint: exit %d", code)
	}
	ctx := context.Background()
	cands, err := registry.Lookup(ctx, repo, fp)
	if err != nil {
		t.Fatal(err)
	}
	var copies []registry.Attestation
	for _, c := range cands {
		a, err := registry.Fetch(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		env := *a.Envelope
		env.Payload = bytes.Replace(env.Payload, []byte(`"result":"passed"`), []byte(`"result":"failed"`), 1)
		copies = append(copies, registry.Attestation{Envelope: &env, Bundle: a.Bundle})
	}
	fresh()
	for _, a := range copies {
		if _, err := registry.Push(ctx, repo, fp, a); err != nil {
			t.Fatal(err)
		}
	}
}
