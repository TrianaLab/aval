package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	// trustField is a struct field that would let a parsed file (aval.yaml or
	// anything else in the tree) name the registry or the trust policy.
	trustField = regexp.MustCompile(`(?i)^(?:registry|policy|policyfile|trustpolicy)$`)
	// discoveredConfig is a config file aval would find in the tree.
	discoveredConfig = regexp.MustCompile(`(?i)\.avalrc|\baval\.(?:json|toml|ini|conf|config|env)\b|\.aval\.ya?ml\b|\baval\.config\.`)
	trustEnvName     = regexp.MustCompile(`^AVAL_(?:REGISTRY|POLICY)$`)
)

// envResolver is the one file that may read AVAL_REGISTRY and AVAL_POLICY.
const envResolver = "cmd/aval/config.go"

// trustSourceProblems: Go code under dir (tests excluded) reads the registry
// or the trust policy from something other than a flag or the environment
// resolver: a yaml/json field named for them (the contract, or any file it
// parses), a config file discovered in the tree, or the variables outside
// envResolver. rel names files relative to the repository.
func trustSourceProblems(t *testing.T, dir string, rel func(string) string) []string {
	t.Helper()
	var problems []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			problems = append(problems, nodeProblems(rel(p), n)...)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return problems
}

// The tree under test controls aval.yaml and every file in it; PR CI takes
// trust from the base branch (#137). So the registry and the policy come
// only from flags or AVAL_REGISTRY / AVAL_POLICY (#163).
func TestTrustSource(t *testing.T) {
	for _, dir := range []string{"cmd", "internal"} {
		for _, p := range trustSourceProblems(t, filepath.Join(root, dir), func(p string) string {
			r, _ := filepath.Rel(root, p)
			return filepath.ToSlash(r)
		}) {
			t.Error(p)
		}
	}
}

func TestTrustSourceCheckCatchesFixtures(t *testing.T) {
	for _, fixture := range []string{"contract", "configfile", "env"} {
		dir := filepath.Join("testdata", "trustsource", fixture)
		if p := trustSourceProblems(t, dir, filepath.ToSlash); len(p) == 0 {
			t.Errorf("%s: no problem reported", fixture)
		}
	}
}

// nodeProblems checks one syntax node of the file name.
func nodeProblems(name string, n ast.Node) []string {
	var problems []string
	switch n := n.(type) {
	case *ast.Field:
		if n.Tag == nil {
			break
		}
		tag, _ := strconv.Unquote(n.Tag.Value)
		for _, key := range []string{"yaml", "json"} {
			if v, _, _ := strings.Cut(reflect.StructTag(tag).Get(key), ","); trustField.MatchString(v) {
				problems = append(problems, name+": field tagged "+key+":"+strconv.Quote(v)+" reads the registry or policy from a file")
			}
		}
	case *ast.BasicLit:
		if n.Kind != token.STRING {
			break
		}
		s, _ := strconv.Unquote(n.Value)
		if discoveredConfig.MatchString(s) {
			problems = append(problems, name+": names a config file discovered in the tree: "+s)
		}
		if trustEnvName.MatchString(s) && name != envResolver {
			problems = append(problems, name+": reads "+s+" outside "+envResolver)
		}
	}
	return problems
}
