package gen

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/TrianaLab/aval/internal/contract"
)

// Rewrite returns data, a kind: TestSet, with the entries lang owns (the
// fragments and tests named "<lang>.*") replaced by those generated from
// units. Every other line is kept byte for byte, comments included; the
// generated entries take the place of the first owned one, or go at the end
// of fragments and tests. The result must be a valid TestSet, so a
// hand-written test that uses the fragment of a unit that no longer exists
// is an error. A unit whose ID matches one of the contract globs in noTest
// gets no test; its fragment stays while another test uses it, so its files
// stay in that test's scope. A non-nil shared becomes the fragment
// "<lang>.module", which every generated test uses. On any error nothing is
// returned to write (invariant 3).
func Rewrite(data []byte, lang string, units []Unit, shared *Shared, noTest ...string) ([]byte, error) {
	p, err := withShared(lang, units, shared)
	if err != nil {
		return nil, err
	}
	skip, err := skipped(lang, units, noTest)
	if err != nil {
		return nil, err
	}
	root, err := parseSet(data)
	if err != nil {
		return nil, err
	}
	lines := splitLines(string(data))
	eol := "\n"
	if len(lines) > 0 {
		if t := lines[0][len(strings.TrimRight(lines[0], "\r\n")):]; t == "\r\n" || t == "\r" {
			eol = t // generated lines follow the file's line endings
		}
		if last := lines[len(lines)-1]; !strings.ContainsAny(last, breaks) {
			lines[len(lines)-1] += eol
		}
	}
	e := edit{lines: lines, drop: make([]bool, len(lines)), ins: map[int]string{}, root: root, lang: lang, eol: eol}
	frags, tests := generated(p, skip, used(lang, root, p, skip))
	if err := errors.Join(e.splice("fragments", frags), e.splice("tests", tests)); err != nil {
		return nil, err
	}
	var b strings.Builder
	for i := 0; i <= len(lines); i++ {
		b.WriteString(e.ins[i])
		if i < len(lines) && !e.drop[i] {
			b.WriteString(lines[i])
		}
	}
	b.WriteString(e.tail)
	out := []byte(b.String())
	if _, err := contract.ParseAll(out); err != nil {
		return nil, fmt.Errorf("the generated file is not a valid TestSet: %w", err)
	}
	// The line edit above relies on block layout; prove it changed only the
	// owned entries by comparing it with the same edit done on the nodes.
	want, _ := parseSet(data)
	replace(want, lang, "fragments", frags)
	replace(want, lang, "tests", tests)
	got, err := parseSet(out)
	if err != nil || canon(got) != canon(want) {
		return nil, errors.New("cannot rewrite the generated entries without changing other lines; check the layout of fragments and tests")
	}
	return out, nil
}

// breaks are the characters yaml.v3 counts as a line break; "\r\n" is one.
const breaks = "\r\n\u0085\u2028\u2029"

// splitLines splits s after every line break, the way yaml.v3 numbers
// lines, so that line n of a node is lines[n-1].
func splitLines(s string) []string {
	var lines []string
	for s != "" {
		i := strings.IndexAny(s, breaks)
		if i < 0 {
			return append(lines, s)
		}
		_, w := utf8.DecodeRuneInString(s[i:])
		if strings.HasPrefix(s[i:], "\r\n") {
			w = 2
		}
		lines, s = append(lines, s[:i+w]), s[i+w:]
	}
	return lines
}

// parseSet decodes data: one YAML document whose root is a block mapping
// with kind: TestSet.
func parseSet(data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("parse: the file must hold one YAML document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 {
		return nil, errors.New("the file must be a block mapping")
	}
	if k := value(root, "kind"); k == nil || k.Value != contract.KindTestSet {
		return nil, errors.New("aval gen writes into a kind: TestSet (start one with aval init --set)")
	}
	return root, nil
}

// value returns the value of key in the mapping n, or nil.
func value(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// testName returns metadata.name of the test node t, or "".
func testName(t *yaml.Node) string {
	if n := value(value(t, "metadata"), "name"); n != nil {
		return n.Value
	}
	return ""
}

func str(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func seq(style yaml.Style, vs ...string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Style: style}
	for _, v := range vs {
		n.Content = append(n.Content, str(v))
	}
	return n
}

func mapping(style yaml.Style, kv ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Style: style, Content: kv}
}

// used returns the fragment names that the tests of p not in skip, and the
// tests in root that lang does not own, use.
func used(lang string, root *yaml.Node, p []planned, skip map[string]bool) map[string]bool {
	use := map[string]bool{}
	for _, u := range p {
		for _, n := range u.use {
			if !skip[u.name] {
				use[n] = true
			}
		}
	}
	if ts := value(root, "tests"); ts != nil {
		for _, t := range ts.Content {
			if u := value(t, "use"); u != nil && !owned(lang, testName(t)) {
				for _, n := range u.Content {
					use[n.Value] = true
				}
			}
		}
	}
	return use
}

// generated returns the fragments (a mapping) and the tests (a sequence)
// for p, in name order: no test for the names in skip or for a shared
// fragment, and only the fragments in use (a fragment no test uses is
// invalid).
func generated(p []planned, skip, use map[string]bool) (frags, tests *yaml.Node) {
	frags, tests = mapping(0), &yaml.Node{Kind: yaml.SequenceNode}
	for _, u := range p {
		body := u.body
		if body == nil {
			body = mapping(0, str("scope"), mapping(0, str("source"), seq(0, u.source...)))
		}
		if use[u.name] {
			frags.Content = append(frags.Content, str(u.name), body)
		}
		if skip[u.name] || u.body != nil {
			continue
		}
		tests.Content = append(tests.Content, mapping(0,
			str("metadata"), mapping(yaml.FlowStyle, str("name"), str(u.name)),
			str("runner"), mapping(yaml.FlowStyle, str("command"), seq(yaml.FlowStyle, u.command...)),
			str("use"), seq(0, u.use...)))
	}
	return frags, tests
}

// withShared plans units and adds shared, when not nil, as "<lang>.module",
// and its name to the use of every unit.
func withShared(lang string, units []Unit, shared *Shared) ([]planned, error) {
	p, err := plan(lang, units)
	if err != nil || shared == nil {
		return p, err
	}
	name := lang + "." + SharedName
	for i := range p {
		if p[i].name == name {
			return nil, fmt.Errorf("%w: a unit gets the name %s, which the shared fragment owns", ErrUnits, name)
		}
		p[i].use = append(p[i].use, name)
		slices.Sort(p[i].use)
	}
	p = append(p, planned{name: name, body: shared.node()})
	slices.SortFunc(p, func(a, b planned) int { return strings.Compare(a.name, b.name) })
	return p, nil
}

// node returns the body of the shared fragment.
// ponytail: every key is written; Go fills them all. Skip empty ones when an
// adapter has none.
func (s *Shared) node() *yaml.Node {
	tool, probe := mapping(yaml.FlowStyle), mapping(0)
	for _, k := range slices.Sorted(maps.Keys(s.Toolchain)) {
		tool.Content = append(tool.Content, str(k), str(s.Toolchain[k]))
	}
	for _, k := range slices.Sorted(maps.Keys(s.Probe)) {
		probe.Content = append(probe.Content, str(k), seq(yaml.FlowStyle, s.Probe[k]...))
	}
	return mapping(0,
		str("scope"), mapping(0, str("dependencies"), seq(0, slices.Sorted(slices.Values(s.Dependencies))...)),
		str("environment"), mapping(0,
			str("toolchain"), tool,
			str("probe"), probe,
			str("env"), seq(0, slices.Sorted(slices.Values(s.Env))...)))
}

// edit collects line edits to the file: lines to drop, text to insert
// before a line index (len(lines) appends), and new keys to append.
type edit struct {
	lines []string
	drop  []bool
	ins   map[int]string
	tail  string // new top-level keys, after everything else
	root  *yaml.Node
	lang  string
	eol   string // the file's line ending: "\n", "\r\n" or "\r"
}

// encode renders n like encode, with the file's line endings; an empty
// collection (every unit under --no-test) renders as nothing.
func (e *edit) encode(n *yaml.Node, prefix string) string {
	if len(n.Content) == 0 {
		return ""
	}
	return strings.ReplaceAll(encode(n, prefix), "\n", e.eol)
}

// isGap: a blank or comment-only line, which belongs to the entry after it.
func isGap(l string) bool {
	t := strings.TrimSpace(l)
	return t == "" || strings.HasPrefix(t, "#")
}

// splice drops the owned entries of the top-level collection key and
// inserts gen in place of the first one, or after the last entry.
func (e *edit) splice(key string, gen *yaml.Node) error {
	indent := strings.Repeat(" ", e.root.Content[0].Column-1)
	j := e.find(key)
	if j < 0 {
		e.add(key, indent, gen)
		return nil
	}
	k, v := e.root.Content[j], e.root.Content[j+1]
	keyLine := k.Line - 1
	end := len(e.lines)
	if j+2 < len(e.root.Content) {
		end = e.root.Content[j+2].Line - 1
	}
	if v.Tag == "!!null" && v.Value == "" {
		e.ins[keyLine+1] += e.encode(gen, indent+"  ")
		return nil
	}
	if v.Kind != gen.Kind || v.Style&yaml.FlowStyle != 0 || len(v.Content) == 0 {
		return fmt.Errorf("%s: aval gen needs a block %s here", key, map[bool]string{true: "sequence", false: "mapping"}[gen.Kind == yaml.SequenceNode])
	}
	starts, names := e.entries(v)
	ends := make([]int, len(starts))
	for i := range starts {
		if i+1 < len(starts) {
			ends[i] = e.back(starts[i+1], starts[i]+1)
		} else {
			ends[i] = e.back(end, keyLine+1)
		}
	}
	at := ends[len(ends)-1]
	first := true
	for i, name := range names {
		if !owned(e.lang, name) {
			continue
		}
		from := starts[i]
		if first {
			at, first = from, false
		} else {
			from = ends[i-1] // its leading comments go with it
		}
		for l := from; l < ends[i]; l++ {
			e.drop[l] = true
		}
	}
	prefix := e.lines[starts[0]][:strings.IndexFunc(e.lines[starts[0]], func(r rune) bool { return r != ' ' })]
	e.ins[at] += e.encode(gen, prefix)
	return nil
}

// add appends the new top-level key with gen, unless gen is empty.
func (e *edit) add(key, indent string, gen *yaml.Node) {
	if len(gen.Content) > 0 {
		e.tail += indent + key + ":" + e.eol + e.encode(gen, indent+"  ")
	}
}

// find returns the index of the top-level key in root.Content, or -1.
func (e *edit) find(key string) int {
	for i := 0; i < len(e.root.Content); i += 2 {
		if e.root.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// back moves line index i up over gap lines, not above floor.
func (e *edit) back(i, floor int) int {
	for i > floor && isGap(e.lines[i-1]) {
		i--
	}
	return i
}

// entries returns the first line and the name of each entry of the block
// collection v: a fragment's key, or a test's metadata.name.
func (e *edit) entries(v *yaml.Node) (starts []int, names []string) {
	if v.Kind == yaml.MappingNode {
		for i := 0; i < len(v.Content); i += 2 {
			starts = append(starts, v.Content[i].Line-1)
			names = append(names, v.Content[i].Value)
		}
		return starts, names
	}
	for _, it := range v.Content {
		s := it.Line - 1
		for !strings.HasPrefix(strings.TrimLeft(e.lines[s], " "), "-") { // the item's dash
			s--
		}
		starts = append(starts, s)
		names = append(names, testName(it))
	}
	return starts, names
}

// encode renders n as YAML, indent 2, with prefix before every line.
func encode(n *yaml.Node, prefix string) string {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	_ = enc.Encode(n) // a node built by generated always encodes
	_ = enc.Close()
	var out strings.Builder
	for l := range strings.SplitAfterSeq(b.String(), "\n") {
		if l != "" {
			out.WriteString(prefix + l)
		}
	}
	return out.String()
}

// replace does on the nodes what splice does on the lines.
func replace(root *yaml.Node, lang, key string, gen *yaml.Node) {
	v := value(root, key)
	empty := len(gen.Content) == 0 // splice writes no line for it
	switch {
	case v == nil && empty:
		return
	case v == nil:
		root.Content = append(root.Content, str(key), gen)
		return
	case v.Kind != gen.Kind && empty:
		return
	case v.Kind != gen.Kind:
		*v = *gen
		return
	}
	step := 1
	if v.Kind == yaml.MappingNode {
		step = 2
	}
	var kept []*yaml.Node
	at := -1
	for i := 0; i < len(v.Content); i += step {
		name := v.Content[i].Value
		if step == 1 {
			name = testName(v.Content[i])
		}
		if owned(lang, name) {
			if at < 0 {
				at = len(kept)
			}
			continue
		}
		kept = append(kept, v.Content[i:i+step]...)
	}
	if at < 0 {
		at = len(kept)
	}
	v.Content = append(kept[:at:at], append(gen.Content, kept[at:]...)...)
	if len(v.Content) == 0 { // every entry dropped: the key is left empty
		*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
	}
}

// canon renders n without comments or styles, to compare two trees by
// content alone.
func canon(n *yaml.Node) string {
	var strip func(*yaml.Node)
	strip = func(n *yaml.Node) {
		n.Style, n.HeadComment, n.LineComment, n.FootComment = 0, "", "", ""
		for _, c := range n.Content {
			strip(c)
		}
	}
	strip(n)
	return encode(n, "")
}

// Diff returns the lines that differ between old and new as one hunk:
// the common leading and trailing lines are dropped, the rest of old is
// prefixed "-" and the rest of new "+".
// ponytail: one hunk, not a minimal diff; the owned entries are contiguous,
// so it is usually exact.
func Diff(oldName, newName string, old, new []byte) string {
	a, b := strings.SplitAfter(string(old), "\n"), strings.SplitAfter(string(new), "\n")
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", oldName, newName, p+1, len(a)-p-s, p+1, len(b)-p-s)
	for _, l := range a[p : len(a)-s] {
		out.WriteString("-" + strings.TrimSuffix(l, "\n") + "\n")
	}
	for _, l := range b[p : len(b)-s] {
		out.WriteString("+" + strings.TrimSuffix(l, "\n") + "\n")
	}
	return out.String()
}
