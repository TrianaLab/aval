package java

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/TrianaLab/aval/internal/gen"
)

var (
	includeRe = regexp.MustCompile(`\binclude\b`)
	// Settings that move a project or its build file, or pull in another
	// build: the files they point to cannot be read from the include list.
	settingsRe = regexp.MustCompile(`\b(includeBuild|includeFlat|projectDir|buildFileName)\b`)
	projectRe  = regexp.MustCompile(`\b(project|findProject)\s*\(`)
	pathArgRe  = regexp.MustCompile(`^path\s*[:=]\s*`)
	accessorRe = regexp.MustCompile(`\bprojects\.`)
)

// gradle reads the include(...) list of the settings script and the
// project(...) references of each build script. The script is code, so it
// reads only string literals and fails on anything else: a computed include
// or project path could hide a unit or an edge.
func gradle(root, settings string) ([]gen.Unit, error) {
	text, err := script(root, settings)
	if err != nil {
		return nil, err
	}
	if m := settingsRe.FindString(text); m != "" {
		return nil, fmt.Errorf("%s: %s: %w", settings, m, errUnsupported)
	}
	paths, err := includes(text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", settings, err)
	}
	// References in the root build script (subprojects { ... }, allprojects
	// { ... }) may reach every project: each one is an edge of every unit.
	var common []string
	if build, err := oneOf(root, "build.gradle.kts", "build.gradle"); err != nil {
		return nil, err
	} else if build != "" {
		if common, err = refs(root, build, ":"); err != nil {
			return nil, err
		}
	}
	gradle := tool(root, "gradlew", "gradle")
	seen := map[string]bool{}
	var units []gen.Unit
	for _, p := range paths {
		if !strings.HasPrefix(p, ":") {
			p = ":" + p
		}
		id := projectID(p)
		if err := checkDir(id); err != nil {
			return nil, fmt.Errorf("%s: include %q: %w", settings, p, err)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		build, err := oneOf(filepath.Join(root, filepath.FromSlash(id)), "build.gradle.kts", "build.gradle")
		if err != nil {
			return nil, err
		}
		deps := common
		if build != "" {
			own, err := refs(root, id+"/"+build, p)
			if err != nil {
				return nil, err
			}
			deps = append(own, common...)
		}
		units = append(units, gen.Unit{
			ID:      id,
			Source:  source(root, id, build),
			Deps:    without(deps, id),
			Command: []string{gradle, p + ":test"},
		})
	}
	return units, nil
}

// projectID turns a project path (:a:b) into a unit ID and directory (a/b).
func projectID(p string) string { return strings.ReplaceAll(strings.TrimPrefix(p, ":"), ":", "/") }

func without(ids []string, id string) []string {
	var out []string
	for _, d := range ids {
		if d != id {
			out = append(out, d)
		}
	}
	return out
}

// script reads a Groovy or Kotlin script without its comments.
func script(root, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return "", err
	}
	return stripComments(string(data)), nil
}

// includes returns the project paths of every include statement: Kotlin
// include(":a", ":b") and Groovy include ':a', ':b', over several lines too.
func includes(text string) ([]string, error) {
	var out []string
	for _, loc := range includeRe.FindAllStringIndex(text, -1) {
		i := skip(text, loc[1], false)
		paren := i < len(text) && text[i] == '('
		if paren {
			i++
		}
		for {
			v, j, err := literal(text, skip(text, i, paren))
			if err != nil {
				return nil, fmt.Errorf("include: %w", err)
			}
			out = append(out, v)
			if i = skip(text, j, paren); i >= len(text) || text[i] != ',' {
				break
			}
			if i = skip(text, i+1, true); paren && i < len(text) && text[i] == ')' {
				break // a trailing comma
			}
		}
		ok := i == len(text) || strings.ContainsRune("\n;}", rune(text[i]))
		if paren {
			ok = i < len(text) && text[i] == ')'
		}
		if !ok {
			return nil, fmt.Errorf("include: an argument other than a string literal: %w", errUnsupported)
		}
	}
	return out, nil
}

// refs returns the unit IDs a build script refers to with project(...) or
// findProject(...), relative paths resolved against self, the project path
// of the script. The root project (":") is not a unit and is skipped.
func refs(root, name, self string) ([]string, error) {
	text, err := script(root, name)
	if err != nil {
		return nil, err
	}
	if accessorRe.MatchString(text) {
		return nil, fmt.Errorf("%s: type-safe project accessors (projects.*): %w, use project(\":path\")", name, errUnsupported)
	}
	var ids []string
	for _, loc := range projectRe.FindAllStringIndex(text, -1) {
		i := skip(text, loc[1], true)
		i += len(pathArgRe.FindString(text[i:]))
		v, _, err := literal(text, i)
		if err != nil {
			return nil, fmt.Errorf("%s: project(...): %w", name, err)
		}
		if !strings.HasPrefix(v, ":") {
			v = strings.TrimSuffix(self, ":") + ":" + v
		}
		if v != ":" {
			ids = append(ids, projectID(v))
		}
	}
	return ids, nil
}

// skip returns the index of the first character at or after i that is not
// a space or tab, nor a line break when lines is set.
func skip(text string, i int, lines bool) int {
	for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == '\r' || lines && text[i] == '\n') {
		i++
	}
	return i
}

// literal reads the plain string literal at i ('...' or "...") and returns
// its value and the index after it. Interpolation, escapes and anything
// that is not a literal are errors.
func literal(text string, i int) (string, int, error) {
	if i >= len(text) || text[i] != '"' && text[i] != '\'' {
		return "", 0, fmt.Errorf("an argument other than a string literal: %w", errUnsupported)
	}
	end := strings.IndexByte(text[i+1:], text[i])
	if end < 0 || strings.ContainsAny(text[i+1:i+1+end], "$\\\n") {
		return "", 0, fmt.Errorf("a string with interpolation or escapes: %w", errUnsupported)
	}
	return text[i+1 : i+1+end], i + end + 2, nil
}

// stripComments blanks // and /* */ comments outside string literals.
func stripComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		switch {
		case strings.HasPrefix(rest, "//"):
			n := strings.IndexByte(rest, '\n')
			if n < 0 {
				n = len(rest)
			}
			i += n
		case strings.HasPrefix(rest, "/*"):
			n := strings.Index(rest[2:], "*/")
			if n < 0 {
				return b.String()
			}
			b.WriteByte(' ')
			i += n + 4
		case rest[0] == '"' || rest[0] == '\'':
			n := 1
			for n < len(rest) && rest[n] != rest[0] && rest[n] != '\n' {
				if rest[n] == '\\' {
					n++
				}
				n++
			}
			n = min(n+1, len(rest))
			b.WriteString(rest[:n])
			i += n
		default:
			b.WriteByte(rest[0])
			i++
		}
	}
	return b.String()
}
