package pinning

import (
	"bytes"
	"cmp"
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

// escapedKeyRe is a double-quoted key with at least one escape.
var escapedKeyRe = regexp.MustCompile(`"((?:[^"\\]|\\.)*\\.(?:[^"\\]|\\.)*)"\s*:`)

// escapedKeys is the fallback for a file the YAML parser rejects: a quoted
// key with an escape is decoded on its own. One that decodes to image has
// its value checked; one that does not decode is a scan finding (fail
// closed).
func escapedKeys(in Input) []Finding {
	var out []Finding
	for i, l := range strings.Split(string(in.Data), "\n") {
		for _, m := range escapedKeyRe.FindAllStringSubmatchIndex(l, -1) {
			var k string
			switch err := yaml.Unmarshal([]byte(`"`+l[m[2]:m[3]]+`"`), &k); {
			case err != nil:
				out = append(out, Finding{File: in.Path, Line: i + 1, Ecosystem: "scan", Ref: l[m[0] : m[3]+1],
					Fix: "a quoted key with an escape YAML cannot decode, so Aval cannot tell whether it is image: write the key plainly"})
			case k == "image":
				out = append(out, yamlImage(in.Path, i+1, l[m[1]:])...)
			}
		}
	}
	return out
}

// kustomize checks the images: of a kustomization, which retarget an
// image by name (newName) or tag (newTag); only a digest pins it.
type kustomize struct{}

func (kustomize) Applies(p string) bool {
	b := path.Base(p)
	return b == "kustomization.yaml" || b == "kustomization.yml" || b == "Kustomization"
}

func (kustomize) Detect(in Input) []Finding {
	docs, err := yamlDocs(in.Data)
	if err != nil {
		return []Finding{{File: in.Path, Ecosystem: "scan", Ref: in.Path, Fix: "not valid YAML, so its images cannot be checked: " + err.Error()}}
	}
	last := lastLine(in.Data)
	var out []Finding
	for _, d := range docs {
		for _, e := range kustomizeImages(d) {
			f := map[string]string{}
			for i := 0; i+1 < len(e.Content); i += 2 {
				f[scalar(e.Content[i])] = scalar(e.Content[i+1])
			}
			if f["newName"] == "" && f["newTag"] == "" && f["digest"] == "" {
				continue // the image is left as the manifests write it
			}
			ref := cmp.Or(f["newName"], f["name"])
			if f["newTag"] != "" {
				ref += ":" + f["newTag"]
			}
			if f["digest"] != "" {
				ref += "@" + f["digest"]
			}
			out = append(out, image(in.Path, min(e.Line, last), ref, nil)...)
		}
	}
	return out
}

// kustomizeImages returns the entries of the top-level images: list.
func kustomizeImages(doc *yaml.Node) []*yaml.Node {
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	m := doc.Content[0].Content
	for i := 0; i+1 < len(m); i += 2 {
		if scalar(m[i]) == "images" && m[i+1].Kind == yaml.SequenceNode {
			return m[i+1].Content
		}
	}
	return nil
}
