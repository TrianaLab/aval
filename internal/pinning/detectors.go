package pinning

import (
	"cmp"
	"encoding/json"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
)

// composeImages checks image: keys in YAML: Compose files, Kubernetes and
// rendered Helm manifests, Helm templates and helpers (*.tpl), CI service
// definitions.
type composeImages struct{}

// lockfiles are hashed but not scanned: they hold no image: keys, and a
// large one would be an unacceptable scan finding.
var lockfiles = []string{"pnpm-lock.yaml", "conda-lock.yml", "conda-lock.yaml"}

func (composeImages) Applies(p string) bool {
	e, b := path.Ext(p), path.Base(p)
	if slices.Contains(lockfiles, b) {
		return false
	}
	return e == ".yaml" || e == ".yml" || e == ".tpl" || e == ".json" && strings.Contains(b, "compose")
}

// yamlImageRe finds a block-style image: key (quoted or not); its value is
// the rest of the line. flowImageRe finds image: keys inside flow mappings
// ({image: x, ...}), whose value ends at , or }.
var (
	yamlImageRe = regexp.MustCompile(`^\s*(?:-\s+)?(["']?image["']?)\s*:\s*(.*)$`)
	flowImageRe = regexp.MustCompile(`[{,]\s*(["']?image["']?)\s*:\s*([^,}]*)`)
	// yamlKeyRe finds a line that starts a mapping key or a list item, so it
	// is not the continuation of an image: value on the next line.
	yamlKeyRe = regexp.MustCompile(`^\s*(-(\s|$)|["']?[\w.-]+["']?\s*:(\s|$))`)
)

// Detect reads a file the YAML parser accepts with the parser alone; the
// regexes only read a file it rejects (a Helm template). Templated keys are
// a text property, so they are checked either way.
func (composeImages) Detect(in Input) []Finding {
	var out []Finding
	if docs, err := yamlDocs(in.Data); err == nil {
		for _, d := range docs {
			out = append(out, decodedImages(in, d)...)
		}
		phys := physLines(in.Data)
		for i := range out {
			out[i].Line = phys[min(out[i].Line, len(phys))-1] // clamped: see valueText
		}
	} else {
		out = textImages(in)
	}
	out = append(out, templatedKeys(in)...)
	slices.SortStableFunc(out, func(a, b Finding) int { return cmp.Or(a.Line-b.Line, strings.Compare(a.Ref, b.Ref)) })
	return slices.Compact(out)
}

// textImages finds image: keys line by line, for a file the YAML parser
// rejects.
func textImages(in Input) []Finding {
	var out []Finding
	pending := -1                                            // indent of an image: key whose value may be on the next line
	for i, s := range strings.Split(string(in.Data), "\n") { // YAML has no line continuation
		l := line{i + 1, strings.TrimSuffix(s, "\r")}
		t := strings.TrimSpace(l.text)
		if t == "" || isComment(t) {
			continue
		}
		indent := len(l.text) - len(strings.TrimLeft(l.text, " "))
		if pending >= 0 && indent > pending && !yamlKeyRe.MatchString(l.text) {
			out = append(out, yamlImage(in.Path, l.n, t)...) // a plain scalar on the next line
		}
		pending = -1
		ms := flowImageRe.FindAllStringSubmatchIndex(l.text, -1)
		if m := yamlImageRe.FindStringSubmatchIndex(l.text); m != nil {
			ms = append([][]int{m}, ms...) // and flow keys inside its value
		}
		for _, m := range ms {
			// an empty or anchor-only value continues on the next line
			v, _, _ := strings.Cut(" "+l.text[m[4]:m[5]], " #")
			if f := strings.Fields(v); len(f) == 0 || len(f) == 1 && f[0][0] == '&' {
				pending = indent
			}
			out = append(out, yamlImage(in.Path, l.n, l.text[m[4]:m[5]])...)
		}
	}
	out = append(out, escapedKeys(in)...)
	return append(out, prefixedKeys(in)...)
}

// yamlImage checks one image: value. An empty value is a nested mapping
// (Helm values) or a plain scalar on the next line, which Detect checks. A
// value Aval cannot resolve (an alias, a block scalar, a tag) is a finding,
// never skipped (fail closed).
func yamlImage(file string, n int, v string) []Finding {
	if i := strings.Index(" "+v, " #"); i >= 0 { // a comment, also right after the colon
		v = v[:i]
	}
	if v = strings.Trim(strings.TrimSpace(v), `"'`); v == "" {
		return nil
	}
	if strings.HasPrefix(v, "&") { // an anchor: the value follows it
		_, rest, ok := strings.Cut(v, " ")
		if v = strings.Trim(strings.TrimSpace(rest), `"'`); !ok || v == "" {
			return nil // an anchor on a nested mapping has no inline value
		}
	}
	if strings.ContainsAny(v[:1], "|>*!") {
		return []Finding{{File: file, Line: n, Ecosystem: "container", Ref: v, Fix: unresolvedFix}}
	}
	return image(file, n, v, nil)
}

// dockerfile checks FROM, COPY --from and remote ADD in Dockerfiles and
// Containerfiles, and RUN instructions like a shell script.
type dockerfile struct{}

func (dockerfile) Applies(p string) bool {
	b := strings.ToLower(path.Base(p))
	return strings.HasPrefix(b, "dockerfile") || strings.HasPrefix(b, "containerfile") ||
		strings.HasSuffix(b, ".dockerfile") || strings.HasSuffix(b, ".containerfile")
}

func (dockerfile) Detect(in Input) []Finding {
	lines, out := dockerLines(in.Path, string(in.Data))
	plain := make([]line, len(lines))
	for i, l := range lines {
		plain[i] = l.line
	}
	vars := assignments(plain)
	defs := dockerArgs(lines, vars)
	stages := map[string]bool{}
	directives := true // parser directives (# syntax=) come before anything else
	for _, l := range lines {
		if directives {
			if m := syntaxRe.FindStringSubmatch(l.text); m != nil {
				out = append(out, image(in.Path, l.n, m[1], nil)...)
				continue
			}
			directives = isComment(l.text)
		}
		out = append(out, redefinedFrom(in.Path, l.line, defs)...)
		f := strings.Fields(vars.expand(l.text))
		if len(f) < 2 || isComment(f[0]) {
			continue
		}
		out = append(out, instruction(in, l.n, f, stages)...)
		if strings.EqualFold(f[0], "RUN") {
			out = append(out, heredocFindings(in, l.body)...)
		}
	}
	slices.SortStableFunc(out, func(a, b Finding) int { return a.Line - b.Line })
	return out
}

// syntaxRe finds the BuildKit frontend image of a # syntax= directive.
// ponytail: looser than BuildKit (also after comments and blank lines), so
// it reports more, never less.
var syntaxRe = regexp.MustCompile(`(?i)^\s*#\s*syntax\s*=\s*(\S+)`)

// heredocFindings checks the body of a RUN heredoc like a shell script,
// numbered by its physical lines.
func heredocFindings(in Input, body []line) []Finding {
	texts := make([]string, len(body))
	for i, l := range body {
		texts[i] = l.text
	}
	fs := scanShell(in.Path, strings.Join(texts, "\n"), in.Has)
	for i := range fs {
		fs[i].Line = body[fs[i].Line-1].n
	}
	return fs
}

// instruction checks one Dockerfile instruction, split into fields.
func instruction(in Input, n int, f []string, stages map[string]bool) []Finding {
	i := 1 // flags (--platform=, --from=, --checksum=) come first
	for i < len(f) && strings.HasPrefix(f[i], "--") {
		i++
	}
	flags, rest := f[1:i], f[i:]
	var out []Finding
	switch strings.ToUpper(f[0]) {
	case "FROM":
		if len(rest) > 0 {
			out = image(in.Path, n, rest[0], stages)
		}
		if len(rest) >= 3 && strings.EqualFold(rest[1], "AS") {
			stages[strings.ToLower(rest[2])] = true
		}
	case "COPY":
		for _, fl := range flags {
			if from, ok := strings.CutPrefix(fl, "--from="); ok && strings.Trim(from, "0123456789") != "" {
				out = append(out, image(in.Path, n, from, stages)...)
			}
		}
	case "ADD":
		out = remoteAdd(in.Path, n, flags, rest)
	case "RUN":
		for _, fd := range scanShell(in.Path, strings.Join(rest, " "), in.Has) {
			fd.Line = n
			out = append(out, fd)
		}
	}
	return out
}

func remoteAdd(file string, n int, flags, srcs []string) []Finding {
	if slices.ContainsFunc(flags, func(f string) bool { return strings.HasPrefix(f, "--checksum=") }) || len(srcs) < 2 {
		return nil
	}
	var out []Finding
	for _, s := range srcs[:len(srcs)-1] {
		if strings.Contains(s, "://") || strings.HasPrefix(s, "git@") {
			out = append(out, Finding{File: file, Line: n, Ecosystem: "remote-script", Ref: s, Fix: "a remote ADD fetches whatever is there today: add --checksum=sha256:... (or a commit id for git sources)"})
		}
	}
	return out
}

// gomod: go.sum pins every module by hash; go.mod alone does not.
type gomod struct{}

func (gomod) Applies(p string) bool { return path.Base(p) == "go.mod" }

var requireRe = regexp.MustCompile(`(?m)^\s*require\b`)

func (gomod) Detect(in Input) []Finding {
	if !requireRe.Match(in.Data) || lockedBy(in, false, "go.sum") {
		return nil
	}
	return []Finding{missingLock(in, "go", "go.sum")}
}

// npm: without a lockfile, package.json ranges resolve to new releases.
type npm struct{}

func (npm) Applies(p string) bool { return path.Base(p) == "package.json" }

// maxLineLookups bounds the line numbers looked up in one package.json.
const maxLineLookups = 100

var npmLocks = []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb"}

func (npm) Detect(in Input) []Finding {
	var pkg map[string]json.RawMessage
	if err := json.Unmarshal(in.Data, &pkg); err != nil {
		return []Finding{{File: in.Path, Ecosystem: "npm", Ref: in.Path, Fix: "not valid JSON, so its dependencies cannot be checked: " + err.Error()}}
	}
	deps := map[string]string{}
	for _, k := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
		var m map[string]string
		_ = json.Unmarshal(pkg[k], &m) // ponytail: a malformed section reads as empty; npm itself rejects it
		for name, spec := range m {
			deps[name] = spec
		}
	}
	if len(deps) == 0 || lockedBy(in, true, npmLocks...) {
		return nil
	}
	var out []Finding
	for _, name := range slices.Sorted(maps.Keys(deps)) {
		if spec := deps[name]; !npmVersionPinned(spec) {
			n := 0
			if len(out) < maxLineLookups { // each lookup scans the file
				n = lineOf(in.Data, `"`+name+`"`)
			}
			out = append(out, Finding{File: in.Path, Line: n, Ecosystem: "npm", Ref: name + "@" + spec,
				Fix: "a range with no lockfile resolves to new releases: commit a lockfile (package-lock.json) and add it to scope.dependencies"})
		}
	}
	if len(out) == 0 { // exact versions, but transitive ones float
		out = append(out, missingLock(in, "npm", npmLocks[:4]...))
	}
	return out
}

// python: requirements files must pin with ==; Poetry, Pipenv and PEP 621
// [project] dependencies need a lockfile.
type python struct{}

func (python) Applies(p string) bool {
	b := path.Base(p)
	return b == "pyproject.toml" || b == "Pipfile" || strings.Contains(p, "requirements") && path.Ext(p) == ".txt"
}

// pyDepsRe is a non-empty dependencies list or an optional-dependencies
// table, as PEP 621 writes them under [project].
var (
	pyDepsRe = regexp.MustCompile(`(?m)^\s*dependencies\s*=\s*\[\s*[^\]\s]|^\s*\[project\.optional-dependencies\]`)
	pyLocks  = []string{"uv.lock", "pdm.lock", "poetry.lock"}
)

func (python) Detect(in Input) []Finding {
	switch path.Base(in.Path) {
	case "pyproject.toml":
		s := string(in.Data)
		switch {
		case strings.Contains(s, "[tool.poetry") && !lockedBy(in, false, "poetry.lock"):
			return []Finding{missingLock(in, "python", "poetry.lock")}
		case strings.Contains(s, "[project") && pyDepsRe.MatchString(s) && !lockedBy(in, true, pyLocks...):
			return []Finding{missingLock(in, "python", pyLocks...)}
		}
		return nil
	case "Pipfile":
		if !lockedBy(in, false, "Pipfile.lock") {
			return []Finding{missingLock(in, "python", "Pipfile.lock")}
		}
		return nil
	}
	return requirements(in)
}

func requirements(in Input) []Finding {
	lines := logicalLines(string(in.Data))
	hashes := strings.Contains(string(in.Data), "--hash") || strings.Contains(string(in.Data), "--require-hashes")
	var out []Finding
	for _, l := range lines {
		if f, ok := include(l.text); ok && !in.Has(path.Join(path.Dir(in.Path), f)) {
			out = append(out, Finding{File: in.Path, Line: l.n, Ecosystem: "python", Ref: f, Fix: "the included requirements file is not in scope, so its pins are not checked: add it to scope.dependencies"})
			continue
		}
		spec, opts := requirement(l.text)
		switch {
		case spec == "": // a comment, an option or only a marker
		case !pipPinned(spec):
			out = append(out, Finding{File: in.Path, Line: l.n, Ecosystem: "python", Ref: spec, Fix: "not pinned to one version: write name==1.2.3 (better, generate the file with hashes: pip-compile --generate-hashes or uv pip compile --generate-hashes)"})
		case hashes && !strings.Contains(opts, "-hash") && !strings.HasPrefix(spec, ".") && !strings.HasPrefix(spec, "/"):
			out = append(out, Finding{File: in.Path, Line: l.n, Ecosystem: "python", Ref: spec, Fix: "the file uses hashes but this requirement has none, so pip refuses it or installs it unchecked: add --hash=sha256:..."})
		}
	}
	return out
}

// include returns the file a -r, --requirement, -c or --constraint line
// names, relative to the requirements file, as pip reads it.
func include(s string) (string, bool) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return "", false
	}
	for _, o := range []string{"--requirement", "--constraint", "-r", "-c"} {
		switch v, ok := strings.CutPrefix(f[0], o); {
		case !ok:
		case v == "" && len(f) > 1:
			return f[1], true
		case v != "" && (len(o) == 2 || v[0] == '='):
			return cmp.Or(strings.TrimPrefix(v, "="), strings.TrimSpace(s)), true // an empty name is reported by its line
		}
	}
	return "", false
}

// requirement splits one requirements line into the requirement, without
// spaces and environment marker, and its options. Comments, blank lines and
// option lines other than -e give an empty requirement.
func requirement(s string) (spec, opts string) {
	if i := strings.Index(s, "#"); i >= 0 && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	spec, opts, _ = strings.Cut(s, " -")
	if strings.HasPrefix(s, "-") { // an option line; -e names a requirement
		f := strings.Fields(s)
		if len(f) < 2 || (f[0] != "-e" && f[0] != "--editable") {
			return "", ""
		}
		spec, opts = f[1], ""
	}
	spec, _, _ = strings.Cut(spec, ";") // environment marker
	return strings.Join(strings.Fields(spec), ""), opts
}

// cargo: Cargo.lock pins every crate; in a workspace it sits at the root.
type cargo struct{}

func (cargo) Applies(p string) bool { return path.Base(p) == "Cargo.toml" }

var cargoDepsRe = regexp.MustCompile(`(?m)^\s*\[(.+\.)?(dev-|build-)?dependencies(\..+)?\]`)

func (cargo) Detect(in Input) []Finding {
	if !cargoDepsRe.Match(in.Data) || lockedBy(in, true, "Cargo.lock") {
		return nil
	}
	return []Finding{missingLock(in, "rust", "Cargo.lock")}
}

// java: Maven and Gradle versions that move: SNAPSHOT, LATEST, RELEASE,
// dynamic (1.+) and ranges ([1.0,2.0)).
type java struct{}

func (java) Applies(p string) bool {
	b := path.Base(p)
	return b == "pom.xml" || b == "build.gradle" || b == "build.gradle.kts" || strings.HasSuffix(b, ".versions.toml")
}

var (
	pomVersionRe    = regexp.MustCompile(`<([\w.-]*[vV]ersion[\w.-]*)>\s*([^<]+?)\s*</`)
	gradleCoordRe   = regexp.MustCompile(`["']([\w.-]+:[\w.-]+:([^"'\s]+))["']`)
	gradleVersionRe = regexp.MustCompile(`\b(?:version|require|strictly|prefer)\s*[=(:]?\s*["']([^"']+)["']`)
	tomlVersionRe   = regexp.MustCompile(`^\s*[\w.-]+\s*=\s*["']([^"']+)["']`)
)

func mutableJavaVersion(v string) bool {
	return strings.Contains(v, "SNAPSHOT") || strings.Contains(v, "+") || strings.HasPrefix(v, "latest.") ||
		v == "LATEST" || v == "RELEASE" || strings.ContainsAny(v[:1], "[(]")
}

func (java) Detect(in Input) []Finding {
	var out []Finding
	add := func(n int, ref, v string) {
		if v != "" && mutableJavaVersion(v) {
			out = append(out, Finding{File: in.Path, Line: n, Ecosystem: "java", Ref: ref, Fix: "version " + v + " moves (snapshot, latest, dynamic or range): pin a release version such as 1.2.3"})
		}
	}
	b := path.Base(in.Path)
	for _, l := range logicalLines(string(in.Data)) {
		switch m := gradleCoordRe.FindStringSubmatch(l.text); {
		case b == "pom.xml":
			for _, m := range pomVersionRe.FindAllStringSubmatch(l.text, -1) {
				add(l.n, m[0], m[2])
			}
		case m != nil:
			add(l.n, m[1], m[2])
		default:
			re := gradleVersionRe
			if strings.HasSuffix(b, ".toml") {
				re = tomlVersionRe
			}
			if m := re.FindStringSubmatch(l.text); m != nil {
				add(l.n, strings.TrimSpace(l.text), m[1])
			}
		}
	}
	return out
}

// ruby: Gemfile needs Gemfile.lock (gems.rb needs gems.locked).
type ruby struct{}

func (ruby) Applies(p string) bool { b := path.Base(p); return b == "Gemfile" || b == "gems.rb" }

func (ruby) Detect(in Input) []Finding {
	lock := "Gemfile.lock"
	if path.Base(in.Path) == "gems.rb" {
		lock = "gems.locked"
	}
	if lockedBy(in, false, lock) {
		return nil
	}
	return []Finding{missingLock(in, "ruby", lock)}
}

// php: composer.json with requirements needs composer.lock.
type php struct{}

func (php) Applies(p string) bool { return path.Base(p) == "composer.json" }

func (php) Detect(in Input) []Finding {
	if !strings.Contains(string(in.Data), `"require`) || lockedBy(in, false, "composer.lock") {
		return nil
	}
	return []Finding{missingLock(in, "php", "composer.lock")}
}

// gitmodules: a submodule that tracks a branch is updated to wherever the
// branch points.
type gitmodules struct{}

func (gitmodules) Applies(p string) bool { return path.Base(p) == ".gitmodules" }

var branchRe = regexp.MustCompile(`^\s*branch\s*=\s*(\S+)`)

func (gitmodules) Detect(in Input) []Finding {
	var out []Finding
	for _, l := range logicalLines(string(in.Data)) {
		if m := branchRe.FindStringSubmatch(l.text); m != nil {
			out = append(out, Finding{File: in.Path, Line: l.n, Ecosystem: "git", Ref: "branch = " + m[1], Fix: "the submodule tracks a branch: remove the branch line and rely on the commit recorded in the repository"})
		}
	}
	return out
}
