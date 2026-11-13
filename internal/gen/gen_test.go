package gen

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fake is an Adapter that returns its units, or err.
type fake struct {
	units []Unit
	err   error
}

func (f fake) Units(string) ([]Unit, error) { return f.units, f.err }

var _ Adapter = fake{}

func unit(id string, deps ...string) Unit {
	return Unit{ID: id, Source: []string{id + "/*.go"}, Deps: deps, Command: []string{"go", "test", "./" + id}}
}

// withTest returns u with TestSource ts.
func withTest(u Unit, ts ...string) Unit {
	u.TestSource = ts
	return u
}

func TestName(t *testing.T) {
	for id, want := range map[string]string{
		"internal/contract": "go.internal.contract",
		"cmd/aval":          "go.cmd.aval",
		"@Scope/My Pkg":     "go.scope.my-pkg",
		"a__b--c..d":        "go.a__b--c..d",
		"com.acme:core":     "go.com.acme-core",
		"./x/":              "go.x",
	} {
		got, err := Name("go", id)
		if err != nil || got != want {
			t.Errorf("Name(%q) = %q, %v; want %q", id, got, err, want)
		}
	}
	for _, id := range []string{"", "///", "@@"} {
		if _, err := Name("go", id); err == nil {
			t.Errorf("Name(%q): want an error", id)
		}
	}
	if _, err := Name("Go", "a"); err == nil {
		t.Error(`Name("Go", "a"): want an error`)
	}
	long := strings.Repeat("abcdefghij/", 8)
	a, err := Name("go", long)
	b, _ := Name("go", long+"x")
	if err != nil || len(a) > 64 || a == b || a[:55] != b[:55] {
		t.Errorf("long names: %q, %q, %v: want <= 64 chars, a shared stem and different hashes", a, b, err)
	}
	if again, _ := Name("go", long); again != a {
		t.Errorf("long name not stable: %q then %q", a, again)
	}
}

func TestPlanErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		lang  string
		units []Unit
		want  string
		isErr bool
	}{
		"bad language":                      {"Go", []Unit{unit("a")}, "language", false},
		"no units":                          {"go", nil, "no units found", true},
		"duplicate id":                      {"go", []Unit{unit("a"), unit("a")}, `"a": listed twice`, true},
		"empty name":                        {"go", []Unit{unit("@@")}, "empty name", true},
		"name collision":                    {"go", []Unit{unit("a/b"), unit("A/B")}, "both get the name go.a.b", true},
		"no source":                         {"go", []Unit{{ID: "a", Command: []string{"x"}}}, "no source files", true},
		"no command":                        {"go", []Unit{{ID: "a", Source: []string{"a/**"}}}, "no test command", true},
		"unknown dependency":                {"go", []Unit{unit("a", "gone")}, `unknown dependency "gone"`, true},
		"test fragment collision":           {"go", []Unit{withTest(unit("a"), "x"), unit("a/test")}, `units "a" and "a/test" both get the name go.a.test`, true},
		"test fragment collision, reversed": {"go", []Unit{unit("a/test"), withTest(unit("a"), "x")}, `units "a/test" and "a" both get the name go.a.test`, true},
		"inexpressible output":              {"go", []Unit{withOutputs(unit("a"), "../a.out")}, `"../a.out": a contract glob cannot express`, true},
	} {
		_, err := plan(tc.lang, tc.units)
		if err == nil || !strings.Contains(err.Error(), tc.want) || errors.Is(err, ErrUnits) != tc.isErr {
			t.Errorf("%s: got %v; want %q (ErrUnits %v)", name, err, tc.want, tc.isErr)
		}
	}
}

func withOutputs(u Unit, outputs ...string) Unit {
	u.Outputs = outputs
	return u
}

func TestPlanClosure(t *testing.T) {
	// a -> b -> c, c -> b (a cycle), d alone, e -> e.
	p, err := plan("go", []Unit{unit("d"), unit("a", "b"), unit("b", "c"), unit("c", "b"), unit("e", "e")})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, u := range p {
		got[u.name] = u.use
	}
	want := map[string][]string{
		"go.a": {"go.a", "go.b", "go.c"},
		"go.b": {"go.b", "go.c"},
		"go.c": {"go.b", "go.c"},
		"go.d": {"go.d"},
		"go.e": {"go.e"},
	}
	for k, v := range want {
		if !slices.Equal(got[k], v) {
			t.Errorf("%s uses %v, want %v", k, got[k], v)
		}
	}
	if p[0].name != "go.a" || p[4].name != "go.e" {
		t.Errorf("not sorted by name: %v", p)
	}
}

func TestRoot(t *testing.T) {
	set := func(defaults string) []byte {
		return []byte("apiVersion: aval.trianalab.io/v1\nkind: TestSet\n" + defaults)
	}
	dir := filepath.Join("r", "sub")
	for in, want := range map[string]string{
		"":                                       dir,
		"defaults: {cacheability: x}\n":          dir,
		"defaults: {scope: {root: ..}}\n":        "r",
		"defaults:\n  scope:\n    root: ../..\n": ".",
	} {
		if got, err := Root(set(in), dir); err != nil || got != want {
			t.Errorf("Root(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Root(set("defaults: {scope: {root: ../x}}\n"), "/r"); err == nil {
		t.Error("root ../x: want an error")
	}
	if _, err := Root([]byte("kind: Test\n"), "/r"); err == nil {
		t.Error("kind: Test: want an error")
	}
}

// TestCanonical resolves the symlinks of a relative root, the working
// directory's included.
func TestCanonical(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(real, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(link)
	if got, err := Canonical("ws"); err != nil || got != filepath.Join(real, "ws") {
		t.Errorf("Canonical(ws) = %q, %v; want %q", got, err, filepath.Join(real, "ws"))
	}
	if _, err := Canonical("missing"); err == nil {
		t.Error("Canonical(missing): want an error")
	}
}
