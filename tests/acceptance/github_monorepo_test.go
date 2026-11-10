package acceptance

import (
	"io"
	"log"
	"maps"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"

	"github.com/TrianaLab/aval/internal/policy"
)

// monorepo is examples/github-monorepo copied into a temporary directory,
// with an in-process registry, a signing key and a policy that trusts it.
type monorepo struct {
	dir, repo, key, policy string
	names                  []string
}

// aval runs aval in the copy with the registry.
func (m monorepo) aval(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return avalIn(t, m.dir, append(args, "--registry", m.repo)...)
}

// verify fails unless aval verify of each test exits as want says.
func (m monorepo) verify(t *testing.T, step string, want map[string]int) {
	t.Helper()
	for _, n := range slices.Sorted(maps.Keys(want)) {
		if out, errOut, code := m.aval(t, "verify", "--test", n, "--policy", m.policy); code != want[n] {
			t.Errorf("%s: verify %s: exit %d, want %d\n%s\n%s", step, n, code, want[n], out, errOut)
		}
	}
}

// edit appends text to the file rel and returns a func that restores it.
func (m monorepo) edit(t *testing.T, rel, text string) func() {
	t.Helper()
	p := filepath.Join(m.dir, rel)
	old, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(slices.Clone(old), text...), 0o644); err != nil {
		t.Fatal(err)
	}
	return func() {
		var err error
		if old == nil {
			err = os.Remove(p)
		} else {
			err = os.WriteFile(p, old, 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

// tests lists the contract's tests with aval explain --all and fails when
// two share a fingerprint: two shards never do.
func (m *monorepo) tests(t *testing.T) {
	t.Helper()
	stdout, _, code := avalIn(t, m.dir, "explain", "--all")
	fps := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		n, fp, _ := strings.Cut(l, " ")
		if prev, ok := fps[fp]; ok {
			t.Errorf("%s and %s share fingerprint %s", prev, n, fp)
		}
		fps[fp] = n
		m.names = append(m.names, n)
	}
	if want := []string{"unit", "ui", "e2e-1", "e2e-2", "e2e-3"}; code != 0 || !slices.Equal(m.names, want) {
		t.Fatalf("explain --all: exit %d, tests %q, want %q", code, m.names, want)
	}
}

// publishAndReuse runs every test with --push (a miss), then again (a hit
// per test, each shard on its own), and checks that unit's reuse restores
// coverage.out.
func (m monorepo) publishAndReuse(t *testing.T) {
	t.Helper()
	for _, n := range m.names {
		if out, errOut, code := m.aval(t, "run", "--test", n, "--push", "--key", m.key); code != 0 || !strings.Contains(out, "miss "+n) || !strings.Contains(errOut+out, "pushed "+m.repo) {
			t.Fatalf("first run of %s: exit %d\n%s\n%s", n, code, out, errOut)
		}
	}
	cov := filepath.Join(m.dir, "coverage.out")
	want, err := os.ReadFile(cov)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cov); err != nil {
		t.Fatal(err)
	}
	for _, n := range m.names {
		if out, errOut, code := m.aval(t, "run", "--test", n, "--policy", m.policy); code != 0 || !strings.Contains(out, "reused passed result") {
			t.Fatalf("second run of %s: exit %d\n%s\n%s", n, code, out, errOut)
		}
	}
	if got, err := os.ReadFile(cov); err != nil || string(got) != string(want) {
		t.Fatalf("unit reuse did not restore coverage.out: %v", err)
	}
}

// want is exit code 0 for every test, 4 for those in miss.
func (m monorepo) want(miss ...string) map[string]int {
	w := map[string]int{}
	for _, n := range m.names {
		w[n] = 0
	}
	for _, n := range miss {
		w[n] = 4
	}
	return w
}

var shards = []string{"e2e-1", "e2e-2", "e2e-3"}

// Story 14.20 (#217): the monorepo example, integrated in 5 files, through
// the CLI against an in-process registry. Every test misses and is pushed,
// then each one, every matrix shard on its own, is reused; an edit in one
// fragment misses exactly the tests that use it; a mutable reference
// outside allowMutable[].files blocks reuse; a tampered attestation exits
// 5; the external-dependencies suite is not a test of the contract. It runs
// the example's real suites, so it fails without npm instead of skipping.
func TestGitHubMonorepoStory(t *testing.T) {
	for _, tool := range []string{"npm", "make"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required: %v", tool, err)
		}
	}
	src := filepath.Join("..", "..", "examples", "github-monorepo")
	// The policy the actions fetch from the default branch: valid, with
	// the registry results go to.
	if p, err := policy.Load(filepath.Join(src, ".github", "aval", "policy.yaml")); err != nil || p.Registry == "" {
		t.Fatalf("example policy: %v", err)
	}
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	keys := t.TempDir()
	key, pub := keyPair(t, keys, "ci")
	m := &monorepo{
		dir: copyExample(t, src), repo: strings.TrimPrefix(srv.URL, "http://") + "/acme/monorepo-aval",
		key: key, policy: writePolicy(t, keys, "policy.yaml", pub),
	}
	m.tests(t)

	// The external-dependencies suite stays a plain make job: no test of
	// the contract runs it, so it is never looked up nor reused.
	for _, n := range m.names {
		if out, _, _ := avalIn(t, m.dir, "explain", "--test", n, "--expand"); strings.Contains(out, "test-external") || !strings.Contains(out, "kind: Test") {
			t.Errorf("%s runs test-external or does not expand:\n%s", n, out)
		}
	}
	if _, errOut, code := m.aval(t, "run", "--test", "external"); code != 2 {
		t.Errorf("run --test external: exit %d, want 2\n%s", code, errOut)
	}

	m.publishAndReuse(t)
	for _, tc := range []struct {
		fragment, file string
		miss           []string
	}{
		{"go", "pkg/version/version.go", append([]string{"unit"}, shards...)},
		{"ui", "ui/src/format.js", []string{"ui"}},
		{"image", "Dockerfile", shards},
		{"image", "data/fixtures/podinfo.yaml", shards},
	} {
		restore := m.edit(t, tc.file, "\n")
		m.verify(t, "edit in fragment "+tc.fragment+" ("+tc.file+")", m.want(tc.miss...))
		restore()
	}
	m.verify(t, "edits reverted", m.want())

	// False hit: the accepted reference in a file outside data/fixtures/*.yaml
	// is a new, unaccepted mutable reference: nothing is published, so every
	// shard stays a miss. The control, a new file in data/ without one, is
	// published and reused.
	for _, tc := range []struct {
		content string
		e2e2    int
	}{{"replicas: 2\n", 0}, {"image: ghcr.io/stefanprodan/podinfo:6.5.0\n", 4}} {
		restore := m.edit(t, "data/extra.yaml", tc.content)
		out, errOut, _ := m.aval(t, "run", "--test", "e2e-2", "--push", "--key", m.key)
		w := m.want(shards...)
		w["e2e-2"] = tc.e2e2
		m.verify(t, "data/extra.yaml "+strings.TrimSpace(tc.content)+"\n"+out+errOut, w)
		restore()
	}

	tamper(t, m.repo, m.dir, func() {}, "--test", "e2e-3")
	if out, errOut, code := m.aval(t, "run", "--test", "e2e-3", "--policy", m.policy); code != 5 || strings.Contains(out, "reused") {
		t.Fatalf("tampered e2e-3: exit %d, want 5\n%s\n%s", code, out, errOut)
	}
	m.verify(t, "a tampered shard leaves the other shards reusable", map[string]int{"e2e-1": 0, "e2e-2": 0, "e2e-3": 5})
}
