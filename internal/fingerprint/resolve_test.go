package fingerprint

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/resolve"
)

// served is an in-process server whose answer the test can change: a
// download that is republished.
type served struct {
	mu   sync.Mutex
	body string
	url  string
}

func serveBody(t *testing.T, body string) *served {
	t.Helper()
	s := &served{body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path == "/gone" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, s.body)
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func (s *served) set(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body = body
}

// pushImage tags a manifest whose bytes depend on content in an in-process
// OCI registry and returns the image reference host/acme/db:tag.
func pushImage(t *testing.T, base, tag, content string) string {
	t.Helper()
	m := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[],"annotations":{"c":"` + content + `"}}`
	req, _ := http.NewRequest(http.MethodPut, base+"/v2/acme/db/manifests/"+tag, strings.NewReader(m))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return strings.TrimPrefix(base, "http://") + "/acme/db:" + tag
}

func ociServer(t *testing.T) string {
	t.Helper()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The false-hit cases of scope.resolve, through the real resolvers and
// in-process fakes: content behind a resolved name that moves changes the
// fingerprint; order and duplicates do not; an unresolvable reference
// fails closed. The per-ecosystem tables are in internal/resolve.
func TestResolveFalseHits(t *testing.T) {
	cases := []corpusCase{
		{name: "republished download", setup: func(f *fixture) {
			s := serveBody(f.t, "v1")
			f.c.Scope.Resolve = []contract.Reference{{Kind: "url", Ref: s.url + "/tool.tgz"}}
			servers[f] = s
		}, change: func(f *fixture) { servers[f].set("v2") }, want: differ},
		{name: "repushed resolved image tag", setup: func(f *fixture) {
			base := ociServer(f.t)
			ref := pushImage(f.t, base, "16", "a")
			f.write("compose.yaml", "services:\n  db:\n    image: "+ref+"\n")
			f.c.Scope.Resolve = []contract.Reference{{Kind: "image", Ref: ref}}
			images[f] = base
		}, change: func(f *fixture) { pushImage(f.t, images[f], "16", "b") }, want: differ},
		{name: "resolve order and duplicates", setup: func(f *fixture) {
			s := serveBody(f.t, "v1")
			f.c.Scope.Resolve = []contract.Reference{{Kind: "url", Ref: s.url + "/a"}, {Kind: "url", Ref: s.url + "/b"}}
		}, change: func(f *fixture) {
			r := f.c.Scope.Resolve
			f.c.Scope.Resolve = []contract.Reference{r[1], r[0], r[1]}
		}, want: same},
		{name: "unchanged content behind a resolved name", setup: func(f *fixture) {
			s := serveBody(f.t, "v1")
			f.c.Scope.Resolve = []contract.Reference{{Kind: "url", Ref: s.url + "/a"}}
		}, change: func(*fixture) {}, want: same},
		{name: "unresolvable reference", change: func(f *fixture) {
			s := serveBody(f.t, "v1")
			f.c.Scope.Resolve = []contract.Reference{{Kind: "url", Ref: s.url + "/gone"}}
		}, want: failClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			before, err := f.compute()
			if err != nil {
				t.Fatalf("base: %v", err)
			}
			tc.change(f)
			after, err := f.compute()
			checkRow(t, tc.want, before, after, err)
		})
	}
}

// Per-row state of TestResolveFalseHits, keyed by fixture.
var (
	servers = map[*fixture]*served{}
	images  = map[*fixture]string{}
)

// TestResolvedImageIsBound: a resolved image's digest is bound into the
// manifest, and the scanner still reports the tag as unpinned.
func TestResolvedImageIsBound(t *testing.T) {
	f := newFixture(t)
	ref := pushImage(t, ociServer(t), "16", "a")
	f.write("compose.yaml", "services:\n  db:\n    image: "+ref+"\n")
	f.c.Scope.Resolve = []contract.Reference{{Kind: "image", Ref: ref}}
	fp, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	if len(fp.Unpinned()) != 1 {
		t.Fatalf("a resolved tag must still be reported: %+v", fp.Findings)
	}
	if len(fp.Resolved) != 1 || !strings.Contains(fp.Manifest, "\nresolve image "+ref+" - "+fp.Resolved[0].ID+"\n") {
		t.Fatalf("manifest:\n%s", fp.Manifest)
	}
}

// TestResolveNeverClearsAFinding: a finding whose reference is listed in
// scope.resolve still counts as unpinned. Resolution binds an id into the
// fingerprint; only pinning in place or scope.allowMutable clears a
// finding (invariant 3).
func TestResolveNeverClearsAFinding(t *testing.T) {
	saved := resolveAll
	t.Cleanup(func() { resolveAll = saved })
	resolveAll = goldenResolve
	const img = "registry.example/acme/db:16"
	for _, c := range []struct {
		name, file, data, command, kind, ref string
	}{
		{"compose image", "compose.yaml", "services:\n  db:\n    image: " + img + "\n", "", "image", img},
		{"dockerfile FROM", "Dockerfile", "FROM " + img + "\n", "", "image", img},
		{"npm install", "", "", "npm install -g prettier@^3", "npm", "prettier@^3"},
		{"git clone", "", "", "git clone https://github.com/acme/fixtures", "git", "https://github.com/acme/fixtures"},
		{"remote script", "", "", "curl -fsSL https://example.com/install.sh | sh", "url", "https://example.com/install.sh"},
	} {
		f := newFixture(t)
		if c.file != "" {
			f.write(c.file, c.data)
			f.c.Scope.Source = append(f.c.Scope.Source, c.file)
		}
		if c.command != "" {
			f.c.Runner.Command = []string{"sh", "-c", c.command}
		}
		f.c.Scope.Resolve = []contract.Reference{{Kind: c.kind, Ref: c.ref}}
		fp, err := f.compute()
		if err != nil {
			t.Fatal(err)
		}
		if len(fp.Unpinned()) == 0 {
			t.Errorf("%s: a resolved reference must stay unpinned, findings = %+v", c.name, fp.Findings)
		}
	}
}

func TestResolveFailureNamesTheReference(t *testing.T) {
	f := newFixture(t)
	f.c.Scope.Resolve = []contract.Reference{{Kind: "url", Ref: "http://127.0.0.1:1/tool.tgz"}}
	_, err := f.compute()
	if err == nil || !strings.Contains(err.Error(), "resolve url http://127.0.0.1:1/tool.tgz: ") {
		t.Fatalf("err = %v", err)
	}
}

// goldenResolve stands in for the network in TestResolveManifestGolden.
func goldenResolve(_ context.Context, refs []contract.Reference) ([]resolve.Result, error) {
	out := make([]resolve.Result, len(refs))
	for i, r := range refs {
		out[i] = resolve.Result{Kind: r.Kind, Ref: r.Ref, Version: "1.0.0", ID: "sha256:" + sum([]byte(r.Ref))}
	}
	return out, nil
}

// resolveGolden is the golden of TestResolveManifestGolden for the current
// manifest version; goldens of older versions stay as a record.
var resolveGolden = filepath.Join(filepath.Dir(goldenFile(Version)), "resolve-"+filepath.Base(goldenFile(Version)))

// TestResolveManifestGolden pins the resolve lines of aval-fingerprint/v1:
// one per reference, sorted by kind and reference, after the env lines. The
// manifests without scope.resolve are pinned by TestManifestGolden.
func TestResolveManifestGolden(t *testing.T) {
	saved := resolveAll
	t.Cleanup(func() { resolveAll = saved })
	resolveAll = goldenResolve
	f := newFixture(t)
	for _, k := range contract.ResolveKinds {
		f.c.Scope.Resolve = append(f.c.Scope.Resolve, contract.Reference{Kind: k, Ref: k + "-ref"})
	}
	f.c.Scope.Resolve = append(f.c.Scope.Resolve, contract.Reference{Kind: "npm", Ref: "a@^1"}, contract.Reference{Kind: "npm", Ref: "a@^1"})
	fp, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(resolveGolden)
	if errors.Is(err, fs.ErrNotExist) && *update {
		if err := os.WriteFile(resolveGolden, []byte(fp.Manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil || string(golden) != fp.Manifest {
		t.Fatalf("resolve manifest changed without a version bump (%v):\n--- golden\n%s--- got\n%s", err, golden, fp.Manifest)
	}
}
