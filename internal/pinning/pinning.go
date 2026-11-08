// Package pinning finds mutable references in a test's inputs: names whose
// content can change while the declared text, and so the fingerprint, stays
// the same. A container tag can be repushed, a manifest without a lockfile
// resolves to new releases, a script piped from the network changes upstream.
//
// Detection is static and offline: one small Detector per ecosystem reads the
// in-scope files it knows by name, plus runner.command. Nothing is fetched or
// executed. A file that cannot be read, or is larger than MaxFileSize, is
// reported as a finding: what Aval cannot check, it does not vouch for
// (invariant 3).
package pinning

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// MaxFileSize bounds what a detector reads from one file.
const MaxFileSize = 1 << 20

// CommandFile is the File of findings in runner.command.
const CommandFile = "runner.command"

// Finding is one mutable reference.
type Finding struct {
	// File is the in-scope path, slash-separated and relative to the
	// contract root, or CommandFile.
	File string
	// Line is 1-based; 0 when the finding is about the whole file.
	Line int
	// Ecosystem names the detector's domain: container, go, npm, python,
	// rust, java, ruby, php, os-package, helm, git, remote-script, scan.
	Ecosystem string
	// Ref is the reference as written. scope.allowMutable matches on it.
	Ref string
	// Fix says what to pin.
	Fix string
	// Allowed is the reason given in scope.allowMutable, empty when the
	// finding is not acknowledged.
	Allowed string
}

func (f Finding) String() string {
	loc := f.File
	if f.Line > 0 {
		loc += ":" + strconv.Itoa(f.Line)
	}
	s := fmt.Sprintf("%s (%s) %s: %s", loc, f.Ecosystem, f.Ref, f.Fix)
	if f.Allowed != "" {
		s += " (allowed: " + f.Allowed + ")"
	}
	return s
}

// Input is one in-scope file handed to a Detector.
type Input struct {
	// Path is slash-separated and relative to the contract root.
	Path string
	Data []byte
	// Has reports whether a path (same form as Path) is in scope.
	Has func(string) bool
}

// Detector finds mutable references for one ecosystem.
type Detector interface {
	// Applies reports whether the detector reads the file at path.
	Applies(path string) bool
	// Detect returns the findings in one file.
	Detect(in Input) []Finding
}

// Detectors are every ecosystem Scan checks.
var Detectors = []Detector{
	composeImages{}, kustomize{}, dockerfile{}, shellScripts{}, gomod{}, npm{}, python{},
	cargo{}, java{}, ruby{}, php{}, gitmodules{}, testcontainers{},
}

// Scan reads the in-scope files (slash-separated, relative to the root of
// fsys) that some
// detector applies to, and runner.command, and returns every finding sorted
// by file and line.
func Scan(fsys fs.FS, files []string, command []string) []Finding {
	inScope := make(map[string]bool, len(files))
	for _, f := range files {
		inScope[f] = true
	}
	has := func(p string) bool { return inScope[p] }
	manifests := kustomizeJSON(fsys, files, has)
	var out []Finding
	for _, rel := range files {
		ds := detectorsFor(fsys, rel, manifests[rel])
		if len(ds) == 0 {
			continue
		}
		data, err := readBounded(fsys, rel)
		if err != nil && len(ds) == 1 && ds[0] == Detector(testcontainers{}) {
			// ponytail: a large source file (a vendored bundle) is not read;
			// a finding for each would make every such scope a miss.
			continue
		}
		if err != nil {
			out = append(out, Finding{File: rel, Ecosystem: "scan", Ref: rel, Fix: err.Error() + ": Aval cannot check its references; shrink or split the file"})
			continue
		}
		for _, d := range ds {
			out = append(out, d.Detect(Input{Path: rel, Data: data, Has: has})...)
		}
	}
	out = append(out, scanShell(CommandFile, strings.Join(command, " "), has)...)
	slices.SortStableFunc(out, func(a, b Finding) int {
		if a.File != b.File {
			return strings.Compare(a.File, b.File)
		}
		return a.Line - b.Line
	})
	return slices.CompactFunc(out, func(a, b Finding) bool { return a == b })
}

// detectorsFor returns the detectors that read rel; manifest says a
// kustomization lists it in resources.
func detectorsFor(fsys fs.FS, rel string, manifest bool) []Detector {
	var ds []Detector
	for _, d := range Detectors {
		if d.Applies(rel) {
			ds = append(ds, d)
		}
	}
	if manifest && !(composeImages{}).Applies(rel) {
		ds = append(ds, composeImages{})
	}
	if len(ds) == 0 && path.Ext(rel) == "" && isShellScript(fsys, rel) {
		ds = []Detector{shellScripts{}}
	}
	return ds
}

// kustomizeJSON returns the in-scope .json files that an in-scope
// kustomization lists in resources: manifests composeImages checks. A
// kustomization it cannot read is reported by the kustomize detector.
func kustomizeJSON(fsys fs.FS, files []string, has func(string) bool) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		if !(kustomize{}).Applies(f) {
			continue
		}
		data, _ := readBounded(fsys, f)
		ks, _ := kustomizations(data)
		for _, k := range ks {
			for _, r := range k.Resources {
				s, _ := r.(string)
				if p := path.Join(path.Dir(f), s); path.Ext(p) == ".json" && has(p) {
					out[p] = true
				}
			}
		}
	}
	return out
}

func readBounded(fsys fs.FS, p string) ([]byte, error) {
	f, err := fsys.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only: a close error loses nothing
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err == nil && len(data) > MaxFileSize {
		err = errors.New("larger than 1 MiB")
	}
	return data, err
}

// line is one logical line: physical lines joined at a trailing backslash.
type line struct {
	n    int // 1-based number of the first physical line
	text string
}

func logicalLines(data string) []line {
	var out []line
	var cur strings.Builder
	start := 0
	for i, l := range strings.Split(data, "\n") {
		if cur.Len() == 0 {
			start = i + 1
		}
		l = strings.TrimSuffix(l, "\r")
		if strings.HasSuffix(l, `\`) {
			cur.WriteString(strings.TrimSuffix(l, `\`) + " ")
			continue
		}
		cur.WriteString(l)
		out = append(out, line{start, cur.String()})
		cur.Reset()
	}
	if cur.Len() > 0 { // a continuation at the end of the text
		out = append(out, line{start, cur.String()})
	}
	return out
}

// isComment reports whether a trimmed line is a #-comment.
func isComment(s string) bool { return strings.HasPrefix(s, "#") }

var (
	assignRe = regexp.MustCompile(`^\s*(?:export\s+|ARG\s+|ENV\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:\?=|::=|:=|=)\s*(.*?)\s*$`)
	varRe    = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)|\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)
)

// vars are the variable definitions of one file and what is left of its
// expansion budget.
type vars struct {
	m      map[string]string
	budget int
}

// maxExpand bounds what expansion may write for one file, so definitions
// that refer to each other (A=$A$A) or a long value used on every line
// cannot blow up memory or time.
const maxExpand = 4 * MaxFileSize

// assignments collects NAME=value, NAME := value (Make) and ARG NAME=value
// (Dockerfile) definitions, so a version kept in a variable is checked.
// ponytail: last definition wins, no scoping or conditionals; a variable it
// cannot resolve stays as written and is reported as unpinned.
func assignments(lines []line) *vars {
	v := &vars{m: map[string]string{}, budget: maxExpand}
	for _, l := range lines {
		if m := assignRe.FindStringSubmatch(l.text); m != nil {
			v.m[m[1]] = strings.Trim(m[2], `"'`)
		}
	}
	return v
}

// expand substitutes the known variables in s, up to three levels deep. An
// unknown $(NAME) or ${NAME} becomes $NAME, so no parenthesis is left to
// split a command on. Once the budget is spent, s is returned as it is: its
// variables stay unresolved and are reported as unpinned (fail closed).
func (v *vars) expand(s string) string {
	s = strings.ReplaceAll(s, "$$", "$") // Make escapes
	for range 3 {
		ms := varRe.FindAllStringSubmatchIndex(s, -1)
		if ms == nil {
			break
		}
		var b strings.Builder
		last := 0
		for _, m := range ms {
			name := s[max(m[2], m[4], m[6]):max(m[3], m[5], m[7])]
			val, ok := v.m[name]
			if !ok {
				val = "$" + name
			}
			b.WriteString(s[last:m[0]])
			b.WriteString(val)
			last = m[1]
			if b.Len() > v.budget {
				return s
			}
		}
		b.WriteString(s[last:])
		v.budget -= b.Len()
		s = b.String()
	}
	return s
}

func lineOf(data []byte, needle string) int {
	i := strings.Index(string(data), needle)
	if i < 0 {
		return 0
	}
	return strings.Count(string(data[:i]), "\n") + 1
}

// lockedBy reports whether one of the lockfiles is in scope next to the
// manifest or, with ancestors, in a parent directory (a workspace root).
func lockedBy(in Input, ancestors bool, locks ...string) bool {
	dir := path.Dir(in.Path)
	for {
		for _, l := range locks {
			if in.Has(path.Join(dir, l)) {
				return true
			}
		}
		if !ancestors || dir == "." || dir == "/" {
			return false
		}
		dir = path.Dir(dir)
	}
}

func missingLock(in Input, eco string, locks ...string) Finding {
	return Finding{File: in.Path, Ecosystem: eco, Ref: in.Path,
		Fix: "no lockfile in scope, so every build may resolve other versions: commit " + strings.Join(locks, " or ") + " and add it to scope.dependencies"}
}

var (
	digestRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	hexRe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// image checks one container image reference. stages are names that refer
// to an earlier build stage, not to a registry.
func image(file string, n int, ref string, stages map[string]bool) []Finding {
	switch {
	case ref == "" || strings.EqualFold(ref, "scratch") || stages[strings.ToLower(ref)] || digestRe.MatchString(ref):
		return nil
	case strings.Contains(ref, "$") || strings.Contains(ref, "{{"):
		return []Finding{{File: file, Line: n, Ecosystem: "container", Ref: ref, Fix: "image comes from a variable or template Aval cannot resolve: write the reference with its digest in the scope (name@sha256:...)"}}
	}
	return []Finding{{File: file, Line: n, Ecosystem: "container", Ref: ref, Fix: "a tag can be repushed: pin the image by digest (name:tag@sha256:...)"}}
}
