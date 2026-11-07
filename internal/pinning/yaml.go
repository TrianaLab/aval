package pinning

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// unresolvedFix is the fix for an image: value Aval cannot resolve.
const unresolvedFix = "image is a YAML alias, block scalar or tag Aval cannot resolve: write the reference inline with its digest (name@sha256:...)"

// yamlDocs decodes every document of a YAML stream, up to the first error.
// Aliases are not expanded.
func yamlDocs(data []byte) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []*yaml.Node
	for {
		var n yaml.Node
		if err := dec.Decode(&n); errors.Is(err, io.EOF) {
			return docs, nil
		} else if err != nil {
			return docs, err
		}
		docs = append(docs, &n)
	}
}

// scalar returns the decoded value of a scalar node, or of the scalar an
// alias points to; "" for anything else.
func scalar(n *yaml.Node) string {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	if n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// lastLine bounds a line number from the YAML parser, which also breaks
// lines at a lone CR, to the lines Aval counts.
func lastLine(data []byte) int { return bytes.Count(data, []byte("\n")) + 1 }

// decodedImages checks the image: keys the regexes did not read (seen holds
// the line and column of those they did): keys written with an escape
// ("ima\x67e"), a tag, an anchor or an alias, explicit keys (? image) and
// keys in nested flow or block sequences. A file that is not valid YAML (a
// Helm template, say) falls back to escapedKeys.
func decodedImages(in Input, seen map[[2]int]bool) []Finding {
	docs, err := yamlDocs(in.Data)
	last := lastLine(in.Data)
	var out []Finding
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		for i := 0; n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if scalar(k) == "image" && !seen[[2]int{k.Line, k.Column}] {
				out = append(out, decodedImage(in.Path, min(v.Line, last), v)...)
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	for _, d := range docs {
		walk(d)
	}
	if err != nil {
		out = append(out, escapedKeys(in)...)
		out = append(out, prefixedKeys(in)...)
	}
	return out
}

// prefixedKeyRe is a block image: key behind a ?, tag or anchor, which
// yamlImageRe does not read; explicitKeyRe is ? image with its value on the
// next line (: x).
var (
	prefixedKeyRe = regexp.MustCompile(`^\s*(?:-\s+)?(?:(?:\?|![^\s:]*|&[^\s:]+)\s+)+["']?image["']?\s*:\s*(.*)$`)
	explicitKeyRe = regexp.MustCompile(`^\s*(?:-\s+)?\?\s+(?:(?:![^\s:]*|&[^\s:]+)\s+)*["']?image["']?\s*(?:#.*)?$`)
)

// prefixedKeys is the fallback for those keys in a file the YAML parser
// rejects (a Helm template), where decodedImages cannot read them.
func prefixedKeys(in Input) []Finding {
	var out []Finding
	explicit := false
	for i, l := range strings.Split(string(in.Data), "\n") {
		l = strings.TrimSuffix(l, "\r")
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), ":"); explicit && ok {
			out = append(out, yamlImage(in.Path, i+1, v)...)
		}
		if m := prefixedKeyRe.FindStringSubmatch(l); m != nil {
			out = append(out, yamlImage(in.Path, i+1, m[1])...)
		}
		explicit = explicitKeyRe.MatchString(l)
	}
	return out
}

// decodedImage checks one decoded image: value, as yamlImage does a
// written one.
func decodedImage(file string, n int, v *yaml.Node) []Finding {
	var ref string
	switch {
	case v.Kind == yaml.MappingNode: // Helm values
		return nil
	case v.Kind == yaml.AliasNode:
		ref = "*" + v.Value
	case v.Kind != yaml.ScalarNode:
		ref = "image"
	case v.Style&yaml.LiteralStyle != 0:
		ref = "|"
	case v.Style&yaml.FoldedStyle != 0:
		ref = ">"
	case v.Style&yaml.TaggedStyle != 0:
		ref = strings.TrimSpace(v.Tag + " " + v.Value)
	default:
		return image(file, n, v.Value, nil)
	}
	return []Finding{{File: file, Line: n, Ecosystem: "container", Ref: ref, Fix: unresolvedFix}}
}

// escapedKeyRe is a double-quoted scalar with at least one escape, behind
// a ? (an explicit key) or before a colon (a key).
var escapedKeyRe = regexp.MustCompile(`(\?\s*)?"((?:[^"\\]|\\.)*\\.(?:[^"\\]|\\.)*)"(\s*:)?`)

// escapedKeys is the fallback for a file the YAML parser rejects: a quoted
// key with an escape is decoded on its own. One that decodes to image has
// its value checked when it is on the same line. Fail closed with a scan
// finding when the key does not decode, when its value is elsewhere (an
// explicit key, the next line) or when a quoted scalar continues on the
// next line (it may join into image).
func escapedKeys(in Input) []Finding {
	var out []Finding
	scan := func(n int, ref, fix string) {
		out = append(out, Finding{File: in.Path, Line: n, Ecosystem: "scan", Ref: ref, Fix: fix})
	}
	for i, l := range strings.Split(string(in.Data), "\n") {
		l = strings.TrimSuffix(l, "\r")
		if strings.HasSuffix(l, `\`) && openQuote(l) {
			scan(i+1, strings.TrimSpace(l), "a quoted key that continues on the next line, so Aval cannot tell whether it is image: write the key plainly")
		}
		for _, m := range escapedKeyRe.FindAllStringSubmatchIndex(l, -1) {
			if m[2] < 0 && m[6] < 0 {
				continue // a quoted value, not a key
			}
			ref, rest := l[m[0]:m[5]+1], strings.TrimSpace(l[m[1]:])
			var k string
			switch err := yaml.Unmarshal([]byte(`"`+l[m[4]:m[5]]+`"`), &k); {
			case err != nil:
				scan(i+1, ref, "a quoted key with an escape YAML cannot decode, so Aval cannot tell whether it is image: write the key plainly")
			case k != "image":
			case m[6] < 0 || rest == "" || rest[0] == '#':
				scan(i+1, ref, "an escaped image key whose value Aval cannot place: write the key plainly")
			default:
				out = append(out, yamlImage(in.Path, i+1, l[m[1]:])...)
			}
		}
	}
	return out
}

// openQuote reports whether l ends inside a double-quoted scalar.
func openQuote(l string) bool {
	in := false
	for i := 0; i < len(l); i++ {
		switch {
		case in && l[i] == '\\':
			i++
		case l[i] == '"':
			in = !in
		}
	}
	return in
}

// kustomize checks the images: of a kustomization, which retarget an
// image by name (newName) or tag (newTag); only a digest pins it.
type kustomize struct{}

func (kustomize) Applies(p string) bool {
	b := path.Base(p)
	return b == "kustomization.yaml" || b == "kustomization.yml" || b == "Kustomization"
}

func (kustomize) Detect(in Input) []Finding {
	// Decode like kustomize does: YAML to JSON (merge keys resolved), then
	// encoding/json, which matches field names case-insensitively.
	scanErr := func(err error) []Finding {
		return []Finding{{File: in.Path, Ecosystem: "scan", Ref: in.Path, Fix: "images cannot be read as kustomize reads them: " + err.Error()}}
	}
	dec := yaml.NewDecoder(bytes.NewReader(in.Data))
	var out []Finding
	for {
		var v any
		if err := dec.Decode(&v); errors.Is(err, io.EOF) {
			return out
		} else if err != nil {
			return scanErr(err)
		}
		j, err := json.Marshal(v)
		if err != nil {
			return scanErr(err)
		}
		var k struct {
			Images []struct{ Name, NewName, NewTag, TagSuffix, Digest string }
		}
		if err := json.Unmarshal(j, &k); err != nil {
			return scanErr(err)
		}
		for _, e := range k.Images {
			if e.NewName == "" && e.NewTag == "" && e.TagSuffix == "" && e.Digest == "" {
				continue // the image is left as the manifests write it
			}
			ref := cmp.Or(e.NewName, e.Name)
			if e.NewTag != "" {
				ref += ":" + e.NewTag
			} else if e.TagSuffix != "" {
				ref += ":<tag>"
			}
			if e.TagSuffix != "" {
				ref += e.TagSuffix // kustomize drops the digest when it appends a suffix
			} else if e.Digest != "" {
				ref += "@" + e.Digest
			}
			// ponytail: line 0, the decoded value has no position.
			out = append(out, image(in.Path, 0, ref, nil)...)
		}
	}
}
