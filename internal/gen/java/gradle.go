package java

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/TrianaLab/aval/internal/gen"
)

var (
	includeRe = regexp.MustCompile(`\binclude\b`)
	// Settings that move a project or its build file, or pull in another
	// build: the files they point to cannot be read from the include list.
	settingsRe = regexp.MustCompile(`\b(includeBuild|includeFlat|projectDir|buildFileName)\b`)
	projectRe  = regexp.MustCompile(`\b(project|findProject|evaluationDependsOn)\s*\(`)
	pathArgRe  = regexp.MustCompile(`^path\s*[:=]\s*`)
	accessorRe = regexp.MustCompile(`\bprojects\.`)
	// apply(from = "...") or apply from: '...' pulls in a script no unit covers.
	applyFromRe = regexp.MustCompile(`\bapply\s*\(?\s*from\b`)
	// Names that reach the files of another project from a subproject.
	parentRe = regexp.MustCompile(`\b(parent|getParent|parentFile)\b|\brootProject\s*\.\s*(get)?([cC]hildProjects|[sS]ubprojects|[aA]llprojects)\b`)
	// project(":").subprojects and kin: the root project's children, from a subproject.
	rootChildrenRe = regexp.MustCompile(`\bproject\s*\(\s*(path\s*[:=]\s*)?["']:["']\s*\)\s*\.\s*(get)?([cC]hildProjects|[sS]ubprojects|[aA]llprojects)\b`)
	rootDirRe      = regexp.MustCompile(`^\$(rootDir|\{(rootDir|rootProject\.rootDir|project\.rootDir|rootProject\.projectDir)\})/`)
	// A root variable anywhere in a literal.
	// Calls whose arguments are paths: file, files, layout.*.dir, resolve, ...
	pathCallRe   = regexp.MustCompile(`\b(file|files|dir|resolve|fileTree|from|into|File)\s*\(`)
	rootVarAnyRe = regexp.MustCompile(`\$(rootDir\b|\{(rootDir|rootProject\.(rootDir|projectDir|layout|file)|project\.rootDir|settingsDir)\b)`)
	// Build logic every project applies.
	logicExt = map[string]bool{".kts": true, ".gradle": true, ".kt": true, ".groovy": true, ".java": true}
)

// gradle reads the include(...) list of the settings script and the
// project(...) references of each build script. The script is code, so it
// reads only string literals and fails on anything else: a computed include
// or project path could hide a unit or an edge.
func gradle(root, settings string) ([]gen.Unit, error) {
	text, paths, known, err := projects(root, settings)
	if err != nil {
		return nil, err
	}
	shared, common, err := buildLogic(root, settings, known)
	if err != nil {
		return nil, err
	}
	// The settings script may name projects too (gradle.beforeProject { ... }).
	more, err := projectRefs(text, settings, ":", nil)
	if err != nil {
		return nil, err
	}
	common = append(common, more...)
	gradle := tool(root, "gradlew", "gradle")
	seen := map[string]bool{}
	var units []gen.Unit
	for _, p := range paths {
		id := projectID(p)
		if err := checkDir(id); err != nil {
			return nil, fmt.Errorf("%s: include %q: %w", settings, p, err)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		dir := filepath.Join(root, filepath.FromSlash(id))
		build, err := oneOf(dir, "build.gradle.kts", "build.gradle")
		if err != nil {
			return nil, err
		}
		deps := slices.Clone(common)
		if build != "" {
			own, err := refs(root, id+"/"+build, p, known)
			if err != nil {
				return nil, err
			}
			deps = append(own, deps...)
		}
		inherited, more, err := ancestors(root, id, known)
		if err != nil {
			return nil, err
		}
		deps = append(deps, more...)
		src := slices.Concat(shared, inherited)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			src = append(src, under(id, "**"))
		}
		units = append(units, gen.Unit{
			ID:      id,
			Source:  src,
			Deps:    slices.Compact(slices.Sorted(slices.Values(without(deps, id)))),
			Command: []string{gradle, p + ":test"},
		})
	}
	return units, nil
}

// projects returns the settings script without comments, the project
// paths it includes and their unit IDs.
func projects(root, settings string) (string, []string, map[string]bool, error) {
	text, err := script(root, settings)
	if err != nil {
		return "", nil, nil, err
	}
	masked, _ := literals(text)
	if m := settingsRe.FindString(masked); m != "" {
		return "", nil, nil, fmt.Errorf("%s: %s: %w", settings, m, errUnsupported)
	}
	if applyFromRe.MatchString(masked) {
		return "", nil, nil, fmt.Errorf("%s: apply from: %w, put the settings in %s", settings, errUnsupported, settings)
	}
	paths, err := includes(text)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%s: %w", settings, err)
	}
	known := map[string]bool{}
	for i, p := range paths {
		if !strings.HasPrefix(p, ":") {
			paths[i] = ":" + p
		}
		known[projectID(paths[i])] = true
	}
	return text, paths, known, nil
}

// buildLogic returns the root context (see rootContext), the files of
// every unit, and the units the root build script and buildSrc refer to
// (subprojects { ... }, convention plugins), edges of every unit.
func buildLogic(root, settings string, known map[string]bool) (shared, common []string, err error) {
	if shared, err = rootContext(root, known); err != nil {
		return nil, nil, err
	}
	build, err := oneOf(root, "build.gradle.kts", "build.gradle")
	if err != nil {
		return nil, nil, err
	}
	if build != "" {
		if common, err = refs(root, build, ":", known); err != nil {
			return nil, nil, err
		}
	}
	if info, err := os.Stat(filepath.Join(root, "buildSrc")); err == nil && info.IsDir() {
		more, err := buildSrc(root, known)
		if err != nil {
			return nil, nil, err
		}
		common = append(common, more...)
	}
	return shared, common, nil
}

// buildSrc returns the units the build logic under buildSrc refers to
// with project(...) or a task path. Its files are in every unit's scope,
// so only the references are read.
func buildSrc(root string, known map[string]bool) ([]string, error) {
	var ids []string
	err := filepath.WalkDir(filepath.Join(root, "buildSrc"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "build" || strings.HasPrefix(n, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !logicExt[filepath.Ext(p)] {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		text, err := script(root, rel)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if err := checkLiterals(text, name, "", known); err != nil {
			return err
		}
		more, err := projectRefs(text, name, ":", known)
		ids = append(ids, more...)
		return err
	})
	return ids, err
}

// ancestors returns the build scripts of the ancestor projects of id (a
// and a/b for a/b/c) and the units they refer to. Gradle evaluates them,
// included or not, and their subprojects { ... } may configure id. An
// ancestor that is a unit is an edge too: its gradle.properties and the
// files its script hands to id (file("config/x") in subprojects { ... })
// are in its directory, which its unit covers.
func ancestors(root, id string, known map[string]bool) (files, deps []string, err error) {
	for i := strings.LastIndexByte(id, '/'); i > 0; i = strings.LastIndexByte(id[:i], '/') {
		anc := id[:i]
		if known[anc] {
			deps = append(deps, anc)
		}
		b, err := oneOf(filepath.Join(root, filepath.FromSlash(anc)), "build.gradle.kts", "build.gradle")
		if err != nil {
			return nil, nil, err
		}
		if b == "" {
			continue
		}
		r, err := refs(root, anc+"/"+b, ":"+strings.ReplaceAll(anc, "/", ":"), known)
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, r...)
		files = append([]string{under(anc, b)}, files...)
	}
	return files, deps, nil
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
// An include inside a string literal is not a statement.
func includes(text string) ([]string, error) {
	var out []string
	masked, _ := literals(text)
	for _, loc := range includeRe.FindAllStringIndex(masked, -1) {
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

// refs reads the build script name of the project at path self and returns
// the units it refers to (see projectRefs). Anything that reaches files
// neither in the project directory nor in the root context is an error:
// apply from (its script's references would be missed), parent, getParent,
// parentFile and rootProject.subprojects (and kin) in a subproject, and
// string literals that are absolute or lead, through ".." or rootDir, out
// of the root or into another project's directory.
func refs(root, name, self string, known map[string]bool) ([]string, error) {
	text, err := script(root, name)
	if err != nil {
		return nil, err
	}
	return scriptRefs(text, name, self, known)
}

func scriptRefs(text, name, self string, known map[string]bool) ([]string, error) {
	masked, _ := literals(text)
	if accessorRe.MatchString(masked) {
		return nil, fmt.Errorf("%s: type-safe project accessors (projects.*): %w, use project(\":path\")", name, errUnsupported)
	}
	if applyFromRe.MatchString(masked) {
		return nil, fmt.Errorf("%s: apply from: %w, put the script in defaults or a fragment", name, errUnsupported)
	}
	dir := projectID(self)
	m := parentRe.FindString(masked)
	if m == "" && dir != "" {
		m = rootChildrenRe.FindString(text)
	}
	if m != "" && (dir != "" || m == "parentFile") {
		return nil, fmt.Errorf("%s: %s refers to files of another project: %w", name, m, errUnsupported)
	}
	if err := checkLiterals(text, name, dir, known); err != nil {
		return nil, err
	}
	return projectRefs(text, name, self, known)
}

// checkLiterals runs checkLiteral on every string literal of a script.
func checkLiterals(text, name, dir string, known map[string]bool) error {
	masked, lits := literals(text)
	for _, l := range lits {
		if err := checkLiteral(l, dir, known); err != nil {
			return fmt.Errorf("%s: %q %w", name, l, err)
		}
	}
	// A bare project directory ("common") is a path only as the argument of
	// a call that takes one; elsewhere (include("common")) it is a name.
	for _, loc := range pathCallRe.FindAllStringIndex(masked, -1) {
		for _, r := range callArgs(masked, loc[1]) {
			a := strings.TrimSpace(text[r[0]:r[1]])
			l, j, err := literal(a, 0)
			if err == nil && j == len(a) && known[l] && l != dir && (dir == "" || !strings.HasPrefix(l, dir+"/")) {
				return fmt.Errorf("%s: %q refers to files of another project: %w", name, l, errUnsupported)
			}
		}
	}
	return nil
}

// callArgs returns the index ranges of the arguments of the call whose
// "(" ends at i in masked (string contents blanked), split at top-level
// commas: File(rootDir, "common") has two.
func callArgs(masked string, i int) [][2]int {
	var out [][2]int
	depth, start := 0, i
	for j := i; j < len(masked); j++ {
		switch masked[j] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				return append(out, [2]int{start, j})
			}
			depth--
		case ',':
			if depth == 0 {
				out = append(out, [2]int{start, j})
				start = j + 1
			}
		}
	}
	return append(out, [2]int{start, len(masked)})
}

// checkLiteral rejects a string literal of the script of the project in
// dir ("" for the root and buildSrc) that is an absolute path, holds "../"
// or a root variable inside larger text (an argument such as
// "-Dfx=../common"), or is a path that leaves the root or names another
// project's directory: through "..", rootDir, or a relative path whose
// leading segments are a project directory other than dir or one below it
// (rootProject.file("common/x"), or file("common/x") in the root script).
// Any other path stays in dir or in the root context, which covers every
// top-level directory that is not a project (see buildLogic).
func checkLiteral(l, dir string, known map[string]bool) error {
	p, err := literalPath(l, dir)
	if err != nil || p == "" {
		return err
	}
	if p == ".." || strings.HasPrefix(p, "../") {
		return fmt.Errorf("refers to files outside the root: %w", errUnsupported)
	}
	for k := range known {
		if (p == k || strings.HasPrefix(p, k+"/")) && k != dir && (dir == "" || !strings.HasPrefix(k, dir+"/")) && !strings.HasPrefix(p, dir+"/") {
			return fmt.Errorf("refers to files of another project: %w", errUnsupported)
		}
	}
	return nil
}

// literalPath returns the path a literal names, relative to the root when
// it goes through ".." or rootDir, "" when it names none worth checking, or
// an error for an absolute path or a path inside larger text.
func literalPath(l, dir string) (string, error) {
	for _, prefix := range []string{"$projectDir/", "${projectDir}/", "${project.projectDir}/"} {
		l = strings.TrimPrefix(l, prefix)
	}
	inText := func(t string) bool { return strings.Contains(t, "../") || rootVarAnyRe.MatchString(t) }
	switch {
	case rootDirRe.MatchString(l):
		p := rootDirRe.ReplaceAllString(l, "")
		if !strings.HasPrefix(p, "../") && p != ".." && inText(p) {
			return "", fmt.Errorf("holds a path inside larger text: %w", errUnsupported)
		}
		return path.Clean(p), nil
	case l == ".." || strings.HasPrefix(l, "../"):
		return path.Clean(path.Join(dir, l)), nil
	case inText(l):
		return "", fmt.Errorf("holds a path inside larger text: %w", errUnsupported)
	case outside(l):
		return "", fmt.Errorf("is an absolute path: %w", errUnsupported)
	case !strings.Contains(l, "/"):
		return "", nil // a bare name: checkLiterals reads it as a path only in a path-taking call
	}
	return l, nil
}

// projectRefs returns the unit IDs a script refers to with project(...),
// findProject(...) or evaluationDependsOn(...), relative paths resolved
// against self, the project path of the script, and every included project
// a string literal names as a path prefix (":b:jar" in dependsOn names :b).
// The root project (":") is not a unit and is skipped.
func projectRefs(text, name, self string, known map[string]bool) ([]string, error) {
	if strings.Contains(text, `"""`) || strings.Contains(text, "'''") {
		return nil, fmt.Errorf("%s: a triple-quoted string: %w", name, errUnsupported)
	}
	masked, lits := literals(text)
	var ids []string
	for _, loc := range projectRe.FindAllStringIndex(masked, -1) {
		i := skip(text, loc[1], true)
		i += len(pathArgRe.FindString(text[i:]))
		v, _, err := literal(text, i)
		if err != nil {
			return nil, fmt.Errorf("%s: %s...): %w", name, strings.TrimSpace(masked[loc[0]:loc[1]]), err)
		}
		if !strings.HasPrefix(v, ":") {
			v = strings.TrimSuffix(self, ":") + ":" + v
		}
		if v == ":" {
			continue
		}
		id := projectID(v)
		if err := checkDir(id); err != nil {
			return nil, fmt.Errorf("%s: project path %q: %w", name, v, err)
		}
		ids = append(ids, id)
	}
	for _, l := range lits {
		if !strings.HasPrefix(l, ":") {
			continue
		}
		segs := strings.Split(l[1:], ":")
		for i := range segs {
			if id := strings.Join(segs[:i+1], "/"); known[id] {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// literals returns text with the contents of its string literals blanked,
// so a pattern matches only code, and the contents themselves. It reads
// literals the way stripComments does.
func literals(text string) (string, []string) {
	b := []byte(text)
	var lits []string
	for i := 0; i < len(b); i++ {
		q := b[i]
		if q != '"' && q != '\'' {
			continue
		}
		j := i + 1
		for j < len(b) && b[j] != q && b[j] != '\n' {
			if b[j] == '\\' {
				j++
			}
			j++
		}
		j = min(j, len(b))
		lits = append(lits, text[i+1:j])
		for k := i + 1; k < j; k++ {
			b[k] = ' '
		}
		i = j
	}
	return string(b), lits
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
