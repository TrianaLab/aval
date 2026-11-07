package acceptance

import (
	"archive/zip"
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"
)

// registries is one in-process server standing in for npm, PyPI, a Go
// module proxy, Maven Central, the crates.io index, RubyGems and a plain
// download host: it serves the body set for each path, 404 otherwise.
type registries struct {
	mu    sync.Mutex
	files map[string]string
	url   string
}

func newRegistries(t *testing.T) *registries {
	t.Helper()
	r := &registries{files: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		body, ok := r.files[req.URL.EscapedPath()]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	r.url = srv.URL
	return r
}

func (r *registries) set(path, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[path] = body
}

func moduleZip(t *testing.T, version string) string {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	fw, _ := w.Create("example.com/m@" + version + "/go.mod")
	_, _ = io.WriteString(fw, "module example.com/m\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// putImage tags an image manifest whose bytes depend on content.
func putImage(t *testing.T, base, tag, content string) {
	t.Helper()
	m := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[],"annotations":{"c":"` + content + `"}}`
	req, _ := http.NewRequest(http.MethodPut, base+"/v2/acme/db/manifests/"+tag, strings.NewReader(m))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

// gitRepo returns a file URL of a repository with a branch main and a
// function that moves main to a new commit.
func gitRepo(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "one")
	return "file://" + dir, func() { git("commit", "-q", "--allow-empty", "-m", "two") }
}

// The scope.resolve story (#102), one row per ecosystem, through the real
// binary against in-process registries and a local git repository: run
// --push runs and publishes; the next run, with nothing moved, reuses the
// verified result; after the registry republishes or moves the reference,
// the fingerprint changes, the lookup misses and the test runs again. An
// image the compose file names by tag stays a mutable reference once
// resolved: scope.allowMutable acknowledges it. A reference that cannot be resolved runs the test, names the
// reference and publishes nothing.
func TestResolveStory(t *testing.T) {
	oci := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(oci.Close)
	host := strings.TrimPrefix(oci.URL, "http://")
	reg := newRegistries(t)
	repoURL, moveMain := gitRepo(t)
	keys := t.TempDir()
	key, pub := keyPair(t, keys, "dev")
	policy := writePolicy(t, keys, "trusted.yaml", pub)
	for k, v := range map[string]string{
		"npm_config_registry": reg.url, "AVAL_PYPI_URL": reg.url, "GOPROXY": reg.url, "AVAL_MAVEN_URL": reg.url,
		"AVAL_CRATES_INDEX": reg.url, "AVAL_RUBYGEMS_URL": reg.url, "NETRC": os.DevNull, "NPM_CONFIG_USERCONFIG": os.DevNull,
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	} {
		t.Setenv(k, v)
	}

	putImage(t, oci.URL, "16", "a")
	npmDoc := func(v string) string {
		return `{"dist-tags":{"latest":"` + v + `"},"versions":{"1.0.0":{"dist":{"integrity":"sha512-a"}},"1.1.0":{"dist":{"integrity":"sha512-b"}}}}`
	}
	reg.set("/left", npmDoc("1.0.0"))
	reg.set("/pypi/requests/json", `{"releases":{"2.32.3":[{"filename":"r.tar.gz","digests":{"sha256":"aa"}}]}}`)
	reg.set("/example.com/m/@v/list", "v1.0.0\n")
	reg.set("/example.com/m/@latest", `{"Version":"v1.0.0"}`)
	reg.set("/example.com/m/@v/v1.0.0.zip", moduleZip(t, "v1.0.0"))
	reg.set("/example.com/m/@v/v1.1.0.zip", moduleZip(t, "v1.1.0"))
	reg.set("/org/junit/junit-bom/maven-metadata.xml", `<metadata><versioning><versions><version>5.9.0</version></versions></versioning></metadata>`)
	reg.set("/org/junit/junit-bom/5.9.0/junit-bom-5.9.0.pom", "<project/>")
	reg.set("/org/junit/junit-bom/5.9.0/junit-bom-5.9.0.jar.sha1", strings.Repeat("a", 40))
	reg.set("/se/rd/serde", `{"vers":"1.0.0","cksum":"c1"}`+"\n")
	reg.set("/api/v1/versions/rails.json", `[{"number":"7.1.0","sha":"g1","platform":"ruby"}]`)
	reg.set("/tool.tgz", "v1")

	rows := []struct {
		name, kind, ref, compose string
		move                     func()
	}{
		{name: "oci", kind: "image", ref: host + "/acme/db:16", compose: host + "/acme/db:16", move: func() { putImage(t, oci.URL, "16", "b") }},
		{name: "npm", kind: "npm", ref: "left@latest", move: func() { reg.set("/left", npmDoc("1.1.0")) }},
		{name: "pypi", kind: "pypi", ref: "requests==2.32.*", move: func() {
			reg.set("/pypi/requests/json", `{"releases":{"2.32.3":[{"filename":"r.tar.gz","digests":{"sha256":"aa"}}],"2.32.4":[{"filename":"r4.tar.gz","digests":{"sha256":"bb"}}]}}`)
		}},
		{name: "go proxy", kind: "go", ref: "example.com/m@latest", move: func() { reg.set("/example.com/m/@v/list", "v1.0.0\nv1.1.0\n") }},
		{name: "git", kind: "git", ref: repoURL + "@main", move: moveMain},
		// A range binds each listed release's content: a redeployed 5.9.0 moves it.
		{name: "maven range", kind: "maven", ref: "org.junit:junit-bom:5.+", move: func() { reg.set("/org/junit/junit-bom/5.9.0/junit-bom-5.9.0.jar.sha1", strings.Repeat("c", 40)) }},
		{name: "maven exact", kind: "maven", ref: "org.junit:junit-bom:5.9.0", move: func() { reg.set("/org/junit/junit-bom/5.9.0/junit-bom-5.9.0.jar.sha1", strings.Repeat("b", 40)) }},
		{name: "crate", kind: "crate", ref: "serde@1", move: func() { reg.set("/se/rd/serde", `{"vers":"1.0.0","cksum":"c2"}`+"\n") }},
		{name: "gem", kind: "gem", ref: "rails@~>7.0", move: func() { reg.set("/api/v1/versions/rails.json", `[{"number":"7.1.0","sha":"g2","platform":"ruby"}]`) }},
		{name: "url", kind: "url", ref: reg.url + "/tool.tgz", move: func() { reg.set("/tool.tgz", "v2") }},
	}
	for i, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			e := pinEnv{t: t, base: oci.URL, repo: host + "/acme/resolve-" + string(rune('a'+i)), key: key, policy: policy}
			p := pinProject{allow: "  resolve:\n    - " + row.kind + ": " + jsonString(row.ref) + "\n"}
			if row.compose != "" {
				p.files = map[string]string{"compose.yaml": "services:\n  db:\n    image: " + row.compose + "\n"}
				p.allow += "  allowMutable:\n    - ref: " + row.compose + "\n      reason: pulled before the test\n"
			}
			e.reusedUntilMoved(p, row.compose != "", row.move)
		})
	}
	t.Run("unresolvable", func(t *testing.T) {
		pinEnv{t: t, base: oci.URL, repo: host + "/acme/resolve-x", key: key, policy: policy}.unresolvable()
	})
	t.Run("explain reports the cost", func(t *testing.T) {
		file, _ := pinEnv{t: t}.setup(pinProject{allow: "  resolve:\n    - url: " + reg.url + "/tool.tgz\n"})
		stdout, stderr, code := avalEnv(t, "run", "-f", file, "--explain", "--fingerprint-only")
		if code != 0 || !strings.Contains(stdout, "\nresolve url "+reg.url+"/tool.tgz - sha256:") || !strings.Contains(stderr, "resolved 1 reference(s) in ") {
			t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
		}
	})
}

// reusedUntilMoved: run --push publishes, the next run reuses (listing the
// image as acknowledged by scope.allowMutable when compose is set, since
// scope.resolve never clears a finding), and after move the run
// misses on a new fingerprint and runs the test.
func (e pinEnv) reusedUntilMoved(p pinProject, compose bool, move func()) {
	t := e.t
	file, report := e.setup(p)
	if stdout, stderr, code := e.push(file, report); code != 0 || !strings.Contains(stdout, "pushed "+e.repo+"@sha256:") {
		t.Fatalf("run --push: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	out, stderr, code := avalEnv(t, "run", "-f", file, "--registry", e.repo, "--policy", e.policy)
	if code != 0 || !strings.Contains(out, "reused passed result") {
		t.Fatalf("nothing moved, want reuse: exit %d\nstdout %s\nstderr %s", code, out, stderr)
	}
	if compose && !strings.Contains(out, "(allowed: pulled before the test)") {
		t.Fatalf("a resolved image must stay listed, acknowledged by scope.allowMutable:\n%s", out)
	}
	before := e.readReport(report).Fingerprint
	move()
	stdout, stderr, code := e.push(file, report)
	if code != 0 || !strings.Contains(stdout, "ran story: passed") || strings.Contains(stdout, "reused") {
		t.Fatalf("after the reference moved: exit %d, want a run\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if r := e.readReport(report); r.Outcome != "miss" || r.Fingerprint == before {
		t.Fatalf("after the reference moved: report %+v, want a miss on a new fingerprint", r)
	}
}

// unresolvable: an exact version that is not published fails the fingerprint, so
// the test runs, nothing is published and the reason names the reference.
func (e pinEnv) unresolvable() {
	t := e.t
	file, report := e.setup(pinProject{allow: "  resolve:\n    - npm: left@9.9.9\n"})
	stdout, stderr, code := e.push(file, report)
	if code != 1 || !strings.Contains(stdout, "ran story: passed") || !strings.Contains(stderr, "result not published: fingerprint: resolve npm left@9.9.9: version 9.9.9 is not published") {
		t.Fatalf("unresolvable: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if r := e.readReport(report); r.Outcome != "error" || !strings.Contains(r.Reason, "resolve npm left@9.9.9") {
		t.Fatalf("report %+v", r)
	}
	if out, _, code := avalEnv(t, "run", "-f", file, "--explain", "--fingerprint-only"); code != 1 || out != "" {
		t.Fatalf("--explain of an unresolvable contract: exit %d, stdout %q", code, out)
	}
}
