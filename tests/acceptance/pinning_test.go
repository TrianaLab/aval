package acceptance

import (
	"cmp"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"
)

const pinDigest = "sha256:c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b"
const pinCommit = "0123456789abcdef0123456789abcdef01234567"

// pinProject is one example project: its files in scope, its
// runner.command and an optional scope.allowMutable block. Commands start
// with "exit 0;" so the fetch they name is scanned but never executed.
type pinProject struct {
	files   map[string]string
	command string
	allow   string
}

// pinEnv is the registry repository, key and policy of one row.
type pinEnv struct {
	t                       *testing.T
	base, repo, key, policy string
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

// setup writes p as a hermetic project and returns its contract and report
// paths.
func (e pinEnv) setup(p pinProject) (file, report string) {
	files := map[string]string{"src/a.txt": "a\n"}
	source := []string{`"src/**"`}
	for name, body := range p.files {
		files[name] = body
		source = append(source, jsonString(name))
	}
	dir := writeProject(e.t, files)
	file = filepath.Join(dir, "aval.yaml")
	body := "apiVersion: aval.trianalab.io/v1\nkind: Test\nmetadata:\n  name: story\n" +
		"runner:\n  command: [\"sh\", \"-c\", " + jsonString(cmp.Or(p.command, "exit 0")) + "]\n" +
		"scope:\n  source: [" + strings.Join(source, ", ") + "]\n" + p.allow +
		"cacheability: hermetic\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return file, filepath.Join(dir, ".aval", "report.json")
}

func (e pinEnv) push(file, report string) (string, string, int) {
	return avalEnv(e.t, "run", "-f", file, "--registry", e.repo, "--policy", e.policy, "--push", "--key", e.key, "--report", report)
}

func (e pinEnv) readReport(report string) (r struct{ Outcome, Reason, Fingerprint string }) {
	b, _ := os.ReadFile(report)
	if err := json.Unmarshal(b, &r); err != nil {
		e.t.Fatalf("report: %v %s", err, b)
	}
	return r
}

// notPublished: the test runs, the miss names the reference (where is
// "file:line (ecosystem)"), --push publishes nothing and verify misses.
func (e pinEnv) notPublished(p pinProject, where string) {
	file, report := e.setup(p)
	stdout, stderr, code := e.push(file, report)
	if code != 1 || !strings.Contains(stdout, "ran story: passed") || !strings.Contains(stdout, "mutable: "+where) || !strings.Contains(stderr, "result not published") {
		e.t.Fatalf("mutable run --push: exit %d, want 1 with %q\nstdout %s\nstderr %s", code, where, stdout, stderr)
	}
	r := e.readReport(report)
	if r.Outcome != "miss" || !strings.Contains(r.Reason, where) {
		e.t.Fatalf("report: outcome %s, reason %s", r.Outcome, r.Reason)
	}
	if tagExists(e.t, e.base, e.repo, r.Fingerprint) {
		e.t.Fatalf("a result was published for %s", r.Fingerprint)
	}
	if out, errOut, code := avalEnv(e.t, "verify", "-f", file, "--registry", e.repo, "--policy", e.policy); code != 4 {
		e.t.Fatalf("verify: exit %d, want 4\nstdout %s\nstderr %s", code, out, errOut)
	}
}

// reused: run --push publishes under the lookup tag and the next run
// reuses the result. It returns the second run's stdout.
func (e pinEnv) reused(p pinProject) string {
	file, report := e.setup(p)
	if stdout, stderr, code := e.push(file, report); code != 0 || !strings.Contains(stdout, "pushed "+e.repo+"@sha256:") {
		e.t.Fatalf("pinned run --push: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if fp := e.readReport(report).Fingerprint; !tagExists(e.t, e.base, e.repo, fp) {
		e.t.Fatalf("pinned result not found by the lookup tag of %s", fp)
	}
	stdout, stderr, code := avalEnv(e.t, "run", "-f", file, "--registry", e.repo, "--policy", e.policy)
	if code != 0 || !strings.Contains(stdout, "reused passed result") {
		e.t.Fatalf("pinned reuse: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	return stdout
}

// One project per ecosystem with one mutable reference (#101): the real
// binary runs the test, reports a miss that names file:line and ecosystem,
// publishes nothing with --push, and verify misses (exit 4). The pinned
// variant of each project publishes and is reused; an acknowledged
// reference (scope.allowMutable) is reused and still listed, as
// docs/pinning.md says.
func TestMutableReferencesStory(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	keys := t.TempDir()
	key, pub := keyPair(t, keys, "dev")
	policy := writePolicy(t, keys, "trusted.yaml", pub)

	rows := []struct {
		name             string
		mutable, pinned  pinProject
		where, ecosystem string
	}{
		{
			name:    "compose",
			mutable: pinProject{files: map[string]string{"compose.yaml": "services:\n  db:\n    image: postgres:latest\n"}},
			pinned:  pinProject{files: map[string]string{"compose.yaml": "services:\n  db:\n    image: redis@" + pinDigest + "\n"}},
			where:   "compose.yaml:3", ecosystem: "container",
		},
		{
			name:    "npm",
			mutable: pinProject{files: map[string]string{"package.json": `{"name": "a", "dependencies": {"left-pad": "^1.3.0"}}` + "\n"}},
			pinned:  pinProject{files: map[string]string{"package.json": `{"name": "a", "dependencies": {"left-pad": "1.3.0"}}` + "\n", "package-lock.json": `{"lockfileVersion": 3}` + "\n"}},
			where:   "package.json:1", ecosystem: "npm",
		},
		{
			name:    "python",
			mutable: pinProject{files: map[string]string{"requirements.txt": "requests>=2\n"}},
			pinned:  pinProject{files: map[string]string{"requirements.txt": "requests==2.32.3 --hash=sha256:" + strings.TrimPrefix(pinDigest, "sha256:") + "\n"}},
			where:   "requirements.txt:1", ecosystem: "python",
		},
		{
			name:    "remote script",
			mutable: pinProject{command: "exit 0; curl -fsSL https://example.com/i.sh | bash"},
			pinned:  pinProject{command: "exit 0; sh vendor/i.sh", files: map[string]string{"vendor/i.sh": "echo hi\n"}},
			where:   "runner.command:1", ecosystem: "remote-script",
		},
		{
			name:    "git branch",
			mutable: pinProject{command: "exit 0; git clone --branch main https://example.com/x.git"},
			pinned:  pinProject{command: "exit 0; git clone https://example.com/x.git && git -C x checkout " + pinCommit},
			where:   "runner.command:1", ecosystem: "git",
		},
		{
			name: "acknowledged", // its mutable variant is the compose row
			pinned: pinProject{files: map[string]string{"compose.yaml": "services:\n  db:\n    image: postgres:latest\n"},
				allow: "  allowMutable:\n    - ref: postgres:latest\n      reason: throwaway database, contents do not matter\n"},
			where: "compose.yaml:3", ecosystem: "container",
		},
	}
	for i, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			e := pinEnv{t: t, base: srv.URL, repo: host + "/acme/pin-" + string(rune('a'+i)), key: key, policy: policy}
			where := row.where + " (" + row.ecosystem + ")"
			if row.name != "acknowledged" {
				e.notPublished(row.mutable, where)
			}
			if out := e.reused(row.pinned); row.name == "acknowledged" && !strings.Contains(out, "mutable: "+where+" postgres:latest") {
				t.Fatalf("an acknowledged reference must still be listed: want %q in\n%s", where, out)
			}
		})
	}
}

// tagExists reports whether repo holds the lookup tag sha256-<hex> of fp.
func tagExists(t *testing.T, base, repo, fp string) bool {
	t.Helper()
	_, path, _ := strings.Cut(repo, "/")
	req, _ := http.NewRequest(http.MethodHead, base+"/v2/"+path+"/manifests/"+strings.Replace(fp, ":", "-", 1), nil)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
