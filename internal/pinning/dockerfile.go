package pinning

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// dockerLine is one Dockerfile instruction or comment, and the raw body
// lines of its heredocs.
type dockerLine struct {
	line
	body []line
}

// directiveRe is a parser directive after its # and leading blanks.
var directiveRe = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9]*)\s*=\s*(.+?)\s*$`)

// continuationRe is a line that ends in the escape character, not itself
// escaped, per escape character (BuildKit's lineContinuationRegex).
var continuationRe = map[string]*regexp.Regexp{
	`\`: regexp.MustCompile(`([^\\])\\[ \t]*$|^\\[ \t]*$`),
	"`": regexp.MustCompile("([^`])`[ \t]*$|^`[ \t]*$"),
}

// dockerLines splits a Dockerfile the way BuildKit's parser does
// (moby/buildkit frontend/dockerfile/parser): # escape= among the leading
// parser directives sets the escape character; a comment line stands on its
// own; a line ending in the escape character continues on the next one,
// joined with nothing, and comment or blank lines inside a continuation are
// dropped; the bodies of RUN, COPY and ADD heredocs are kept raw. What
// BuildKit refuses (an invalid escape, a repeated directive, an unterminated
// heredoc) is a finding, and heredocs after it are read as
// instructions, which reports more, never less.
func dockerLines(file, data string) ([]dockerLine, []Finding) {
	phys := strings.Split(strings.TrimPrefix(data, "\ufeff"), "\n")
	for i := range phys {
		phys[i] = strings.TrimRight(phys[i], "\r")
	}
	esc, bad := escape(file, phys)
	cont := continuationRe[esc]
	var out []dockerLine
	for i := 0; i < len(phys); i++ {
		t := strings.TrimLeftFunc(phys[i], unicode.IsSpace)
		if t == "" {
			continue
		}
		l := dockerLine{line: line{n: i + 1, text: t}}
		if isComment(t) {
			out = append(out, l)
			continue
		}
		var b strings.Builder
		more := cont.MatchString(t)
		b.WriteString(cont.ReplaceAllString(t, "$1"))
		for more && i+1 < len(phys) {
			i++
			if s := strings.TrimLeftFunc(phys[i], unicode.IsSpace); s == "" || isComment(s) {
				continue
			}
			more = cont.MatchString(phys[i])
			b.WriteString(cont.ReplaceAllString(phys[i], "$1"))
		}
		l.text = b.String()
		if bad == nil {
			var ok bool
			if l.body, i, ok = heredocBodies(l.text, phys, i, esc); !ok {
				bad = []Finding{{File: file, Line: l.n, Ecosystem: "container", Ref: t,
					Fix: "an unterminated heredoc or a heredoc name Aval cannot decode: Aval cannot check its references; end the heredoc and write its name plainly"}}
			}
		}
		out = append(out, l)
	}
	return out, bad
}

// escape reads the leading parser directives and returns the escape
// character they set.
func escape(file string, phys []string) (string, []Finding) {
	esc := `\`
	seen := map[string]bool{}
	for i, p := range phys {
		p = strings.TrimLeftFunc(p, unicode.IsSpace)
		rest, ok := strings.CutPrefix(p, "#")
		m := directiveRe.FindStringSubmatch(strings.TrimLeftFunc(rest, unicode.IsSpace))
		if !ok || m == nil {
			break
		}
		k := strings.ToLower(m[1])
		var fix string
		switch {
		case k != "syntax" && k != "escape" && k != "check":
			return esc, nil
		case seen[k]:
			fix = "a parser directive is repeated, so BuildKit refuses the file"
		case k == "escape" && continuationRe[m[2]] == nil:
			fix = "the escape character must be \\ or `, so BuildKit refuses the file"
		}
		if fix != "" {
			return esc, []Finding{{File: file, Line: i + 1, Ecosystem: "container", Ref: p, Fix: fix + ": Aval cannot check its references; fix the directive"}}
		}
		seen[k] = true
		if k == "escape" {
			esc = m[2]
		}
	}
	return esc, nil
}

// heredocRe is a heredoc word: optional file descriptor, <<, optional -.
var heredocRe = regexp.MustCompile(`^\d*<<(-?)\s*([^<]+)$`)

// heredocBodies collects the bodies of the heredocs an instruction opens
// after physical line i, and returns them with the last line consumed.
// Words are split with the escape character esc, as BuildKit does. If
// one is not terminated, it returns false and consumes nothing: BuildKit
// either refuses the file or reads no heredoc there.
func heredocBodies(text string, phys []string, i int, esc string) ([]line, int, bool) {
	if !takesHeredocs(text) {
		return nil, i, true
	}
	var body []line
	j := i + 1
	for _, w := range shellWords(text, rune(esc[0])) {
		m := heredocRe.FindStringSubmatch(w)
		if m == nil {
			continue
		}
		name, ok := heredocName(m[2], esc)
		if !ok {
			return nil, i, false
		}
		for ; j < len(phys); j++ {
			term := phys[j]
			if m[1] == "-" {
				term = strings.TrimLeft(term, "\t")
			}
			if term == name {
				break
			}
			body = append(body, line{j + 1, phys[j]})
		}
		if j == len(phys) {
			return nil, i, false
		}
		j++
	}
	return body, j - 1, true
}

// takesHeredocs reports whether an instruction is a RUN, COPY or ADD (after
// an optional ONBUILD) in shell form, the only ones BuildKit reads heredocs in.
func takesHeredocs(text string) bool {
	f := strings.Fields(text)
	if len(f) > 1 && strings.EqualFold(f[0], "ONBUILD") {
		f = f[1:]
	}
	if len(f) < 2 || !slices.Contains([]string{"RUN", "COPY", "ADD"}, strings.ToUpper(f[0])) {
		return false
	}
	k := 1 // flags (--mount=) come before the arguments
	for k < len(f)-1 && strings.HasPrefix(f[k], "--") {
		k++
	}
	var args []string
	return !strings.HasPrefix(f[k], "[") || json.Unmarshal([]byte(text[strings.Index(text, f[k]):]), &args) != nil
}

// heredocName decodes a heredoc name: a leading escape character or one
// pair of enclosing quotes is dropped. A name with any other quote,
// backslash, escape character or $
// is not ok: BuildKit's lexer decodes and expands those (and with some
// expansions reads no heredoc at all), so fail closed instead of guessing.
// Nor is an empty name (<<""): BuildKit reads no heredoc there.
func heredocName(w, esc string) (string, bool) {
	name := strings.TrimPrefix(w, esc)
	if len(name) >= 2 && (name[0] == '"' || name[0] == '\'') && name[len(name)-1] == name[0] {
		name = name[1 : len(name)-1]
	}
	return name, name != "" && !strings.ContainsAny(name, `"'\$`+esc)
}

// dockerArgs records every NAME=value of every ARG (any case, any number
// of names) in v.
func dockerArgs(lines []dockerLine, v *vars) {
	for _, l := range lines {
		f := strings.Fields(l.text)
		if len(f) < 2 || !strings.EqualFold(f[0], "ARG") {
			continue
		}
		for _, a := range f[1:] {
			if name, val, ok := strings.Cut(a, "="); ok {
				v.m[name] = strings.Trim(val, `"'`)
			}
		}
	}
}

// globalArgs are the ARGs before the first FROM that have a value, the
// only variables BuildKit expands in FROM. An ARG NAME without a value
// drops NAME, so a FROM using it is reported, not guessed.
func globalArgs(lines []dockerLine) *vars {
	v := &vars{m: map[string]string{}, budget: maxExpand}
	for _, l := range lines {
		f := strings.Fields(l.text)
		if len(f) > 0 && strings.EqualFold(f[0], "FROM") {
			break
		}
		if len(f) < 2 || !strings.EqualFold(f[0], "ARG") {
			continue
		}
		for _, a := range f[1:] {
			name, val, ok := strings.Cut(a, "=")
			if delete(v.m, name); ok {
				v.m[name] = strings.Trim(val, `"'`)
			}
		}
	}
	return v
}

// buildkitFrom checks a FROM expanded with the global ARGs. An image that
// still has a variable (a stage ARG, an ENV, an unset ARG) is reported by
// its line instead of guessed.
func buildkitFrom(file string, l line, f []string, stages map[string]bool) []Finding {
	i := 1 // flags (--platform=$BUILDPLATFORM) come first
	for i < len(f) && strings.HasPrefix(f[i], "--") {
		i++
	}
	rest := f[i:]
	var out []Finding
	switch {
	case len(rest) == 0:
	case strings.Contains(rest[0], "$"):
		out = []Finding{{File: file, Line: l.n, Ecosystem: "container", Ref: l.text,
			Fix: "FROM uses a variable that is not a global ARG with a value (an ARG before the first FROM), so Aval cannot tell the image: give it a global ARG default or write the image with its digest"}}
	default:
		out = image(file, l.n, rest[0], stages)
	}
	if len(rest) >= 3 && strings.EqualFold(rest[1], "AS") {
		stages[strings.ToLower(rest[2])] = true
	}
	return out
}

// shellWords splits s at unquoted Unicode spaces and keeps quotes and
// escapes (by the escape character esc), as BuildKit's lexer does when it
// looks for heredoc words.
func shellWords(s string, esc rune) []string {
	var out []string
	var b strings.Builder
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
		case r == esc && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}
