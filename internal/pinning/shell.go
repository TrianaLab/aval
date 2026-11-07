package pinning

import (
	"cmp"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// shellScripts checks scripts and Makefiles for fetches at run time.
type shellScripts struct{}

func (shellScripts) Applies(p string) bool {
	switch b := path.Base(p); b {
	case "Makefile", "makefile", "GNUmakefile":
		return true
	default:
		return slices.Contains([]string{".sh", ".bash", ".zsh", ".mk"}, path.Ext(b))
	}
}

func (shellScripts) Detect(in Input) []Finding { return scanShell(in.Path, string(in.Data), in.Has) }

var shebangRe = regexp.MustCompile(`^#!\s*\S*/(env\s+(-\S+\s+)*)?(ba|z|k|da)?sh\b`)

// isShellScript reports whether the file at p starts with a sh, bash, zsh,
// ksh or dash shebang. Scan asks it for in-scope files without an extension
// (./setup, bin/ci). A file it cannot open counts as a script, so Scan
// reports the read error instead of skipping the file (fail closed).
func isShellScript(fsys fs.FS, p string) bool {
	f, err := fsys.Open(p)
	if err != nil {
		return true
	}
	defer func() { _ = f.Close() }() // read-only: a close error loses nothing
	head := make([]byte, 128)
	n, _ := io.ReadFull(f, head)
	return shebangRe.Match(head[:n])
}

var (
	// The pipe may follow quotes, ; or & (a URL query string), but not
	// another | (curl ... || sh fallback runs nothing it downloaded).
	pipeShellRe = regexp.MustCompile(`\b(curl|wget)\b[^\n]*?[^|]\|\s*((sudo|env)\s+(-\S+\s+)*)?(\S*/)?(env\s+)?((ba|z|k|da|fi)?sh|python[0-9.]*|node|ruby|perl)\b`)
	subShellRe  = regexp.MustCompile(`(^|\s)((ba|z|k|da)?sh|source|eval|\.|python[0-9.]*|node|ruby|perl)\s[^|;&]*?(<\(|\$\()\s*(curl|wget)\b`)
	// latestRe is a download of whatever release is newest.
	latestRe = regexp.MustCompile(`\b(curl|wget)\b[^\n]*?/releases/latest\b\S*`)
	// cloneRe is a clone; checkoutRe is a commit id checked out (or
	// fetched) on the same line. Each clone needs its own.
	cloneRe    = regexp.MustCompile(`\bclone\b`)
	checkoutRe = regexp.MustCompile(`\b(checkout|reset\s+--hard|switch\s+--detach|fetch)\s+([^\s;&|]+\s+)*[0-9a-f]{40}\b`)
	segmentSep = func(r rune) bool { return strings.ContainsRune("|;&()`\n", r) }
)

// scanShell checks every logical line of shell-like text: a script, a
// Makefile, a Dockerfile RUN instruction or runner.command. Lines are split
// into simple commands on | ; & ( ) and backticks.
// ponytail: a lexer-free heuristic. Quoting is ignored, so a quoted "; " also
// splits; a misread token is reported as unpinned (fail closed), never
// skipped.
func scanShell(file, text string, has func(string) bool) []Finding {
	lines := logicalLines(text)
	vars := assignments(lines)
	var out []Finding
	for _, l := range lines {
		s := strings.TrimLeft(strings.TrimSpace(l.text), "@-+ \t")
		if s == "" || isComment(s) {
			continue
		}
		s = vars.expand(s)
		at := func(eco, ref, fix string) {
			// An empty operand (--package=) is reported by its line; quotes
			// are dropped so scope.allowMutable matches the plain text.
			ref = strings.NewReplacer(`"`, "", `'`, "").Replace(cmp.Or(ref, s))
			out = append(out, Finding{File: file, Line: max(l.n, 1), Ecosystem: eco, Ref: ref, Fix: fix})
		}
		if m := pipeShellRe.FindString(s); m != "" {
			at("remote-script", m, "a script piped from the network changes upstream: vendor it into the scope or download a fixed version and check its sha256 before running it")
		} else if m := subShellRe.FindString(s); m != "" {
			at("remote-script", m, "a script run from the network changes upstream: vendor it into the scope or download a fixed version and check its sha256 before running it")
		} else if m := latestRe.FindString(s); m != "" {
			at("remote-script", m, "releases/latest is whatever release is newest: download a fixed version and check its sha256")
		}
		for _, seg := range strings.FieldsFunc(s, segmentSep) {
			if argv := command(tokens(seg)); len(argv) > 0 {
				if rule := shellRules[path.Base(argv[0])]; rule != nil {
					rule(shellCtx{at: at, has: has, line: s}, skipGlobal(argv))
				}
			}
		}
	}
	return out
}

// tokens splits a simple command into words and drops redirections (>f,
// 2> f, < f), so a version range such as 'x>=2' stays one word.
func tokens(seg string) []string {
	var out []string
	target := false
	for _, t := range strings.Fields(seg) {
		if op := strings.TrimLeft(t, "0123456789"); strings.HasPrefix(op, ">") || strings.HasPrefix(op, "<") {
			target = strings.Trim(op, "<>|") == "" // a bare operator: the target is the next word
			continue
		}
		if target {
			target = false
			continue
		}
		if t = strings.Trim(t, `"'[],`); t != "" {
			out = append(out, t)
		}
	}
	return out
}

var (
	wrappers = map[string]bool{"sudo": true, "env": true, "exec": true, "time": true, "nohup": true, "command": true, "xargs": true, "then": true, "do": true, "else": true}
	shells   = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}
	envRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// command drops what precedes the program: VAR=value, wrappers such as sudo
// or env, and sh -c.
func command(t []string) []string {
	for len(t) > 0 {
		if envRe.MatchString(t[0]) {
			t = t[1:]
			continue
		}
		if w := path.Base(t[0]); !wrappers[w] && !shells[w] {
			return t
		}
		t = t[1:]
		for len(t) > 0 && strings.HasPrefix(t[0], "-") {
			t = t[1:]
		}
	}
	return nil
}

// globalValued are options that take a value before a subcommand.
var globalValued = []string{"-C", "-c", "-o", "-H", "--host", "--context", "--config", "--prefix", "--dir", "--cwd", "--root", "-X", "--repository", "--git-dir", "--work-tree", "--cache-dir", "--log-level"}

// skipGlobal drops the options between a program and its subcommand
// (apk --no-cache add, apt-get -y install, git -C dir clone), so a rule sees
// the subcommand at t[1]. npx, bunx, docker-compose and python read their
// options themselves.
// ponytail: an unknown valued global option (tool --opt value sub) still
// hides the subcommand; add it to globalValued when one shows up.
func skipGlobal(t []string) []string {
	switch path.Base(t[0]) {
	case "npx", "bunx", "docker-compose", "python", "python3":
		return t
	}
	i := 1
	for i < len(t) && strings.HasPrefix(t[i], "-") {
		if slices.Contains(globalValued, t[i]) {
			i++
		}
		i++
	}
	return append([]string{t[0]}, t[min(i, len(t)):]...)
}

type shellCtx struct {
	at   func(eco, ref, fix string)
	has  func(string) bool
	line string // the whole expanded line, for context such as a commit id
}

var shellRules = map[string]func(shellCtx, []string){
	"go": goCmd, "npm": jsCmd, "pnpm": jsCmd, "yarn": jsCmd, "bun": jsCmd,
	"npx": dlxCmd, "bunx": dlxCmd, "pip": pipCmd, "pip3": pipCmd, "pipx": pipCmd,
	"uv": uvCmd, "python": pythonCmd, "python3": pythonCmd,
	"apt-get": osCmd, "apt": osCmd, "apk": osCmd,
	"dnf": rpmCmd, "yum": rpmCmd, "microdnf": rpmCmd, "zypper": rpmCmd,
	"kubectl": kubectlCmd, "helm": helmCmd,
	"gem": gemCmd, "cargo": cargoCmd,
	"docker": dockerCmd, "podman": dockerCmd, "nerdctl": dockerCmd, "docker-compose": composeCmd,
	"git": gitCmd,
}

// args returns the operands after the first n tokens: tokens that are not
// flags and not the value of a flag in valued.
func args(t []string, n int, valued ...string) []string {
	var out []string
	for i := n; i < len(t); i++ {
		switch a := t[i]; {
		case slices.Contains(valued, a):
			i++
		case strings.HasPrefix(a, "-"):
		default:
			out = append(out, a)
		}
	}
	return out
}

func sub(t []string, i int, names ...string) bool { return len(t) > i && slices.Contains(names, t[i]) }

var goVersionRe = regexp.MustCompile(`^(v\d+\.\d+\.\d+([-+.].*)?|[0-9a-f]{7,40}|none)$`)

func goCmd(c shellCtx, t []string) {
	if !sub(t, 1, "install", "run", "get") {
		return
	}
	for _, a := range args(t, 2, "-o", "-C", "-mod", "-tags", "-ldflags", "-gcflags", "-exec", "-p", "-pkgdir", "-modfile", "-overlay", "-toolexec") {
		i := strings.LastIndex(a, "@")
		switch {
		case i > 0 && !goVersionRe.MatchString(a[i+1:]):
			c.at("go", a, "@"+a[i+1:]+" is not a fixed release version: use pkg@v1.2.3")
		case i < 0 && t[1] == "get" && strings.Contains(strings.Split(a, "/")[0], ".") && !strings.HasPrefix(a, "."):
			c.at("go", a, "go get without a version fetches the newest release: add @v1.2.3")
		}
	}
}

var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// npmPinned reports whether an npm command operand (name@version, a path
// or a URL) names one fixed version or local code.
func npmPinned(spec string) bool {
	if i := strings.LastIndex(spec, "@"); i > 0 && !strings.Contains(spec[:i], "://") && !strings.Contains(spec, "#") {
		return npmVersionPinned(spec[i+1:])
	}
	return strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") || strings.Contains(spec, ":") && npmVersionPinned(spec)
}

// npmVersionPinned reports whether a package.json version spec is one fixed
// version, a full commit id or local code.
func npmVersionPinned(v string) bool {
	switch {
	case strings.HasPrefix(v, "file:") || strings.HasPrefix(v, "link:") || strings.HasPrefix(v, "workspace:") ||
		strings.HasPrefix(v, ".") || strings.HasPrefix(v, "/"):
		return true
	case strings.HasPrefix(v, "npm:"): // alias: npm:name@1.2.3
		return npmPinned(strings.TrimPrefix(v, "npm:"))
	case strings.Contains(v, "#"): // git: only a full commit id is fixed
		return hexRe.MatchString(v[strings.LastIndex(v, "#")+1:])
	}
	return semverRe.MatchString(strings.TrimLeft(v, "v="))
}

func jsCmd(c shellCtx, t []string) {
	n := 2
	switch {
	case sub(t, 1, "global") && sub(t, 2, "add"): // yarn global add
		n = 3
	case sub(t, 1, "dlx", "x", "exec"): // pnpm dlx, bun x, npm exec
		dlxCmd(c, t[1:])
		return
	case !sub(t, 1, "install", "i", "add"):
		return
	}
	for _, a := range args(t, n, "--prefix", "-w", "--workspace", "--registry", "--cache", "-C", "--dir", "--filter", "--cwd") {
		if !npmPinned(a) {
			c.at("npm", a, "installs whatever version is newest: pin an exact version (name@1.2.3) or install from the lockfile (npm ci)")
		}
	}
}

func dlxCmd(c shellCtx, t []string) {
	var pkgs []string // -p names the packages; else the first operand is one
	for i, a := range t {
		switch {
		case (a == "-p" || a == "--package") && i+1 < len(t):
			pkgs = append(pkgs, t[i+1])
		case strings.HasPrefix(a, "--package="):
			pkgs = append(pkgs, strings.TrimPrefix(a, "--package="))
		}
	}
	if a := args(t, 1, "-c", "--call"); len(pkgs) == 0 && len(a) > 0 {
		pkgs = a[:1]
	}
	for _, p := range pkgs {
		if !npmPinned(p) {
			c.at("npm", p, "runs whatever version is newest: pin an exact version (name@1.2.3)")
		}
	}
}

var pyVCSRe = regexp.MustCompile(`@[0-9a-f]{40}(#|$)`)

// pipPinned reports whether a requirement names one fixed version or local
// code. Spaces and environment markers are already removed.
func pipPinned(spec string) bool {
	switch {
	case strings.Contains(spec, "+") && strings.Contains(spec, "://"): // git+https://...
		return pyVCSRe.MatchString(spec)
	case strings.Contains(spec, "://"):
		return strings.Contains(spec, "#sha256=")
	case strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") || strings.HasSuffix(spec, ".whl"):
		return true
	}
	i := strings.Index(spec, "==")
	return i > 0 && !strings.Contains(spec, "*") && !strings.ContainsAny(spec[i+2:], "<>,!~")
}

var pipValued = []string{"-r", "--requirement", "-c", "--constraint", "-e", "--editable", "-t", "--target", "-i", "--index-url", "--extra-index-url", "--prefix", "--root", "-f", "--find-links", "--python", "-p"}

func pipCmd(c shellCtx, t []string) {
	t = skipGlobal(t) // python -m pip --isolated install, uv pip -q install
	if !sub(t, 1, "install") {
		return
	}
	for i, a := range t {
		f, ok := "", false
		switch {
		case (a == "-r" || a == "--requirement" || a == "-c" || a == "--constraint") && i+1 < len(t):
			f, ok = t[i+1], true
		case strings.HasPrefix(a, "--requirement=") || strings.HasPrefix(a, "--constraint="):
			_, f, ok = strings.Cut(a, "=")
		}
		if ok && !c.has(path.Clean(f)) {
			c.at("python", f, "the requirements file is not in scope, so its pins are not checked: add it to scope.dependencies")
		}
	}
	for _, a := range args(t, 2, pipValued...) {
		if !pipPinned(a) {
			c.at("python", a, "installs whatever version is newest: pin it (name==1.2.3) or install from a requirements file with pins and hashes")
		}
	}
}

func uvCmd(c shellCtx, t []string) {
	if sub(t, 1, "pip", "tool") {
		pipCmd(c, t[1:])
	}
}

func pythonCmd(c shellCtx, t []string) {
	if sub(t, 1, "-m") && sub(t, 2, "pip") {
		pipCmd(c, t[2:])
	}
}

func osCmd(c shellCtx, t []string) {
	if !sub(t, 1, "install", "add") {
		return
	}
	for _, a := range args(t, 2, "-t", "--target-release", "-o", "-X", "--repository", "--virtual", "--root") {
		if !strings.Contains(a, "=") {
			c.at("os-package", a, "installs whatever version the mirror has today: pin it (name=1.2.3-r0) or bake it into an image pinned by digest")
		}
	}
}

// rpmVersionRe is a name-version-release pin (curl-7.76.1-26.el9).
var rpmVersionRe = regexp.MustCompile(`-\d[\w.~^+]*-\d[\w.~^+]*$`)

func rpmCmd(c shellCtx, t []string) {
	if !sub(t, 1, "install", "in") {
		return
	}
	for _, a := range args(t, 2, "-c", "--config", "--repo", "--repoid", "--enablerepo", "--disablerepo", "--setopt", "--releasever", "--installroot", "-x", "--exclude", "-r", "--from") {
		if !rpmVersionRe.MatchString(a) && !strings.Contains(a, "=") && !strings.HasPrefix(a, ".") && !strings.HasPrefix(a, "/") {
			c.at("os-package", a, "installs whatever version the mirror has today: pin it (name-1.2.3-1.el9) or bake it into an image pinned by digest")
		}
	}
}

// kubectlCmd checks the image of kubectl run and create --image.
func kubectlCmd(c shellCtx, t []string) {
	for i, a := range t {
		v, ok := strings.CutPrefix(a, "--image=")
		if a == "--image" && i+1 < len(t) {
			v, ok = t[i+1], true
		}
		if ok {
			for _, f := range image("", 0, v, nil) {
				c.at(f.Ecosystem, f.Ref, f.Fix)
			}
		}
	}
}

var helmValued = []string{"-n", "--namespace", "-f", "--values", "--set", "--set-string", "--set-file", "--set-json", "--version", "--repo", "--kube-context", "--kubeconfig", "--timeout", "-o", "--output", "--post-renderer", "--description", "--username", "--password", "--ca-file", "--cert-file", "--key-file", "--registry-config"}

// helmCmd checks that helm install and upgrade of a chart that is not in
// scope name an exact --version.
func helmCmd(c shellCtx, t []string) {
	if !sub(t, 1, "install", "upgrade") {
		return
	}
	a := args(t, 2, helmValued...)
	if len(a) == 0 {
		return
	}
	chart := a[min(1, len(a)-1)] // install NAME CHART, upgrade RELEASE CHART, install CHART --generate-name
	if c.has(path.Join(path.Clean(chart), "Chart.yaml")) || c.has(path.Clean(chart)) {
		return // local code in scope
	}
	v := ""
	for i, x := range t {
		if x == "--version" && i+1 < len(t) {
			v = t[i+1]
		} else if s, ok := strings.CutPrefix(x, "--version="); ok {
			v = s
		}
	}
	if !semverRe.MatchString(strings.TrimPrefix(v, "v")) {
		c.at("helm", chart, "a chart without an exact --version installs whatever release is newest: add --version 1.2.3 (or render the chart into the scope)")
	}
}

func gemCmd(c shellCtx, t []string) {
	if sub(t, 1, "install") && !slices.ContainsFunc(t, func(a string) bool { return a == "-v" || strings.HasPrefix(a, "--version") }) {
		for _, a := range args(t, 2, "-i", "--install-dir", "-n", "--bindir", "--source") {
			c.at("ruby", a, "installs whatever version is newest: add --version 1.2.3")
		}
	}
}

func cargoCmd(c shellCtx, t []string) {
	if !sub(t, 1, "install") || slices.ContainsFunc(t, func(a string) bool {
		return a == "--version" || a == "--vers" || a == "--rev" || strings.HasPrefix(a, "--version=")
	}) {
		return
	}
	for _, a := range args(t, 2, "--git", "--branch", "--tag", "--root", "--index", "--registry", "--path", "-j", "--jobs", "--target", "--features", "-F", "--bin", "--example", "--profile") {
		if !strings.Contains(a, "@") {
			c.at("rust", a, "installs whatever version is newest: add --version 1.2.3 (or name@1.2.3)")
		}
	}
}

var dockerValued = []string{
	"-e", "--env", "--env-file", "-v", "--volume", "-p", "--publish", "--name", "-w", "--workdir",
	"--network", "--net", "--entrypoint", "-u", "--user", "--platform", "--mount", "-l", "--label",
	"--add-host", "-h", "--hostname", "--cpus", "-m", "--memory", "--pull", "--restart", "--tmpfs",
	"--device", "--cap-add", "--cap-drop", "--security-opt", "--ulimit", "--shm-size", "--ipc",
	"--pid", "--gpus", "--runtime", "--dns", "--expose", "--link", "--log-driver", "--log-opt",
	"--cidfile", "--stop-signal", "--stop-timeout", "--health-cmd", "--group-add", "--label-file",
}

func dockerCmd(c shellCtx, t []string) {
	if sub(t, 1, "container", "image") {
		t = t[1:]
	}
	switch {
	case sub(t, 1, "compose"):
		composeCmd(c, t[1:])
	case sub(t, 1, "run", "create", "pull"):
		if a := args(t, 2, dockerValued...); len(a) > 0 {
			for _, f := range image("", 0, a[0], nil) {
				c.at(f.Ecosystem, f.Ref, f.Fix)
			}
		}
	}
}

var composeDefaults = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// composeCmd checks that the compose files a command uses are in scope:
// the compose detector then checks their images.
// ponytail: paths are taken relative to the contract root, where
// runner.command runs; a script that changes directory first is reported.
func composeCmd(c shellCtx, t []string) {
	var files []string
	for i, a := range t {
		switch {
		case (a == "-f" || a == "--file") && i+1 < len(t):
			files = append(files, t[i+1])
		case strings.HasPrefix(a, "--file="):
			files = append(files, strings.TrimPrefix(a, "--file="))
		}
	}
	if len(files) == 0 {
		if !slices.ContainsFunc(composeDefaults, c.has) {
			c.at("container", "compose.yaml", "the compose file is not in scope, so its images are not checked: add it to scope.source")
		}
		return
	}
	for _, f := range files {
		if !c.has(path.Clean(f)) {
			c.at("container", f, "the compose file is not in scope, so its images are not checked: add it to scope.source")
		}
	}
}

func gitCmd(c shellCtx, t []string) {
	switch {
	case sub(t, 1, "clone") && len(cloneRe.FindAllString(c.line, -1)) > len(checkoutRe.FindAllString(c.line, -1)):
		ref := "git clone"
		if a := args(t, 2, "-b", "--branch", "--depth", "-o", "--origin", "-c", "--config", "--reference", "-j", "--jobs"); len(a) > 0 {
			ref = a[0]
		}
		c.at("git", ref, "a clone follows a branch or tag that can move: check out a full commit id in the same command")
	case sub(t, 1, "pull"):
		c.at("git", "git pull", "a pull merges whatever the branch points to now: fetch and check out a full commit id")
	case sub(t, 1, "submodule") && slices.Contains(t, "--remote"):
		c.at("git", "git submodule update --remote", "--remote follows the tracked branch: use the commit recorded in the repository")
	}
}
