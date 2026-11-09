package contract

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// yaml.v3 reports problems in Go terms: "field sources not found in type
// contract.Scope", "cannot unmarshal !!str `x` into []string". typeErrors
// says them in YAML terms instead: the path of the field (scope.sources),
// what was found and what is wanted, and the closest field name for an
// unknown one. One error per problem, joined; the text keeps the user's
// value, so callers still escape each line.
func typeErrors(data []byte, te *yaml.TypeError) error {
	var doc yaml.Node
	_ = yaml.Unmarshal(data, &doc) // ponytail: it just decoded into a struct, so it parses
	var nodes []spot
	walk(&doc, "", &nodes)
	errs := make([]error, len(te.Errors))
	for i, e := range te.Errors {
		errs[i] = errors.New(yamlPath(e, nodes))
	}
	return errors.Join(errs...)
}

// spot is one node of the document: a mapping key or a value, with its line
// and its path (scope.source[0]).
type spot struct {
	line       int
	key        bool
	tag, value string
	path       string
}

func walk(n *yaml.Node, path string, out *[]spot) {
	if n.Kind != yaml.DocumentNode {
		*out = append(*out, spot{line: n.Line, tag: n.ShortTag(), value: n.Value, path: path})
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			p := k.Value
			if path != "" {
				p = path + "." + k.Value
			}
			*out = append(*out, spot{line: k.Line, key: true, value: k.Value, path: p})
			walk(n.Content[i+1], p, out)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			walk(c, fmt.Sprintf("%s[%d]", path, i), out)
		}
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walk(c, path, out)
		}
	}
}

var (
	unknownFieldRe = regexp.MustCompile(`^line ([0-9]+): field (.*) not found in type (\S+)$`)
	wrongTypeRe    = regexp.MustCompile("^line ([0-9]+): cannot unmarshal (!!\\w+)( `.*`)? into (.+)$")
)

// yamlPath rewrites one yaml.v3 message. A message it does not know stays
// as it is.
func yamlPath(msg string, nodes []spot) string {
	// The node is the first on the line that fits: a key named like the
	// field, or a value with the tag (and the start of the value) yaml quotes.
	find := func(line string, fits func(spot) bool) string {
		for _, n := range nodes {
			if fmt.Sprint(n.line) == line && fits(n) {
				return n.path + ": "
			}
		}
		return ""
	}
	if m := unknownFieldRe.FindStringSubmatch(msg); m != nil {
		what := "unknown field " + m[2]
		if at := find(m[1], func(n spot) bool { return n.key && n.value == m[2] }); at != "" {
			what = at + "unknown field" // the path ends in the name
		}
		return fmt.Sprintf("line %s: %s; %s", m[1], what, suggest(m[2], m[3]))
	}
	if m := wrongTypeRe.FindStringSubmatch(msg); m != nil {
		quoted := strings.TrimSuffix(strings.Trim(strings.TrimSpace(m[3]), "`"), "...")
		at := find(m[1], func(n spot) bool { return !n.key && n.tag == m[2] && strings.HasPrefix(n.value, quoted) })
		return fmt.Sprintf("line %s: %sgot %s%s, want %s", m[1], at, kindOf(m[2]), m[3], want(m[4]))
	}
	return msg
}

// fieldsOf maps the Go type names yaml.v3 prints to their types.
var fieldsOf = map[string]reflect.Type{}

func init() {
	for _, v := range []any{Contract{}, Metadata{}, Runner{}, Scope{}, Environment{}, AllowMutable{}} {
		fieldsOf[reflect.TypeOf(v).String()] = reflect.TypeOf(v)
	}
}

// suggest names the field of goType closest to name, or lists them all.
func suggest(name, goType string) string {
	t, ok := fieldsOf[goType]
	if !ok {
		return "check the spelling"
	}
	var fields []string
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		fields = append(fields, tag)
	}
	best, dist := "", max(1, len(name)/3)+1
	for _, f := range fields {
		if d := levenshtein(strings.ToLower(name), strings.ToLower(f)); d < dist {
			best, dist = f, d
		}
	}
	if best != "" {
		return "did you mean " + best + "?"
	}
	return "known fields: " + strings.Join(fields, ", ")
}

// kindOf names a YAML tag.
func kindOf(tag string) string {
	names := map[string]string{"!!str": "a string", "!!int": "a number", "!!float": "a number", "!!bool": "a boolean", "!!seq": "a list", "!!map": "a mapping", "!!null": "nothing"}
	if n, ok := names[tag]; ok {
		return n
	}
	return tag
}

// want names what a Go type takes in YAML.
func want(goType string) string {
	names := map[string]string{"string": "a string", "contract.Cacheability": "a string", "[]string": "a list of strings", "map[string]string": "a mapping of strings", "map[string][]string": "a mapping of lists"}
	if n, ok := names[goType]; ok {
		return n
	}
	if strings.HasPrefix(goType, "[]") {
		return "a list of mappings"
	}
	if _, ok := fieldsOf[goType]; ok {
		return "a mapping"
	}
	return goType
}

// levenshtein is the edit distance between a and b.
// ponytail: same as levenshtein in cmd/aval/help.go; share it if a third copy appears.
func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := []int{i}
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur = append(cur, min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost))
		}
		prev = cur
	}
	return prev[len(b)]
}
