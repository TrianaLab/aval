package resolve

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"

	"github.com/TrianaLab/aval/internal/contract"
)

func TestMain(m *testing.M) {
	// Never read the developer's credentials or registry settings.
	dir, err := os.MkdirTemp("", "aval-resolve")
	if err != nil {
		panic(err)
	}
	for k, v := range map[string]string{
		"DOCKER_CONFIG": dir, "NETRC": filepath.Join(dir, "netrc"), "NPM_CONFIG_USERCONFIG": filepath.Join(dir, "npmrc"),
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	} {
		_ = os.Setenv(k, v)
	}
	for _, k := range []string{"npm_config_registry", "NPM_CONFIG_REGISTRY", "AVAL_PYPI_URL", "GOPROXY", "AVAL_MAVEN_URL", "AVAL_CRATES_INDEX", "AVAL_RUBYGEMS_URL",
		"PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL", "PIP_FIND_LINKS", "PIP_NO_INDEX", "GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GOSUMDB", "GOINSECURE", "GOFLAGS"} {
		_ = os.Unsetenv(k)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// fake is an in-process registry for the HTTP ecosystems: it serves the
// body set for each path, 404 otherwise, and records the Authorization
// header of the last request.
type fake struct {
	mu    sync.Mutex
	files map[string]string
	auth  string
	url   string
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{files: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = r.Header.Get("Authorization")
		body, ok := f.files[r.URL.EscapedPath()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func (f *fake) set(path, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = body
}

func (f *fake) lastAuth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth
}

func resolveOne(t *testing.T, kind, ref string) (Result, error) {
	t.Helper()
	rs, err := All(context.Background(), []contract.Reference{{Kind: kind, Ref: ref}})
	if err != nil {
		return Result{}, err
	}
	return rs[0], nil
}

func mustResolve(t *testing.T, kind, ref string) Result {
	t.Helper()
	r, err := resolveOne(t, kind, ref)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// mustFail asserts the resolution fails and the reason names the reference.
func mustFail(t *testing.T, kind, ref, why string) {
	t.Helper()
	_, err := resolveOne(t, kind, ref)
	if err == nil || !strings.Contains(err.Error(), "resolve "+kind+" "+ref+": ") || !strings.Contains(err.Error(), why) {
		t.Fatalf("resolve %s %s: err %v, want one naming the reference and %q", kind, ref, err, why)
	}
}

func TestResolversCoverEveryKind(t *testing.T) {
	keys := slices.Sorted(maps.Keys(Resolvers))
	want := slices.Sorted(slices.Values(contract.ResolveKinds))
	if !slices.Equal(keys, want) {
		t.Fatalf("Resolvers %v, contract.ResolveKinds %v", keys, want)
	}
}

func TestAllRejectsIDsThatAreNotOneToken(t *testing.T) {
	f := newFake(t)
	t.Setenv("npm_config_registry", f.url)
	f.set("/a", `{"dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"dist":{"integrity":"sha512-x\nresolve forged"}}}}`)
	mustFail(t, "npm", "a@1.0.0", "not one token")
	if token("") {
		t.Fatal("an empty id is not a token")
	}
}

func TestAllReportsTimeTaken(t *testing.T) {
	f := newFake(t)
	f.set("/f", "x")
	rs, err := All(context.Background(), []contract.Reference{{Kind: "url", Ref: f.url + "/f"}, {Kind: "url", Ref: f.url + "/f"}})
	if err != nil || len(rs) != 2 || rs[0].Took <= 0 || rs[0].Kind != "url" || rs[0].Version != "-" {
		t.Fatalf("All = %+v, %v", rs, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := All(ctx, []contract.Reference{{Kind: "url", Ref: f.url + "/f"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context must stop resolution: %v", err)
	}
}

// checkFloating: each ref names no exact version, so it resolves to the
// hash of the whole release list. The same list in another order keeps the
// id; every document in moved (a new release anywhere, a yank, a moved tag,
// a republished checksum) changes it, whatever the range would pick.
func checkFloating(t *testing.T, kind string, refs []string, set func(string), base, reordered string, moved map[string]string) {
	t.Helper()
	set(base)
	ids := map[string]string{}
	for _, ref := range refs {
		r := mustResolve(t, kind, ref)
		if r.Version != floating || !strings.HasPrefix(r.ID, "sha256:") {
			t.Fatalf("%s %s = %s %s, want floating and a list hash", kind, ref, r.Version, r.ID)
		}
		ids[ref] = r.ID
	}
	set(reordered)
	for _, ref := range refs {
		if r := mustResolve(t, kind, ref); r.ID != ids[ref] {
			t.Errorf("%s %s: the same list in another order must keep the id", kind, ref)
		}
	}
	for why, doc := range moved {
		set(doc)
		for _, ref := range refs {
			if r := mustResolve(t, kind, ref); r.ID == ids[ref] {
				t.Errorf("%s %s: %s must change the id", kind, ref, why)
			}
		}
	}
	set(base)
}

func TestNPM(t *testing.T) {
	f := newFake(t)
	t.Setenv("NPM_CONFIG_REGISTRY", f.url+"/")
	const (
		v100 = `"1.0.0":{"dist":{"integrity":"sha512-a"}}`
		v110 = `"1.1.0":{"dist":{"integrity":"sha512-b"}}`
		v090 = `"0.9.0":{"dist":{"shasum":"abc"}}`
	)
	doc := func(tags string, versions ...string) string {
		return `{"dist-tags":{` + tags + `},"versions":{` + strings.Join(versions, ",") + `}}`
	}
	tags := `"latest":"1.1.0","next":"2.0.0-rc.1"`
	base := doc(tags, v100, v110, v090)
	set := func(d string) { f.set("/left", d); f.set("/@acme%2fleft", d) }
	// "<=1.2" is <1.3.0 to npm and cargo: 1.2.5 is a match a picker can miss.
	checkFloating(t, "npm", []string{"left", "left@", "left@latest", "left@next", "left@^1", "left@<=1.2", "left@1.x", "@acme/left@~1.0", "left@=1.1.0"}, set,
		base, doc(`"next":"2.0.0-rc.1","latest":"1.1.0"`, v090, v110, v100), map[string]string{
			"a new release":          doc(tags, v100, v110, v090, `"1.2.5":{"dist":{"integrity":"sha512-c"}}`),
			"an unpublished version": doc(tags, v110, v090),
			"a moved latest":         doc(`"latest":"1.0.0","next":"2.0.0-rc.1"`, v100, v110, v090),
			"a deprecation":          doc(tags, v100, `"1.1.0":{"dist":{"integrity":"sha512-b"},"deprecated":"broken"}`, v090),
			"a republished checksum": doc(tags, v100, `"1.1.0":{"dist":{"integrity":"sha512-x"}}`, v090),
		})
	for ref, want := range map[string]string{"left@1.1.0": "1.1.0 sha512-b", "left@0.9.0": "0.9.0 abc", "@acme/left@1.0.0": "1.0.0 sha512-a"} {
		if r := mustResolve(t, "npm", ref); r.Version+" "+r.ID != want {
			t.Errorf("npm %s = %s %s, want %s", ref, r.Version, r.ID, want)
		}
	}
	set(doc(`"latest":"1.2.0"`, v100, v110, v090, `"1.2.0":{"dist":{"integrity":"sha512-c"}}`))
	if r := mustResolve(t, "npm", "left@1.1.0"); r.ID != "sha512-b" {
		t.Fatalf("an exact version must keep its id when other versions are published, got %s", r.ID)
	}
	mirror := newFake(t)
	mirror.set("/left", base)
	t.Setenv("NPM_CONFIG_REGISTRY", mirror.url)
	if r := mustResolve(t, "npm", "left@1.1.0"); r.ID != "sha512-b" {
		t.Fatalf("the same version on a mirror must keep its id, got %s", r.ID)
	}
	f.set("/nosum", `{"versions":{"1.0.0":{"dist":{}}}}`)
	f.set("/empty", `{"versions":{}}`)
	f.set("/bad", `{`)
	t.Setenv("NPM_CONFIG_REGISTRY", f.url)
	mustFail(t, "npm", "left@9.9.9", "version 9.9.9 is not published")
	mustFail(t, "npm", "nosum@1.0.0", "no checksum")
	mustFail(t, "npm", "nosum@^1", "no checksum")
	mustFail(t, "npm", "empty", "lists no version")
	mustFail(t, "npm", "bad", "unexpected end of JSON")
	mustFail(t, "npm", "missing", "404")
	// npm installs these from somewhere else than the registry document of
	// lib: binding lib's release list would bind the wrong thing.
	for _, ref := range []string{"lib@npm:evil", "lib@github:org/lib#main", "lib@org/lib", "lib@git+https://h/x.git", "lib@https://h/x.tgz", "lib@file:../x", "lib@./x", "lib@~/x", "lib@1.0.0#x"} {
		mustFail(t, "npm", ref, "is not a registry version, range or tag: use the git or url kind, or pin")
	}
}

func TestPyPI(t *testing.T) {
	f := newFake(t)
	t.Setenv("AVAL_PYPI_URL", f.url)
	const (
		r231  = `"2.31.0":[{"filename":"r-2.31.0.tar.gz","digests":{"sha256":"aa"}}]`
		whl   = `{"filename":"r-2.32.3-py3-none-any.whl","digests":{"sha256":"bb"}}`
		sdist = `{"filename":"r-2.32.3.tar.gz","digests":{"sha256":"cc"}}`
		r324  = `"2.32.4":[{"filename":"r-2.32.4.tar.gz","digests":{"sha256":"dd"},"yanked":true}]`
		rc    = `"2.33.0rc1":[{"filename":"r-2.33.0rc1.tar.gz","digests":{"sha256":"ee"}}]`
	)
	doc := func(releases ...string) string { return `{"releases":{` + strings.Join(releases, ",") + `}}` }
	base := doc(r231, `"2.32.3":[`+whl+`,`+sdist+`]`, r324, rc, `"2.34.0":[]`)
	set := func(d string) { f.set("/pypi/requests/json", d) }
	// The attacker's inputs: a dev release under >=2.0b1, an epoch, a
	// four-part ~=, all of which a picker reads differently from pip.
	checkFloating(t, "pypi", []string{"requests", "requests==2.32.*", "requests>=2.0b1", "requests~=1.4.5.0", "requests[socks]>=2,<2.32",
		"requests===2.31.0", "requests==2.31.0,!=2.31.1", "requests=2"}, set,
		base, doc(`"2.34.0":[]`, rc, r324, `"2.32.3":[`+sdist+`,`+whl+`]`, r231), map[string]string{
			"a dev release":                 doc(r231, `"2.32.3":[`+whl+`,`+sdist+`]`, r324, rc, `"2.34.0":[]`, `"2.35.0.dev1":[{"filename":"d.tar.gz","digests":{"sha256":"ff"}}]`),
			"an epoch release":              doc(r231, `"2.32.3":[`+whl+`,`+sdist+`]`, r324, rc, `"2.34.0":[]`, `"1!1.0":[{"filename":"e.tar.gz","digests":{"sha256":"ff"}}]`),
			"a four-part release":           doc(r231, `"2.32.3":[`+whl+`,`+sdist+`]`, r324, rc, `"2.34.0":[]`, `"1.4.5.1":[{"filename":"f.tar.gz","digests":{"sha256":"ff"}}]`),
			"a yanked file":                 doc(r231, `"2.32.3":[`+whl+`,{"filename":"r-2.32.3.tar.gz","digests":{"sha256":"cc"},"yanked":true}]`, r324, rc, `"2.34.0":[]`),
			"a file added to empty release": doc(r231, `"2.32.3":[`+whl+`,`+sdist+`]`, r324, rc, `"2.34.0":[{"filename":"g.tar.gz","digests":{"sha256":"ff"}}]`),
		})
	exact := mustResolve(t, "pypi", "requests==2.32.3")
	if exact.Version != "2.32.3" || !strings.HasPrefix(exact.ID, "sha256:") {
		t.Fatalf("pypi requests==2.32.3 = %+v", exact)
	}
	set(doc(r231, `"2.32.3":[`+whl+`,`+sdist+`]`, r324, rc, `"2.34.0":[]`, `"2.35.0":[{"filename":"n.tar.gz","digests":{"sha256":"ff"}}]`))
	if r := mustResolve(t, "pypi", "requests==2.32.3"); r.ID != exact.ID {
		t.Fatal("an exact version must keep its id when other versions are published")
	}
	set(doc(`"2.32.3":[` + whl + `,` + sdist + `,{"filename":"r-2.32.3-cp313-win_amd64.whl","digests":{"sha256":"ff"}}]`))
	if r := mustResolve(t, "pypi", "requests==2.32.3"); r.ID == exact.ID {
		t.Fatal("a file added to the version must change the id")
	}
	// pip's ==2.32.3 also matches a local version (and prefers it) and a
	// zero-padded one: a private index may serve both.
	same := `"2.32.3":[` + whl + `,` + sdist + `]`
	for why, d := range map[string]string{
		"2.32.3+cu added":     doc(r231, same, `"2.32.3+cu":[{"filename":"l.whl","digests":{"sha256":"ff"}}]`),
		"2.32.3.0 added":      doc(r231, same, `"2.32.3.0":[{"filename":"z.tar.gz","digests":{"sha256":"ff"}}]`),
		"2.32.3+CU.1 added":   doc(r231, same, `"2.32.3+CU.1":[{"filename":"l.whl","digests":{"sha256":"ff"}}]`),
		"02.32.03.00.0 added": doc(r231, same, `"02.32.03.00.0":[{"filename":"z.tar.gz","digests":{"sha256":"ff"}}]`),
		"0!2.32.3 added":      doc(r231, same, `"0!2.32.3":[{"filename":"e.tar.gz","digests":{"sha256":"ff"}}]`),
		"v2.32.3 added":       doc(r231, same, `"v2.32.3":[{"filename":"v.tar.gz","digests":{"sha256":"ff"}}]`),
	} {
		set(d)
		if r := mustResolve(t, "pypi", "requests==2.32.3"); r.ID == exact.ID {
			t.Errorf("%s must change the id of ==2.32.3", why)
		}
	}
	local := `"2.32.3+cu":[{"filename":"l.whl","digests":{"sha256":"ff"}}]`
	padded := `"2.32.3.0":[{"filename":"z.tar.gz","digests":{"sha256":"ff"}}]`
	set(doc(r231, same, local, padded))
	before := mustResolve(t, "pypi", "requests==2.32.3")
	if r := mustResolve(t, "pypi", "requests==2.32.3.0"); r.ID != before.ID || r.Version != "2.32.3.0" {
		t.Fatalf("==2.32.3.0 names the same releases as ==2.32.3: %+v %+v", r, before)
	}
	for why, d := range map[string]string{
		"a republished 2.32.3+cu": doc(r231, same, `"2.32.3+cu":[{"filename":"l.whl","digests":{"sha256":"f0"}}]`, padded),
		"a republished 2.32.3.0":  doc(r231, same, local, `"2.32.3.0":[{"filename":"z.tar.gz","digests":{"sha256":"f0"}}]`),
	} {
		set(d)
		if r := mustResolve(t, "pypi", "requests==2.32.3"); r.ID == before.ID {
			t.Errorf("%s must change the id of ==2.32.3", why)
		}
	}
	// ==X+local matches that local version only.
	set(doc(r231, same, local))
	cu := mustResolve(t, "pypi", "requests==2.32.3+cu")
	set(doc(r231, `"2.32.3":[`+whl+`,{"filename":"r-2.32.3.tar.gz","digests":{"sha256":"c0"}}]`, local, `"2.32.3+other":[{"filename":"o.whl","digests":{"sha256":"ff"}}]`))
	if r := mustResolve(t, "pypi", "requests==2.32.3+CU"); r.ID != cu.ID {
		t.Fatal("==2.32.3+cu binds the local version 2.32.3+cu only")
	}
	set(doc(r231, same, `"2.32.3+cu":[{"filename":"l.whl","digests":{"sha256":"f0"}}]`))
	if r := mustResolve(t, "pypi", "requests==2.32.3+cu"); r.ID == cu.ID {
		t.Fatal("a republished 2.32.3+cu must change the id of ==2.32.3+cu")
	}
	mustFail(t, "pypi", "requests==2.32.3+none", "version 2.32.3+none is not published")
	mustFail(t, "pypi", "requests==abc", "version abc is not published")
	mustFail(t, "pypi", "requests==1!2.32.3", "version 1!2.32.3 is not published")
	set(base)
	// pip installs a yanked version when it is pinned exactly (PEP 592).
	if r := mustResolve(t, "pypi", "requests==2.32.4"); r.Version != "2.32.4" {
		t.Fatalf("pypi requests==2.32.4 = %+v", r)
	}
	mustFail(t, "pypi", "requests==9.0", "version 9.0 is not published")
	mustFail(t, "pypi", "requests==2.34.0", "version 2.34.0 is not published")
	mustFail(t, "pypi", "nope", "404")
	f.set("/pypi/nodigest/json", `{"releases":{"2.32.3":[{"filename":"r-2.32.3.tar.gz"}]}}`)
	mustFail(t, "pypi", "nodigest==2.32.3", "no sha256")
	mustFail(t, "pypi", "nodigest>=2", "no sha256")
	f.set("/pypi/empty/json", `{"releases":{"1.0":[]}}`)
	mustFail(t, "pypi", "empty", "lists no version")
}

// goZip is a module zip as the proxy serves it.
func goZip(t *testing.T, mod, version, body string) string {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, data := range map[string]string{"go.mod": "module " + mod + "\n", "a.go": body} {
		fw, err := w.Create(mod + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(fw, data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestGoModule(t *testing.T) {
	f := newFake(t)
	t.Setenv("GOPROXY", f.url+"/|off")
	const p = "/example.com/!acme/m/@"
	set := func(d string) {
		list, latest, _ := strings.Cut(d, "|")
		f.set(p+"v/list", list)
		f.set(p+"latest", `{"Version":"`+latest+`"}`)
	}
	for _, v := range []string{"v1.0.0", "v1.1.0", "v1.2.0"} {
		f.set(p+"v/"+v+".zip", goZip(t, "example.com/Acme/m", v, "package m // "+v+"\n"))
	}
	// v2.0.0+incompatible is the attacker's input: the go command's latest
	// skips it while a v1 release exists, a picker may not.
	checkFloating(t, "go", []string{"example.com/Acme/m", "example.com/Acme/m@latest", "example.com/Acme/m@v1", "example.com/Acme/m@v1.1",
		"example.com/Acme/m@<v1.2.0", "example.com/Acme/m@>=v1.0.0", "example.com/Acme/m@v2.0.0+incompatible", "example.com/Acme/m@v1.1.0+meta"}, set,
		"v1.0.0\nv1.1.0\n|v1.1.0", "v1.1.0\nv1.0.0|v1.1.0", map[string]string{
			"an +incompatible release": "v1.0.0\nv1.1.0\nv2.0.0+incompatible\n|v1.1.0",
			"a new release":            "v1.0.0\nv1.1.0\nv1.2.0\n|v1.2.0",
			"a moved latest":           "v1.0.0\nv1.1.0\n|v1.0.0",
		})
	// A private (GONOSUMDB) proxy can republish a version: the content of
	// the version @latest names is bound too.
	floatingID := mustResolve(t, "go", "example.com/Acme/m@latest").ID
	f.set(p+"v/v1.1.0.zip", goZip(t, "example.com/Acme/m", "v1.1.0", "package m // republished\n"))
	if r := mustResolve(t, "go", "example.com/Acme/m@latest"); r.ID == floatingID {
		t.Fatal("a republished @latest version must change a floating id")
	}
	f.set(p+"v/main.info", `{"Version":"v1.2.1-0.20260101000000-abcdefabcdef"}`)
	for _, v := range []string{"v1.1.0", "v1.2.0", "v1.2.1-0.20260101000000-abcdefabcdef"} {
		f.set(p+"v/"+v+".zip", goZip(t, "example.com/Acme/m", v, "package m // "+v+"\n"))
	}
	for ref, want := range map[string]string{
		"example.com/Acme/m@v1.1.0": "v1.1.0", "example.com/Acme/m@main": "v1.2.1-0.20260101000000-abcdefabcdef",
		"example.com/Acme/m@v1.2.1-0.20260101000000-abcdefabcdef": "v1.2.1-0.20260101000000-abcdefabcdef",
	} {
		if r := mustResolve(t, "go", ref); r.Version != want || !strings.HasPrefix(r.ID, "h1:") {
			t.Errorf("go %s = %s %s, want %s", ref, r.Version, r.ID, want)
		}
	}
	exact := mustResolve(t, "go", "example.com/Acme/m@v1.1.0")
	set("v1.1.0\nv1.2.0\n|v1.2.0")
	if r := mustResolve(t, "go", "example.com/Acme/m@v1.1.0"); r.ID != exact.ID {
		t.Fatal("an exact version must keep its id when other versions are published")
	}
	before := mustResolve(t, "go", "example.com/Acme/m@main")
	f.set(p+"v/main.info", `{"Version":"v1.2.0"}`)
	if after := mustResolve(t, "go", "example.com/Acme/m@main"); after.ID == before.ID {
		t.Fatal("a moved branch must change the id")
	}
	// A module without tagged versions: @latest is the proxy's pseudo-version.
	const u = "/example.com/untagged/@"
	f.set(u+"v/list", "")
	f.set(u+"latest", `{"Version":"v0.0.0-20260101000000-abcdefabcdef"}`)
	mustFail(t, "go", "example.com/untagged@latest", "404") // no zip for @latest: nothing to bind
	f.set(u+"v/v0.0.0-20260101000000-abcdefabcdef.zip", goZip(t, "example.com/untagged", "v0.0.0-20260101000000-abcdefabcdef", "package u\n"))
	if r := mustResolve(t, "go", "example.com/untagged@latest"); r.Version != floating {
		t.Errorf("go example.com/untagged@latest = %s, want floating", r.Version)
	}
	f.set("/example.com/nolatest/@v/list", "v1.0.0\n")
	f.set(p+"v/bad.info", `{"Version":"v1.0.0!"}`)
	mustFail(t, "go", "example.com/Acme/m@v1.3.0", "404")
	mustFail(t, "go", "example.com/Acme/m@bad", "disallowed version string")
	mustFail(t, "go", "example.com/Acme/m@v1.0.0!", "disallowed version string")
	mustFail(t, "go", "example.com/none@latest", "404")
	mustFail(t, "go", "example.com/nolatest@v1", "404")
	// A zip whose headers claim more than MaxDownload is never expanded.
	var bomb bytes.Buffer
	w := zip.NewWriter(&bomb)
	fw, err := w.CreateRaw(&zip.FileHeader{Name: "example.com/!acme/m@v1.4.0/a.go", Method: zip.Store, CompressedSize64: 1, UncompressedSize64: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("x"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.set(p+"v/v1.4.0.zip", bomb.String())
	mustFail(t, "go", "example.com/Acme/m@v1.4.0", "module zip expands beyond")
	f.set(p+"v/v1.5.0.zip", "not a zip")
	mustFail(t, "go", "example.com/Acme/m@v1.5.0", "zip")
	t.Setenv("GOPROXY", "direct")
	mustFail(t, "go", "example.com/Acme/m@latest", "go would fetch this module directly")
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("GOPROXY", f.url)
	mustFail(t, "go", "example.com/Acme/m@v1.1.0", "aval-gomod")
}

func TestMaven(t *testing.T) {
	f := newFake(t)
	t.Setenv("AVAL_MAVEN_URL", f.url)
	const dir = "/org/junit/junit-bom/"
	meta := func(latest, release string, versions ...string) string {
		return `<metadata><versioning><latest>` + latest + `</latest><release>` + release + `</release><versions><version>` +
			strings.Join(versions, `</version><version>`) + `</version></versions></versioning></metadata>`
	}
	set := func(d string) { f.set(dir+"maven-metadata.xml", d) }
	for _, v := range []string{"1.20", "4.13", "5.9.3", "5.10.2", "5.11.0-M1", "5.11.0-M2"} {
		f.set(dir+v+"/junit-bom-"+v+".pom", "<project><packaging>pom</packaging>"+v+"</project>")
	}
	// 1.2+ is the attacker's Gradle input: Gradle reads it as a text
	// prefix, so 1.20 matches; a picker that reads 1.2.x does not.
	checkFloating(t, "maven", []string{"org.junit:junit-bom:5.+", "org.junit:junit-bom:+", "org.junit:junit-bom:1.2+", "org.junit:junit-bom:[5.0,6.0)",
		"org.junit:junit-bom:[1,2),[3,4)", "org.junit:junit-bom:LATEST", "org.junit:junit-bom:RELEASE", "org.junit:junit-bom:release",
		"org.junit:junit-bom:latest.release", "org.junit:junit-bom:5..0"}, set,
		meta("5.11.0-M1", "5.10.2", "4.13", "5.9.3", "5.10.2", "5.11.0-M1"), meta("5.11.0-M1", "5.10.2", "5.11.0-M1", "5.10.2", "5.9.3", "4.13"), map[string]string{
			"a new milestone": meta("5.11.0-M1", "5.10.2", "4.13", "5.9.3", "5.10.2", "5.11.0-M1", "5.11.0-M2"),
			"1.20 published":  meta("5.11.0-M1", "5.10.2", "1.20", "4.13", "5.9.3", "5.10.2", "5.11.0-M1"),
			"a moved release": meta("5.11.0-M1", "5.9.3", "4.13", "5.9.3", "5.10.2", "5.11.0-M1"),
			"a moved latest":  meta("5.10.2", "5.10.2", "4.13", "5.9.3", "5.10.2", "5.11.0-M1"),
		})
	f.set(dir+"5.9.3/junit-bom-5.9.3.pom", "<project><packaging>pom</packaging>5.9.3</project>")
	exact := mustResolve(t, "maven", "org.junit:junit-bom:5.9.3")
	if exact.Version != "5.9.3" || !strings.HasPrefix(exact.ID, "sha256:") {
		t.Fatalf("maven 5.9.3 = %+v", exact)
	}
	set(meta("5.12.0", "5.12.0", "5.9.3", "5.12.0"))
	if r := mustResolve(t, "maven", "org.junit:junit-bom:5.9.3"); r.ID != exact.ID {
		t.Fatal("an exact version must keep its id when other versions are published")
	}
	f.set(dir+"5.9.3/junit-bom-5.9.3.pom", "<project><packaging>pom</packaging>republished</project>")
	if r := mustResolve(t, "maven", "org.junit:junit-bom:5.9.3"); r.ID == exact.ID {
		t.Fatal("a republished POM must change the id")
	}
	f.set("/x/bad/maven-metadata.xml", "<metadata")
	mustFail(t, "maven", "org.junit:junit-bom", "want group:artifact:version")
	mustFail(t, "maven", "org.junit::1", "want group:artifact:version")
	mustFail(t, "maven", "x:bad:LATEST", "XML")
	mustFail(t, "maven", "x:none:RELEASE", "404")
	f.set("/x/empty/maven-metadata.xml", `<metadata><versioning><latest></latest></versioning></metadata>`)
	mustFail(t, "maven", "x:empty:LATEST", "maven-metadata.xml names no version")
	// A SNAPSHOT is redeployed under the same name: never resolved, in any case.
	f.set("/x/snap/maven-metadata.xml", meta("1.1-SNAPSHOT", "", "1.0", "1.1-SNAPSHOT"))
	mustFail(t, "maven", "x:snap:1.1-SNAPSHOT", "SNAPSHOT versions are not supported: resolve a release, or pin")
	mustFail(t, "maven", "x:snap:1.1-snapshot", "SNAPSHOT versions are not supported")
	mustFail(t, "maven", "x:snap:LATEST", "SNAPSHOT versions are not supported")
	// maven-resolver reads any version ending in SNAPSHOT as one.
	for _, v := range []string{"1.0.SNAPSHOT", "1.0SNAPSHOT", "1.0.snapshot"} {
		mustFail(t, "maven", "x:snap:"+v, "SNAPSHOT versions are not supported")
	}

	// A jar: the published checksum of the artifact is part of the id.
	const lib = "/org/x/lib/1.0/lib-1.0"
	f.set(lib+".pom", "<project></project>")
	f.set(lib+".jar.sha1", strings.Repeat("a", 40)+"  lib-1.0.jar\n")
	jar := mustResolve(t, "maven", "org.x:lib:1.0")
	f.set(lib+".jar.sha256", strings.Repeat("D", 64))
	if again := mustResolve(t, "maven", "org.x:lib:1.0"); again.ID == jar.ID {
		t.Fatal("the sha256 checksum must be preferred over sha1")
	}
	jar = mustResolve(t, "maven", "org.x:lib:1.0")
	f.set("/org/x/lib/maven-metadata.xml", meta("1.0", "1.0", "1.0"))
	ranged := mustResolve(t, "maven", "org.x:lib:[1.0,2.0)")
	f.set(lib+".jar.sha256", strings.Repeat("0", 64))
	if again := mustResolve(t, "maven", "org.x:lib:1.0"); again.ID == jar.ID {
		t.Fatal("a jar redeployed under the same version must change the id")
	}
	if again := mustResolve(t, "maven", "org.x:lib:[1.0,2.0)"); again.ID == ranged.ID {
		t.Fatal("a redeployed release must change a floating id")
	}
	// A floating version binds each listed version's content: a version
	// without a POM fails, and so does a list too long to fetch.
	f.set("/org/x/lib/maven-metadata.xml", meta("1.1", "1.1", "1.0", "1.1"))
	mustFail(t, "maven", "org.x:lib:LATEST", "404")
	many := make([]string, 101)
	for i := range many {
		many[i] = "1." + strconv.Itoa(i)
	}
	f.set("/org/x/lib/maven-metadata.xml", meta("1.0", "1.0", many...))
	mustFail(t, "maven", "org.x:lib:LATEST", "too many versions to bind: use an exact version")
	// A proxy that answers 200 with a page for a missing checksum must not
	// make the jar part of the id constant.
	for _, body := range []string{"<html>not found</html>", " ", strings.Repeat("a", 40)} {
		f.set(lib+".jar.sha256", body)
		mustFail(t, "maven", "org.x:lib:1.0", "not a checksum")
	}
	f.set("/org/x/war/1.0/war-1.0.pom", "<project><packaging>war</packaging></project>")
	mustFail(t, "maven", "org.x:war:1.0", "404")
	mustFail(t, "maven", "org.x:missing:1.0", "404")
	f.set("/org/x/odd/1.0/odd-1.0.pom", "<project><packaging>nar</packaging></project>")
	mustFail(t, "maven", "org.x:odd:1.0", "unknown artifact file")
	f.set("/org/x/bad/1.0/bad-1.0.pom", "<project")
	mustFail(t, "maven", "org.x:bad:1.0", "POM")
}

// TestMavenJar: a g:a:v consumer downloads <a>-<v>.jar whatever
// <packaging> says, so a jar the repository has is always bound.
func TestMavenJar(t *testing.T) {
	f := newFake(t)
	t.Setenv("AVAL_MAVEN_URL", f.url)
	// A g:a:v consumer downloads <a>-<v>.jar whatever <packaging> says: a
	// pom-packaged artifact with a jar beside it binds the jar too.
	const bom = "/org/x/bom/1.0/bom-1.0"
	f.set(bom+".pom", "<project><packaging>pom</packaging></project>")
	pomOnly := mustResolve(t, "maven", "org.x:bom:1.0")
	f.set(bom+".jar.sha1", strings.Repeat("a", 40))
	withJar := mustResolve(t, "maven", "org.x:bom:1.0")
	if withJar.ID == pomOnly.ID {
		t.Fatal("a jar beside a pom-packaged POM must be part of the id")
	}
	f.set(bom+".jar.sha1", strings.Repeat("b", 40)) // redeployed with a fresh sha1
	if again := mustResolve(t, "maven", "org.x:bom:1.0"); again.ID == withJar.ID {
		t.Fatal("a jar redeployed beside a pom-packaged POM must change the id")
	}
	// A jar served without a checksum cannot be bound: fail, never skip it.
	f.set("/org/x/nosum/1.0/nosum-1.0.pom", "<project><packaging>pom</packaging></project>")
	f.set("/org/x/nosum/1.0/nosum-1.0.jar", "jar")
	mustFail(t, "maven", "org.x:nosum:1.0", "has no published checksum")
	// A war binds the war and, when there is one, the jar.
	const war = "/org/x/web/1.0/web-1.0"
	f.set(war+".pom", "<project><packaging>war</packaging></project>")
	f.set(war+".war.sha1", strings.Repeat("a", 40))
	warOnly := mustResolve(t, "maven", "org.x:web:1.0")
	f.set(war+".jar.sha1", strings.Repeat("c", 40))
	if again := mustResolve(t, "maven", "org.x:web:1.0"); again.ID == warOnly.ID {
		t.Fatal("a jar beside a war must be part of the id")
	}
	// A jar packaging without a jar fails.
	f.set("/org/x/nojar/1.0/nojar-1.0.pom", "<project></project>")
	mustFail(t, "maven", "org.x:nojar:1.0", "404")
	// Only a missing checksum is skipped: a failing server is not "no jar".
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".pom") {
			_, _ = io.WriteString(w, "<project><packaging>pom</packaging></project>")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer broken.Close()
	t.Setenv("AVAL_MAVEN_URL", broken.URL)
	mustFail(t, "maven", "org.x:bom:1.0", "503")
}

func TestCrate(t *testing.T) {
	f := newFake(t)
	t.Setenv("AVAL_CRATES_INDEX", f.url)
	const (
		a = `{"name":"serde","vers":"1.0.0","cksum":"a1"}`
		b = `{"name":"serde","vers":"1.0.200","cksum":"a2"}`
		c = `{"name":"serde","vers":"1.0.201","cksum":"a3","yanked":true}`
		d = `{"name":"serde","vers":"2.0.0-beta.1","cksum":"a5"}`
	)
	index := func(lines ...string) string { return strings.Join(lines, "\n") + "\n" }
	set := func(body string) {
		for _, path := range []string{"/se/rd/serde", "/1/a", "/2/ab", "/3/a/abc"} {
			f.set(path, body)
		}
	}
	// A bare 1.0.0 is ^1.0.0 to Cargo: floating. <=1.2 is the attacker's
	// partial: Cargo reads it as <1.3.0.
	checkFloating(t, "crate", []string{"serde", "serde@1", "serde@*", "serde@<=1.2", "serde@^2.0.0-beta.0", "serde@1.0.0", "a@~1.0", "ab@>=1,<1.0.100", "abc@=1"}, set,
		index(a, b, c, d), index(d, c, b, a), map[string]string{
			"a new release":          index(a, b, c, d, `{"name":"serde","vers":"1.2.5","cksum":"a4"}`),
			"a yanked version":       index(a, `{"name":"serde","vers":"1.0.200","cksum":"a2","yanked":true}`, c, d),
			"an unyanked version":    index(a, b, `{"name":"serde","vers":"1.0.201","cksum":"a3"}`, d),
			"a republished checksum": index(a, `{"name":"serde","vers":"1.0.200","cksum":"a9"}`, c, d),
		})
	for ref, want := range map[string]string{"serde@=1.0.200": "1.0.200 sha256:a2", "a@=1.0.0": "1.0.0 sha256:a1"} {
		if r := mustResolve(t, "crate", ref); r.Version+" "+r.ID != want {
			t.Errorf("crate %s = %s %s, want %s", ref, r.Version, r.ID, want)
		}
	}
	set(index(a, b, c, d, `{"name":"serde","vers":"1.0.202","cksum":"a6"}`))
	if r := mustResolve(t, "crate", "serde@=1.0.200"); r.ID != "sha256:a2" {
		t.Fatal("an exact version must keep its id when other versions are published")
	}
	f.set("/bo/gu/bogus", "{")
	f.set("/no/su/nosum", `{"vers":"1.0.0"}`)
	mustFail(t, "crate", "bogus@1", "index entry")
	mustFail(t, "crate", "nosum@=1.0.0", "no checksum")
	mustFail(t, "crate", "nosum", "no checksum")
	mustFail(t, "crate", "serde@=1.0.201", "version 1.0.201 is not published") // Cargo never picks a yanked version
	mustFail(t, "crate", "serde@=9.9.9", "version 9.9.9 is not published")
	mustFail(t, "crate", "nope@1", "404")
	for _, ref := range []string{"serde@git:x", "serde@org/serde", "serde@./x", "serde@1#x"} {
		mustFail(t, "crate", ref, "is not a registry version, range or tag")
	}
}

func TestGem(t *testing.T) {
	f := newFake(t)
	t.Setenv("AVAL_RUBYGEMS_URL", f.url)
	const (
		a = `{"number":"7.1.3","sha":"s1","platform":"ruby"}`
		b = `{"number":"7.1.3","sha":"s2","platform":"x86_64-linux"}`
		c = `{"number":"7.0.8","sha":"s3","platform":"ruby"}`
		d = `{"number":"7.2.0.beta1","sha":"s4","platform":"ruby"}`
		e = `{"number":"6.1.0","sha":"s5","platform":"ruby"}`
	)
	doc := func(entries ...string) string { return "[" + strings.Join(entries, ",") + "]" }
	set := func(body string) { f.set("/api/v1/versions/rails.json", body) }
	// ~>1.4.5.0 is the attacker's four-part input: RubyGems reads it as
	// >=1.4.5.0, <1.4.6.
	checkFloating(t, "gem", []string{"rails", "rails@", "rails@~>7.0", "rails@~>1.4.5.0", "rails@>=6,<7", "rails@=7.0.8", "rails@!=7.1.3"}, set,
		doc(a, b, c, d, e), doc(e, d, c, b, a), map[string]string{
			"a new release":          doc(a, b, c, d, e, `{"number":"1.4.5.1","sha":"s6","platform":"ruby"}`),
			"a yanked version":       doc(a, b, c, d),
			"a republished checksum": doc(a, `{"number":"7.1.3","sha":"s9","platform":"x86_64-linux"}`, c, d, e),
		})
	exact := mustResolve(t, "gem", "rails@7.1.3")
	if exact.Version != "7.1.3" || !strings.HasPrefix(exact.ID, "sha256:") {
		t.Fatalf("gem rails@7.1.3 = %+v", exact)
	}
	if r := mustResolve(t, "gem", "rails@7.2.0.beta1"); r.Version != "7.2.0.beta1" {
		t.Fatalf("gem rails@7.2.0.beta1 = %+v", r)
	}
	set(doc(a, b, c, d, e, `{"number":"7.1.4","sha":"s7","platform":"ruby"}`))
	if r := mustResolve(t, "gem", "rails@7.1.3"); r.ID != exact.ID {
		t.Fatal("an exact version must keep its id when other versions are published")
	}
	for why, body := range map[string]string{
		"a changed platform gem":          doc(a, `{"number":"7.1.3","sha":"s9","platform":"x86_64-linux"}`, c, d, e),
		"7.1.3.0, equal to 7.1.3 for gem": doc(a, b, c, d, e, `{"number":"7.1.3.0","sha":"s8","platform":"ruby"}`),
	} {
		set(body)
		if r := mustResolve(t, "gem", "rails@7.1.3"); r.ID == exact.ID {
			t.Errorf("%s must change the id", why)
		}
	}
	set(doc(a, b, c, d, e))
	mustFail(t, "gem", "rails@9", "version 9 is not published")
	mustFail(t, "gem", "nope", "404")
	f.set("/api/v1/versions/nosha.json", `[{"number":"7.1.3","platform":"ruby"}]`)
	mustFail(t, "gem", "nosha@7.1.3", "no checksum")
	mustFail(t, "gem", "nosha", "no checksum")
	f.set("/api/v1/versions/empty.json", `[]`)
	mustFail(t, "gem", "empty", "lists no version")
	for _, ref := range []string{"rails@github:rails/rails", "rails@rails/rails", "rails@~/x", "rails@https://h/x.gem"} {
		mustFail(t, "gem", ref, "is not a registry version, range or tag")
	}
}

// TestGemCanon: an exact gem version binds every published number RubyGems
// holds equal to it.
func TestGemCanon(t *testing.T) {
	for _, eq := range [][2]string{{"1.2", "1.2.0"}, {"1.2a", "1.2.a"}, {"1.0.a", "1.0.a.0"}, {"1.01", "1.1"}, {"1.0.0.beta1", "1.beta1"}} {
		if gemCanon(eq[0]) != gemCanon(eq[1]) {
			t.Errorf("gemCanon(%s) != gemCanon(%s)", eq[0], eq[1])
		}
	}
	for _, ne := range [][2]string{{"1.2", "1.2.1"}, {"1.2", "1.2.beta"}, {"1.10", "1.1"}} {
		if gemCanon(ne[0]) == gemCanon(ne[1]) {
			t.Errorf("gemCanon(%s) == gemCanon(%s)", ne[0], ne[1])
		}
	}
}

func TestURL(t *testing.T) {
	f := newFake(t)
	f.set("/tool.tgz", "v1")
	f.set("/mirror/tool.tgz", "v1")
	a := mustResolve(t, "url", f.url+"/tool.tgz")
	if b := mustResolve(t, "url", f.url+"/mirror/tool.tgz"); a.ID != b.ID {
		t.Fatal("the same bytes under another URL must keep the id")
	}
	f.set("/tool.tgz", "v2")
	if c := mustResolve(t, "url", f.url+"/tool.tgz"); c.ID == a.ID {
		t.Fatal("changed content must change the id")
	}
	mustFail(t, "url", f.url+"/gone", "404")
	mustFail(t, "url", "ftp://example.com/x", "unsupported protocol scheme")
	mustFail(t, "url", "http://exa mple.com/%zz", "invalid")
	if err := get(context.Background(), f.url+"/tool.tgz", "", 1, io.Discard); err == nil || !strings.Contains(err.Error(), "larger than 1 bytes") {
		t.Fatalf("size cap: %v", err)
	}
	if err := get(context.Background(), "http://127.0.0.1:1/x", "", 1, io.Discard); err == nil {
		t.Fatal("an unreachable server must fail")
	}
}

// TestCredentialsOnlyOverHTTPS: over plain http, credentials go to a
// loopback host only (a contract can name any URL), and a redirect to plain
// http drops them.
func TestCredentialsOnlyOverHTTPS(t *testing.T) {
	netrcFile := filepath.Join(t.TempDir(), "netrc")
	if err := os.WriteFile(netrcFile, []byte("machine example.com login u password secret\nmachine localhost login l password lp\nmachine ::1 login v password vp"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETRC", netrcFile)
	t.Setenv("NPM_CONFIG_USERCONFIG", os.DevNull)
	for u, want := range map[string]bool{"http://example.com/x": false, "https://example.com/x": true, "http://localhost:1/x": true, "http://[::1]:1/x": true, "http://other.example/x": false} {
		req := httptest.NewRequest(http.MethodGet, u, nil)
		authorize(req)
		if got := req.Header.Get("Authorization") != ""; got != want {
			t.Errorf("%s: credentials sent = %v, want %v", u, got, want)
		}
	}
	redirect := httptest.NewRequest(http.MethodGet, "http://example.com/x", nil)
	redirect.Header.Set("Authorization", "Basic x")
	if err := client.CheckRedirect(redirect, []*http.Request{httptest.NewRequest(http.MethodGet, "https://example.com/x", nil)}); err != nil || redirect.Header.Get("Authorization") != "" {
		t.Fatalf("a redirect to http must drop credentials: %v", err)
	}
	if err := client.CheckRedirect(redirect, make([]*http.Request, 10)); err == nil {
		t.Fatal("ten redirects must stop")
	}
}

// TestNpmTokenBoundary: a token goes only to its host and path, on a
// boundary: a lookalike host or a longer path segment never gets it.
func TestNpmTokenBoundary(t *testing.T) {
	npmrc := filepath.Join(t.TempDir(), "npmrc")
	t.Setenv("NPM_CONFIG_USERCONFIG", npmrc)
	for key, cases := range map[string]map[string]string{
		"registry.example.com": {
			"registry.example.com": "t", "registry.example.com/x": "t",
			"registry.example.com.attacker.tld/x": "", "registry.example.comx/x": "",
		},
		"registry.example.com/npm/": {"registry.example.com/npm/pkg": "t", "registry.example.com/npmx": ""},
		"registry.example.com/npm":  {"registry.example.com/npm/pkg": "t", "registry.example.com/npmx/pkg": ""},
	} {
		if err := os.WriteFile(npmrc, []byte("//"+key+":_authToken=t\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for hostPath, want := range cases {
			if got := npmToken(hostPath); got != want {
				t.Errorf("npmrc key %s, request %s: token %q, want %q", key, hostPath, got, want)
			}
		}
	}
}

func TestCredentials(t *testing.T) {
	f := newFake(t)
	f.set("/f", "x")
	dir := t.TempDir()
	host := strings.TrimPrefix(f.url, "http://")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NETRC", filepath.Join(dir, "netrc"))
	t.Setenv("NPM_CONFIG_USERCONFIG", filepath.Join(dir, "npmrc"))
	t.Setenv("NPM_TOKEN", "tok2")
	write("netrc", "machine other login o password p\nmachine 127.0.0.1 login u password secret\ndefault login d password dp trailing")
	mustResolve(t, "url", f.url+"/f")
	if got := f.lastAuth(); got != "Basic dTpzZWNyZXQ=" { // u:secret
		t.Fatalf("netrc auth = %q", got)
	}
	write("netrc", "machine other login o password p\ndefault\nlogin d password dp")
	mustResolve(t, "url", f.url+"/f")
	if got := f.lastAuth(); got != "" {
		t.Fatalf("the netrc default entry must never be sent, got %q", got)
	}
	write("npmrc", "registry=x\n//"+host+"/:_authToken=tok1\n//"+host+"/f:_authToken=${NPM_TOKEN}\n")
	mustResolve(t, "url", f.url+"/f")
	if got := f.lastAuth(); got != "Bearer tok2" {
		t.Fatalf("npm token = %q", got)
	}
	write("netrc", "machine other login o")
	write("npmrc", "")
	mustResolve(t, "url", f.url+"/f")
	if got := f.lastAuth(); got != "" {
		t.Fatalf("no credentials, got %q", got)
	}
	// A registry variable may carry userinfo: errors must not show it.
	f.set("/pypi/x/json", "{")
	cred := strings.Replace(f.url, "http://", "http://u:s3cret@", 1)
	tok := strings.Replace(f.url, "http://", "http://tok3n@", 1)
	t.Setenv("GOPROXY", cred)
	t.Setenv("npm_config_registry", tok)
	t.Setenv("AVAL_PYPI_URL", cred)
	for kind, ref := range map[string]string{"go": "example.com/m@v1.0.0", "npm": "left-pad@1", "pypi": "x"} {
		_, err := resolveOne(t, kind, ref)
		if err == nil || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "tok3n") {
			t.Errorf("%s error must not show credentials: %v", kind, err)
		}
	}
	for _, u := range []string{"http://u:s3cret@127.0.0.1:1/x", "http://u:s3cret@[::1/x"} {
		if err := get(context.Background(), u, "", 1, io.Discard); err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("get %s: %v", u, err)
		}
	}
	t.Setenv("NETRC", "")
	t.Setenv("HOME", dir)
	write(".netrc", "machine 127.0.0.1 login h password hp")
	if _, _, ok := netrc("127.0.0.1"); !ok {
		t.Fatal("~/.netrc must be read when $NETRC is unset")
	}
}

func TestCredentialsInHTTPURLRefused(t *testing.T) {
	// Userinfo in a registry variable becomes Basic auth: never over plain
	// http to a remote host.
	t.Setenv("GOPROXY", "http://u:tok@example.invalid")
	if _, err := resolveOne(t, "go", "example.com/m@v1.0.0"); err == nil || !strings.Contains(err.Error(), "credentials in an http URL would be sent in clear") || strings.Contains(err.Error(), "tok@") {
		t.Errorf("credentials over http must be refused: %v", err)
	}
}

// gitRepo makes a repository with a branch main, an annotated tag v1 and a
// lightweight tag main-tag, and returns its file URL and a commit runner.
func gitRepo(t *testing.T) (string, func(string) string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	commit := func(msg string) string {
		run("commit", "-q", "--allow-empty", "-m", msg)
		return run("rev-parse", "HEAD")
	}
	commit("one")
	run("tag", "-a", "v1", "-m", "v1")
	return "file://" + dir, commit
}

func TestGit(t *testing.T) {
	repo, commit := gitRepo(t)
	first := mustResolve(t, "git", repo+"@main")
	if first.Version != "refs/heads/main" || len(first.ID) != 40 {
		t.Fatalf("git main = %+v", first)
	}
	if tag := mustResolve(t, "git", repo+"@v1"); tag.ID != first.ID || tag.Version != "refs/tags/v1" {
		t.Fatalf("an annotated tag must resolve to its commit: %+v, want %s", tag, first.ID)
	}
	if head := mustResolve(t, "git", repo+"@HEAD"); head.ID != first.ID {
		t.Fatalf("HEAD = %+v", head)
	}
	second := commit("two")
	if moved := mustResolve(t, "git", repo+"@refs/heads/main"); moved.ID != second || moved.ID == first.ID {
		t.Fatal("a moved branch must change the id")
	}
	if pinned := mustResolve(t, "git", repo+"@"+first.ID); pinned.ID != first.ID {
		t.Fatal("a full commit id resolves to itself")
	}
	repo2, _ := gitRepo(t)
	_ = exec.Command("git", "-C", strings.TrimPrefix(repo2, "file://"), "branch", "v1").Run()
	mustFail(t, "git", repo2+"@v1", "ambiguous")
	mustFail(t, "git", repo+"@nope", `no ref "nope"`)
	mustFail(t, "git", repo, "want <url>@<ref>")
	mustFail(t, "git", repo+"@", "want <url>@<ref>")
	mustFail(t, "git", "ext::sh@main", "unsupported URL scheme")
	mustFail(t, "git", "http://[::1@main", "missing ']'")
	mustFail(t, "git", repo+"@--upload-pack=touch", "must not start with -")
	mustFail(t, "git", "file:///nonexistent/aval@main", "git ls-remote")
}

func TestImage(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	put := func(tag, config string) {
		t.Helper()
		m := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[],"annotations":{"c":"` + config + `"}}`
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v2/acme/db/manifests/"+tag, strings.NewReader(m))
		req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("put %s: %v %v", tag, err, resp)
		}
		_ = resp.Body.Close()
	}
	put("16", "a")
	put("16.4", "a")
	a := mustResolve(t, "image", host+"/acme/db:16")
	if b := mustResolve(t, "image", host+"/acme/db:16.4"); b.ID != a.ID || !strings.HasPrefix(a.ID, "sha256:") {
		t.Fatalf("the same manifest under another tag must keep the id: %s %s", a.ID, b.ID)
	}
	if d := mustResolve(t, "image", host+"/acme/db@"+a.ID); d.ID != a.ID {
		t.Fatal("a digest reference resolves to itself")
	}
	put("16", "b")
	if c := mustResolve(t, "image", host+"/acme/db:16"); c.ID == a.ID {
		t.Fatal("a repushed tag must change the id")
	}
	mustFail(t, "image", host+"/acme/db:17", "not found")
	mustFail(t, "image", "UPPER/x", "invalid")
}

func TestAllWrapsResolverErrors(t *testing.T) {
	saved := Resolvers["url"]
	t.Cleanup(func() { Resolvers["url"] = saved })
	Resolvers["url"] = func(context.Context, string) (string, string, error) { return "", "", errors.New("boom") }
	mustFail(t, "url", "x", "boom")
}

// TestLocalFileSpecsRejected: npm and pip install these from a local file,
// not from the registry entry of the name.
func TestLocalFileSpecsRejected(t *testing.T) {
	for _, ref := range []string{"lib@x.tgz", "lib@x.TAR.GZ", "lib@x.tar", `lib@a\b`} {
		mustFail(t, "npm", ref, "is not a registry version, range or tag")
	}
	for _, ref := range []string{"x.whl", "x-1.0.tar.gz", "x.zip", "x.tar", "x.egg", "X.WHL==1.0", "demo.tar.xz", "demo.tar.xz[x]", "x.tar.lzma"} {
		mustFail(t, "pypi", ref, "not a valid pypi package name")
	}
}

// TestPackageNamesRejected: the resolver checks the name again, so a name
// npm, pip, go, Cargo, gem or Maven reads as a path or a file never reaches
// a registry that would answer for its escaped form.
func TestPackageNamesRejected(t *testing.T) {
	for kind, refs := range map[string][]string{
		"npm":   {"left-pad/", "@types/node/", "./x", "../x", "/x", "C:x", "x.tgz", "@UP/x"},
		"pypi":  {"./x", "../x", "/x", "dir/x", `dir\x`, "C:x"},
		"go":    {"./x", "../x", "/x", "C:x", "Example..com/m@latest"},
		"crate": {"./x", "../x", "/x", "C:x", "x.tgz"},
		"gem":   {"./x", "../x", "/x", "C:x", "x.gem"},
		"maven": {"../x:a:1", "a..b:x:1", "g:./x:1", "g:a/b:1"},
	} {
		for _, ref := range refs {
			mustFail(t, kind, ref, "not a valid "+kind+" package name")
		}
	}
}

func TestGitCommitThatIsAlsoARefName(t *testing.T) {
	repo, _ := gitRepo(t)
	sha := mustResolve(t, "git", repo+"@main").ID
	dir := strings.TrimPrefix(repo, "file://")
	if out, err := exec.Command("git", "-C", dir, "tag", sha).CombinedOutput(); err != nil {
		t.Fatalf("git tag: %v %s", err, out)
	}
	mustFail(t, "git", repo+"@"+sha, "is both a commit id and a ref name")
}

// TestRegistryConfigDivergence: when the tool would install from another
// registry than the one Aval resolves against, resolution fails.
func TestRegistryConfigDivergence(t *testing.T) {
	f := newFake(t)
	f.set("/left", `{"versions":{"1.0.0":{"dist":{"integrity":"sha512-a"}}}}`)
	f.set("/@acme%2fleft", `{"versions":{"1.0.0":{"dist":{"integrity":"sha512-a"}}}}`)
	npmrc := filepath.Join(t.TempDir(), "npmrc")
	t.Setenv("NPM_CONFIG_USERCONFIG", npmrc)
	write := func(s string) {
		if err := os.WriteFile(npmrc, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("npm_config_registry", f.url)
	write("; comment\n@acme:registry=" + f.url + "/\n@other:registry=https://other.example/\nregistry=https://ignored.example/\n")
	mustResolve(t, "npm", "@acme/left@1.0.0") // same registry
	mustResolve(t, "npm", "left@1.0.0")       // the environment beats registry= for npm too
	write("@acme:registry=https://evil.example/\n")
	mustFail(t, "npm", "@acme/left@1.0.0", "@acme:registry")
	mustResolve(t, "npm", "left@1.0.0")
	t.Setenv("npm_config_registry", "")
	write(" registry = https://corp.example/npm/ \n")
	mustFail(t, "npm", "left@1.0.0", "registry")
	write("registry=https://registry.npmjs.org/\n//registry.npmjs.org/:_authToken=x\n")
	if err := npmRegistry("left", "https://registry.npmjs.org"); err != nil {
		t.Fatal(err)
	}

	for env, mods := range map[string][]string{
		"GOPRIVATE": {"example.com/acme/m", "example.com/acme/m/v2"}, "GONOPROXY": {"example.com/acme/m"},
	} {
		t.Setenv(env, "other.example,example.com/acme")
		for _, m := range mods {
			mustFail(t, "go", m+"@v1.0.0", "go would fetch this module directly: pin it or use the git kind")
		}
		t.Setenv(env, "")
	}
	// Without the checksum database a proxy can serve any content for a
	// version: refused.
	for env, val := range map[string]string{"GOSUMDB": "off", "GONOSUMDB": "other.example,example.com/acme", "GOINSECURE": "example.com/acme", "GOFLAGS": "-mod=mod -insecure"} {
		t.Setenv(env, val)
		mustFail(t, "go", "example.com/acme/m@v1.0.0", "go would not verify this module against the checksum database")
		t.Setenv(env, "")
	}
	t.Setenv("GOPRIVATE", "*.example.com")
	mustFail(t, "go", "git.example.com/m@v1.0.0", "go would fetch this module directly")
	t.Setenv("GOPRIVATE", "")
	for _, p := range []string{"direct", "off", "direct," + f.url, "off|" + f.url} {
		t.Setenv("GOPROXY", p)
		mustFail(t, "go", "example.com/m@v1.0.0", "go would fetch this module directly")
	}

	t.Setenv("AVAL_PYPI_URL", f.url)
	f.set("/pypi/x/json", `{"releases":{"1.0":[{"filename":"x-1.0.tar.gz","digests":{"sha256":"aa"}}]}}`)
	for _, same := range []string{"", f.url, f.url + "/", f.url + "/simple/"} {
		t.Setenv("PIP_INDEX_URL", same)
		mustResolve(t, "pypi", "x==1.0")
	}
	t.Setenv("PIP_INDEX_URL", "https://corp.example/simple")
	mustFail(t, "pypi", "x==1.0", "PIP_INDEX_URL")
	t.Setenv("PIP_INDEX_URL", "")
	t.Setenv("PIP_EXTRA_INDEX_URL", "https://corp.example/simple")
	mustFail(t, "pypi", "x==1.0", "PIP_EXTRA_INDEX_URL")
	t.Setenv("PIP_EXTRA_INDEX_URL", "")
	t.Setenv("PIP_FIND_LINKS", "./wheels")
	mustFail(t, "pypi", "x==1.0", "PIP_FIND_LINKS")
	t.Setenv("PIP_FIND_LINKS", "")
	for _, off := range []string{"0", "false", "No", "off"} {
		t.Setenv("PIP_NO_INDEX", off)
		mustResolve(t, "pypi", "x==1.0")
	}
	for _, on := range []string{"1", "true", "YES", "on", "x"} {
		t.Setenv("PIP_NO_INDEX", on)
		mustFail(t, "pypi", "x==1.0", "PIP_NO_INDEX")
	}
}

// TestNpmRegistryFromEnvironment: npm reads a registry per scope and the
// registry itself from the environment; one that differs fails.
func TestNpmRegistryFromEnvironment(t *testing.T) {
	f := newFake(t)
	f.set("/left", `{"versions":{"1.0.0":{"dist":{"integrity":"sha512-a"}}}}`)
	f.set("/@acme%2fleft", `{"versions":{"1.0.0":{"dist":{"integrity":"sha512-a"}}}}`)
	t.Setenv("npm_config_registry", f.url)
	for _, k := range []string{"npm_config_@acme:registry", "NPM_CONFIG_@ACME:REGISTRY"} {
		t.Setenv(k, f.url+"/")
		mustResolve(t, "npm", "@acme/left@1.0.0")
		t.Setenv(k, "https://evil.example/")
		mustFail(t, "npm", "@acme/left@1.0.0", k+" is https://evil.example")
		mustResolve(t, "npm", "left@1.0.0") // another scope
		_ = os.Unsetenv(k)
	}
	t.Setenv("npm_config_@acme:registry", f.url)
	t.Setenv("NPM_CONFIG_@ACME:REGISTRY", "https://evil.example/")
	mustFail(t, "npm", "@acme/left@1.0.0", "set one")
	_ = os.Unsetenv("npm_config_@acme:registry")
	_ = os.Unsetenv("NPM_CONFIG_@ACME:REGISTRY")
	t.Setenv("NPM_CONFIG_REGISTRY", f.url+"/")
	mustResolve(t, "npm", "left@1.0.0")
	t.Setenv("NPM_CONFIG_REGISTRY", "https://evil.example")
	mustFail(t, "npm", "left@1.0.0", "NPM_CONFIG_REGISTRY")
	_ = os.Unsetenv("NPM_CONFIG_REGISTRY")
	_ = os.Unsetenv("npm_config_registry")

	// npm reads npm_config_* in any letter case (@npmcli/config).
	t.Setenv("Npm_Config_Registry", f.url)
	mustResolve(t, "npm", "left@1.0.0") // resolved against that registry
	t.Setenv("Npm_Config_Registry", "https://evil.example.invalid")
	mustFail(t, "npm", "left@1.0.0", "evil.example.invalid")
	t.Setenv("Npm_Config_Registry", f.url)
	t.Setenv("npm_config_REGISTRY", "https://evil.example.invalid")
	mustFail(t, "npm", "left@1.0.0", "set one")
	_ = os.Unsetenv("npm_config_REGISTRY")
	// The userconfig from any spelling is the .npmrc checked.
	npmrc := filepath.Join(t.TempDir(), "npmrc")
	if err := os.WriteFile(npmrc, []byte("@acme:registry=https://evil.example/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NPM_CONFIG_USERCONFIG", "")
	t.Setenv("npm_config_userconfig", npmrc)
	mustFail(t, "npm", "@acme/left@1.0.0", "@acme:registry")
	t.Setenv("npm_config_UserConfig", os.DevNull)
	mustFail(t, "npm", "left@1.0.0", "set one")
	if tok := npmToken(strings.TrimPrefix(f.url, "http://")); tok != "" {
		t.Fatalf("conflicting userconfig spellings sent token %q", tok)
	}
}

// TestUserAgent: Maven Central throttles Go's default User-Agent.
func TestUserAgent(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ua = r.UserAgent() }))
	t.Cleanup(srv.Close)
	Version = "1.2.3"
	t.Cleanup(func() { Version = "dev" })
	if err := get(context.Background(), srv.URL, "", 1, io.Discard); err != nil || ua != "aval/1.2.3 (+https://github.com/TrianaLab/aval)" {
		t.Fatalf("User-Agent %q, %v", ua, err)
	}
}

func TestRedirectToAnotherHostDropsCredentials(t *testing.T) {
	from := httptest.NewRequest(http.MethodGet, "https://a.example/x", nil)
	for u, kept := range map[string]bool{"https://a.example/y": true, "https://b.example/y": false, "https://a.example:8443/y": false, "https://sub.a.example/y": false} {
		req := httptest.NewRequest(http.MethodGet, u, nil)
		req.Header.Set("Authorization", "Basic x")
		if err := client.CheckRedirect(req, []*http.Request{from}); err != nil || (req.Header.Get("Authorization") != "") != kept {
			t.Errorf("redirect to %s: credentials kept = %v, want %v (%v)", u, !kept, kept, err)
		}
	}
}

func TestAllUnknownKind(t *testing.T) {
	mustFail(t, "nope", "x", `unknown kind "nope"`)
}

// TestPipEnvSpellings: pip reads any PIP_ variable with the rest of the
// name in any case and - equal to _ (pip 25: PIP_index_url and
// PIP_INDEX-URL both set the index); pip_ in lower case is not read.
func TestPipEnvSpellings(t *testing.T) {
	f := newFake(t)
	t.Setenv("AVAL_PYPI_URL", f.url)
	f.set("/pypi/x/json", `{"releases":{"1.0":[{"filename":"x-1.0.tar.gz","digests":{"sha256":"aa"}}]}}`)
	for k, v := range map[string]string{
		"PIP_index_url": "https://evil.example/simple", "PIP_INDEX-URL": "https://evil.example/simple",
		"PIP_Extra-Index_URL": "https://evil.example/simple", "PIP_find-links": "./wheels", "PIP_no_index": "1",
		"PIP_FIND_LINKS": "/", "PIP_EXTRA_INDEX_URL": "/",
		"PIP___INDEX_URL": "https://evil.example/simple", "PIP_--find-links": "./wheels",
	} {
		t.Setenv(k, v)
		mustFail(t, "pypi", "x==1.0", k)
		_ = os.Unsetenv(k)
	}
	t.Setenv("PIP_index_url", f.url)
	mustResolve(t, "pypi", "x==1.0")
	t.Setenv("PIP_INDEX_URL", "https://evil.example/simple")
	mustFail(t, "pypi", "x==1.0", "set one")
	_ = os.Unsetenv("PIP_INDEX_URL")
	_ = os.Unsetenv("PIP_index_url")
	t.Setenv("pip_INDEX_URL", "https://ignored.example/simple")
	mustResolve(t, "pypi", "x==1.0")
}

// TestNpmrcIni: ~/.npmrc is read as npm reads it (the ini package): quotes
// stripped from keys and values, ; and # comments, keys under a [section]
// ignored. A line Aval cannot read fails closed.
func TestNpmrcIni(t *testing.T) {
	npmrc := filepath.Join(t.TempDir(), "npmrc")
	t.Setenv("NPM_CONFIG_USERCONFIG", npmrc)
	t.Setenv("K", "registry")
	t.Setenv("TOK", "secret")
	const base = "https://registry.npmjs.org"
	for rc, wantErr := range map[string]string{
		`"registry"=http://evil/`:                                "registry",
		"registry=http://evil/\n[x]\n":                           "registry",
		"registry='http://evil/'":                                "registry",
		"[x]\nregistry=http://evil/":                             "",
		"registry=" + base + "/\n[x]\nregistry=http://evil/":     "",
		`registry="` + base + `/"`:                               "",
		"registry=" + base + "/ ; comment":                       "",
		"registry=" + base + "/ # comment":                       "",
		"# c\r\n  ; c\r\n\r\nregistry=" + base:                   "",
		"registry=http://evil/ ; " + base:                        "registry",
		"registry\n":                                             "cannot read",
		"garbage line\n":                                         "cannot read",
		"=x\n":                                                   "cannot read",
		"registry[]=" + base:                                     "cannot read",
		`"@acme:registry"="http://evil/"`:                        "@acme:registry",
		"[x]\n@acme:registry=http://evil/":                       "",
		"[x]\njunk line":                                         "",
		"registry=http://evil/\n[x]\nregistry=" + base:           "registry",
		"registry=" + base + "/\n[x]\n[registry.a]\nx=1":         "cannot read",
		"registry=" + base + `/\;x`:                              "registry",
		"registry=" + base + `/\#x`:                              "registry",
		`registry=` + base + `/\x`:                               "registry",
		"registry=http://evil/\nregistry=" + base + "/\u2028":    "cannot read",
		"registry=http://evil/\nregistry=" + base + "/\u2029":    "cannot read",
		"registry=https://${x@evil.example/}registry.npmjs.org/": "registry",
		"${K}=http://evil/":                                      "registry",
		"registry=${UNSET_AVAL_X?}" + base:                       "",
		`registry=\${K}`:                                         "registry",
		`'["registry"]'=http://evil/`:                            "cannot read",
		`'["@acme:registry"]'=http://evil/`:                      "cannot read",
		`registry='["http://evil/"]'`:                            "cannot read",
	} {
		if err := os.WriteFile(npmrc, []byte(rc), 0o600); err != nil {
			t.Fatal(err)
		}
		err := npmRegistry("@acme/left", base)
		if wantErr == "" && err != nil || wantErr != "" && (err == nil || !strings.Contains(err.Error(), wantErr)) {
			t.Errorf(".npmrc %q: %v, want %q", rc, err, wantErr)
		}
	}
	for rc, want := range map[string]string{
		`"//h.example/:_authToken"="t"`:          "t",
		"//h.example/:_authToken='t' ; c":        "'t'", // not quoted as a whole: npm keeps the quotes
		"[x]\n//h.example/:_authToken=t":         "",
		`//h.example/:_authToken=a\;b\#c\\d\xe\`: `a;b#c\d\xe\`,
		"junk\n//h.example/:_authToken=t":        "",
		"//h.example/:_authToken=${TOK}":         "secret",
		"//h.example/:_authToken=${NOPE_X}":      "${NOPE_X}",
	} {
		if err := os.WriteFile(npmrc, []byte(rc), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := npmToken("h.example/pkg"); got != want {
			t.Errorf(".npmrc %q: token %q, want %q", rc, got, want)
		}
	}
}

// TestMavenLatestReleaseListed: <latest> and <release> name what Maven
// installs for LATEST and RELEASE: a SNAPSHOT or a version outside
// <versions> (whose content is not bound) fails.
func TestMavenLatestReleaseListed(t *testing.T) {
	f := newFake(t)
	t.Setenv("AVAL_MAVEN_URL", f.url)
	f.set("/x/a/1.0/a-1.0.pom", "<project><packaging>pom</packaging></project>")
	for meta, why := range map[string]string{
		"<latest>2.0-SNAPSHOT</latest><release>1.0</release>": "SNAPSHOT",
		"<latest>1.0</latest><release>1.1-snapshot</release>": "SNAPSHOT",
		"<latest>2.0</latest><release>1.0</release>":          "not in <versions>",
		"<latest>1.0</latest><release>2.0</release>":          "not in <versions>",
		"<latest>1.0</latest><release>1.0</release>":          "",
		"<release>1.0</release>":                              "",
	} {
		f.set("/x/a/maven-metadata.xml", "<metadata><versioning>"+meta+"<versions><version>1.0</version></versions></versioning></metadata>")
		if why == "" {
			mustResolve(t, "maven", "x:a:LATEST")
			continue
		}
		mustFail(t, "maven", "x:a:LATEST", why)
	}
}

// TestTokenInRegistryPathMasked: a registry URL may carry its token in the
// path (Gemfury: https://npm-proxy.fury.io/<TOKEN>/user/); errors never
// show it.
func TestTokenInRegistryPathMasked(t *testing.T) {
	f := newFake(t)
	t.Setenv("npm_config_registry", f.url+"/T0KEN/user/")
	t.Setenv("AVAL_PYPI_URL", f.url+"/T0KEN/user")
	t.Setenv("AVAL_MAVEN_URL", f.url+"/T0KEN/user")
	for kind, ref := range map[string]string{"npm": "left-pad@1.0.0", "pypi": "x==1.0", "maven": "x:a:1.0"} {
		if _, err := resolveOne(t, kind, ref); err == nil || strings.Contains(err.Error(), "T0KEN") {
			t.Errorf("%s error must not show the token: %v", kind, err)
		}
	}
	t.Setenv("PIP_INDEX_URL", "https://npm-proxy.fury.io/T0KEN/user/")
	if _, err := resolveOne(t, "pypi", "x==1.0"); err == nil || strings.Contains(err.Error(), "T0KEN") || !strings.Contains(err.Error(), "npm-proxy.fury.io") {
		t.Errorf("PIP_INDEX_URL error must name the host, not the token: %v", err)
	}
}
