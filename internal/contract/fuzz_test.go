package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Crashers found by `make fuzz` land in testdata/fuzz/<Target>/ and run as
// regression cases on every `go test`; commit them with the fix.

// FuzzParse: Parse never panics, and an accepted contract is valid and has
// only globs that ValidateGlob accepts.
func FuzzParse(f *testing.F) {
	f.Add([]byte(valid))
	for _, file := range []string{"../../aval.yaml", "../../examples/go-compose/aval.yaml"} {
		if b, err := os.ReadFile(filepath.FromSlash(file)); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(strings.Replace(valid, `"compose.yaml"`, `"../secrets"`, 1)))
	f.Add([]byte(valid + "\n---\nfoo: bar\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := Parse(data)
		if err != nil {
			return
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("Parse accepted an invalid contract: %v", err)
		}
		for _, g := range append(append(append([]string{}, c.Scope.Source...), c.Scope.Exclude...), c.Scope.Dependencies...) {
			if err := ValidateGlob(g); err != nil {
				t.Fatalf("Parse accepted glob %q: %v", g, err)
			}
		}
	})
}

// FuzzMatch: for any glob ValidateGlob accepts, Match never panics and never
// matches a name outside the contract root; a glob without metacharacters
// matches exactly itself.
func FuzzMatch(f *testing.F) {
	for _, s := range [][2]string{
		{"**/*.go", "a/b/c.go"}, {"testdata/**", "testdata/x/y.json"}, {"cmd/*/main.go", "cmd/aval/main.go"},
		{"a/**/z", "a/z"}, {"**", "../secrets"}, {"*/x", "../x"}, {"**", "/etc/passwd"}, {"go.[sum", "go.sum"},
		{"a/b", "a/b"}, {"a/b", "a/b/c"}, {"[a-c]?/\\*", "b1/*"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, glob, name string) {
		// ponytail: `**` backtracking is exponential in the number of `**`
		// segments; real contracts use one or two, so cap the fuzzer there.
		if ValidateGlob(glob) != nil || strings.Count(glob, "**") > 3 || len(name) > 256 {
			return
		}
		got := Match(glob, name)
		if got && (strings.HasPrefix(name, "/") || strings.Contains("/"+name+"/", "/../")) {
			t.Fatalf("Match(%q, %q) matched a path outside the root", glob, name)
		}
		if !strings.ContainsAny(glob, `*?[\`) && got != (glob == name) {
			t.Fatalf("literal Match(%q, %q) = %v", glob, name, got)
		}
	})
}
