package pinning

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
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

// decodedImages checks every image: key under n by its decoded value, so
// keys written with an escape ("ima\x67e"), a tag, an anchor or an alias,
// explicit keys (? image) and keys in nested flow or block sequences are
// read as Compose and Kubernetes read them. It also checks a Compose
// build.dockerfile_inline as a Dockerfile and the docker-image:// values of
// build.additional_contexts. Lines are the parser's, which also breaks lines
// at a lone CR, NEL or LS.
func decodedImages(in Input, n *yaml.Node) []Finding {
	var out []Finding
	for i := 0; n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch scalar(k) {
		case "image":
			out = append(out, decodedImage(in, v)...)
		case "dockerfile_inline":
			for _, f := range (dockerfile{}).Detect(Input{Path: in.Path, Data: []byte(scalar(v)), Has: in.Has}) {
				f.Line = v.Line // ponytail: the line of the value, not of the instruction in it
				out = append(out, f)
			}
		case "additional_contexts": // a mapping (name: value) or a list (name=value)
			for _, c := range v.Content {
				if _, ref, ok := strings.Cut(scalar(c), "docker-image://"); ok {
					out = append(out, image(in.Path, c.Line, ref, nil)...)
				}
			}
		}
	}
	for _, c := range n.Content {
		out = append(out, decodedImages(in, c)...)
	}
	return out
}

// templatedKeyRe is a line with a template action ({{ }}) before the colon
// of its key, outside a comment.
var templatedKeyRe = regexp.MustCompile(`^[^:#]*\{\{.*\}\}[^:]*:(\s|$)`)

// templatedKeys reports keys a Helm template writes (ima{{ "ge" }}:,
// {{ $k }}:): rendered, any of them may be image, so fail closed.
func templatedKeys(in Input) []Finding {
	var out []Finding
	for i, l := range strings.Split(string(in.Data), "\n") {
		if l = strings.TrimSpace(l); templatedKeyRe.MatchString(l) {
			out = append(out, Finding{File: in.Path, Line: i + 1, Ecosystem: "scan", Ref: l,
				Fix: "a template writes this key, so Aval cannot tell whether it is image: write the key plainly or render the chart into the scope"})
		}
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
// rejects (a Helm template), where yamlImageRe does not read them.
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
func decodedImage(in Input, v *yaml.Node) []Finding {
	file, n := in.Path, v.Line
	var ref string
	switch {
	case v.Kind == yaml.MappingNode && scalarKeys(v): // Helm values
		return nil
	case v.Kind == yaml.MappingNode: // a template action ({{ x }}) reads as a mapping with a mapping key
		return image(file, n, valueText(in.Data, v), nil)
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

// scalarKeys reports whether every key of a mapping is a scalar.
func scalarKeys(m *yaml.Node) bool {
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Kind != yaml.ScalarNode {
			return false
		}
	}
	return true
}

// yamlBreakRe is a line break as the YAML parser counts one.
var yamlBreakRe = regexp.MustCompile("\r\n|[\n\r\u0085\u2028\u2029]")

// physLines maps the parser's line numbers (index line-1) to the file's
// \n-separated lines.
func physLines(data []byte) []int {
	out := []int{1}
	for _, m := range yamlBreakRe.FindAllIndex(data, -1) {
		n := out[len(out)-1]
		if data[m[1]-1] == '\n' {
			n++
		}
		out = append(out, n)
	}
	return out
}

// valueText is the source text of a node to the end of its line, without a
// comment, or "image" when there is none.
// ponytail: line and column are clamped, since the parser also decodes
// UTF-16, whose line breaks yamlBreakRe does not see.
func valueText(data []byte, v *yaml.Node) string {
	lines := yamlBreakRe.Split(string(data), -1)
	l := []rune(lines[min(v.Line, len(lines))-1])
	t, _, _ := strings.Cut(string(l[min(v.Column-1, len(l)):]), " #")
	return cmp.Or(strings.TrimSpace(t), "image")
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

// kustomize checks the images: (and legacy imageTags:) of a kustomization,
// which retarget an image by name (newName) or tag (newTag); only a digest
// pins it. A patch or replacement that mentions an image is a scan finding.
type kustomize struct{}

func (kustomize) Applies(p string) bool {
	b := path.Base(p)
	return b == "kustomization.yaml" || b == "kustomization.yml" || b == "Kustomization"
}

// kustomization is what Aval reads of a kustomization file.
type kustomization struct {
	Images, ImageTags []struct{ Name, NewName, NewTag, TagSuffix, Digest string }

	Patches, PatchesStrategicMerge, PatchesJson6902, Replacements []json.RawMessage
}

// patches reports each patch or replacement that mentions an image.
// ponytail: any mention, inline or in a file name; a patch file that sets an
// image under another name is a known limit.
func (k kustomization) patches(file string) []Finding {
	var out []Finding
	for _, f := range []struct {
		name    string
		entries []json.RawMessage
	}{{"patches", k.Patches}, {"patchesStrategicMerge", k.PatchesStrategicMerge}, {"patchesJson6902", k.PatchesJson6902}, {"replacements", k.Replacements}} {
		for i, e := range f.entries {
			if bytes.Contains(bytes.ToLower(e), []byte("image")) {
				out = append(out, Finding{File: file, Ecosystem: "scan", Ref: fmt.Sprintf("%s[%d]", f.name, i),
					Fix: "kustomize patch or replacement sets an image Aval cannot read: pin the image in the base, or retarget it in images: with a digest"})
			}
		}
	}
	return out
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
		var k kustomization
		if err := json.Unmarshal(j, &k); err != nil {
			return scanErr(err)
		}
		out = append(out, k.patches(in.Path)...)
		for _, e := range slices.Concat(k.Images, k.ImageTags) { // imageTags: the legacy name kustomize still applies
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
