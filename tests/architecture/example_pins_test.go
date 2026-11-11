package architecture

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// examplePin matches a step that uses Aval's own GitHub actions; the
// comment after the commit SHA is optional so that a missing one is caught.
var (
	examplePin     = regexp.MustCompile(`uses: TrianaLab/aval/integrations/github/(run|publish)@(\S+)(?: # ?(.*))?`)
	fullSHA        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	desiredVersion = regexp.MustCompile(`(?m)^ *DESIRED_VERSION: *(\S+)$`)
	// docsInstallVersion is a hard-coded aval version in prose, such as
	// "install aval 0.7.0" or "aval v0.8.2".
	docsInstallVersion = regexp.MustCompile("(?i)\\baval`? v?[0-9]+\\.[0-9]+\\.[0-9]+")
)

// examplePinProblems: each use of Aval's actions in an example workflow
// (path to content) is pinned by a full commit SHA with a "# vX.Y.Z"
// comment, versionAt(sha, action) (the DESIRED_VERSION that action.yml has
// at that commit) equals the comment, and the version is the one main pins
// (current) or, while the examples' pin PR from release.yaml's pin-examples
// job is open, the one main pinned before it (previous). A pin PR cannot
// move the examples itself: its squash-merge commit does not exist yet.
func examplePinProblems(files map[string]string, versionAt func(sha, action string) (string, error), current, previous string) []string {
	var problems []string
	for f, data := range files {
		for _, m := range examplePin.FindAllStringSubmatch(data, -1) {
			action, sha, comment := m[1], m[2], m[3]
			at := fmt.Sprintf("%s: %s@%s", f, action, sha)
			if !fullSHA.MatchString(sha) {
				problems = append(problems, at+": not a full commit SHA")
				continue
			}
			v, err := versionAt(sha, action)
			switch {
			case err != nil:
				problems = append(problems, at+": "+err.Error())
			case v != current && v != previous:
				problems = append(problems, fmt.Sprintf("%s: installs aval %s, main pins %s", at, v, current))
			case comment != v:
				problems = append(problems, fmt.Sprintf("%s: comment %q, but that commit installs aval %s: use # %s", at, comment, v, v))
			}
		}
	}
	return problems
}

// pinnedVersions returns the DESIRED_VERSION main pins and the one it pinned
// before, from the git history of integrations/github/run/action.yml.
func pinnedVersions(git func(...string) (string, error)) (current, previous string, err error) {
	const path = "integrations/github/run/action.yml"
	log, err := git("log", "--format=%H", "-G", "DESIRED_VERSION: v", "HEAD", "--", path)
	if err != nil {
		return "", "", fmt.Errorf("git log %s: %w", path, err)
	}
	for _, sha := range strings.Fields(log) {
		data, err := git("show", sha+":"+path)
		if err != nil {
			return "", "", fmt.Errorf("git show %s:%s: %w", sha, path, err)
		}
		m := desiredVersion.FindStringSubmatch(data)
		switch {
		case m == nil:
		case current == "":
			current = m[1]
		case m[1] != current:
			return current, m[1], nil
		}
	}
	if current == "" {
		return "", "", fmt.Errorf("no DESIRED_VERSION in the history of %s", path)
	}
	return current, current, nil
}

// versionAtCommit reads DESIRED_VERSION of an action at a commit, which must
// be in main's history (not a commit reachable only from a fork or a closed
// pull request).
func versionAtCommit(git func(...string) (string, error)) func(sha, action string) (string, error) {
	return func(sha, action string) (string, error) {
		if _, err := git("merge-base", "--is-ancestor", sha, "HEAD"); err != nil {
			return "", fmt.Errorf("not a commit in this branch's history (a shallow clone needs fetch-depth: 0): %w", err)
		}
		data, err := git("show", sha+":integrations/github/"+action+"/action.yml")
		if err != nil {
			return "", err
		}
		m := desiredVersion.FindStringSubmatch(data)
		if m == nil {
			return "", fmt.Errorf("no DESIRED_VERSION in its action.yml")
		}
		return m[1], nil
	}
}

// The examples the guides embed install the release main pins (#261): the
// integrate-in-5-files guide showed a SHA that installed aval 0.7.0 when
// main pinned 0.8.2.
func TestExampleActionPins(t *testing.T) {
	git := gitIn(root)
	current, previous, err := pinnedVersions(git)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range trackedFiles(t, "examples/*/.github/workflows/*.yml", "examples/*/.github/workflows/*.yaml") {
		files[f] = readFile(t, f)
	}
	if len(files) == 0 {
		t.Fatal("no example workflows")
	}
	for _, p := range examplePinProblems(files, versionAtCommit(git), current, previous) {
		t.Error(p)
	}
}

func TestExamplePinCheckCatchesDrift(t *testing.T) {
	const (
		cur  = "1111111111111111111111111111111111111111"
		prev = "2222222222222222222222222222222222222222"
		old  = "3333333333333333333333333333333333333333"
		fork = "4444444444444444444444444444444444444444"
	)
	versions := map[string]string{cur: "v0.8.2", prev: "v0.8.1", old: "v0.7.0"}
	versionAt := func(sha, _ string) (string, error) {
		if v, ok := versions[sha]; ok {
			return v, nil
		}
		return "", fmt.Errorf("not in history")
	}
	use := func(action, ref string) string {
		return "      - uses: TrianaLab/aval/integrations/github/" + action + "@" + ref + "\n"
	}
	ok := map[string]string{"ci.yml": use("run", cur+" # v0.8.2") + use("publish", prev+" # v0.8.1")}
	if p := examplePinProblems(ok, versionAt, "v0.8.2", "v0.8.1"); len(p) != 0 {
		t.Fatalf("valid pins reported %q", p)
	}
	for name, ref := range map[string]string{
		"two releases old": old + " # v0.7.0",
		"date comment":     old + " # main, 2026-11-10",
		"no comment":       cur,
		"wrong comment":    prev + " # v0.8.2",
		"tag":              "v0.8.2",
		"short sha":        cur[:12] + " # v0.8.2",
		"fork commit":      fork + " # v0.8.2",
	} {
		files := map[string]string{"ci.yml": use("run", cur+" # v0.8.2") + use("publish", ref)}
		if p := examplePinProblems(files, versionAt, "v0.8.2", "v0.8.1"); len(p) != 1 {
			t.Errorf("%s: got %q, want one problem", name, p)
		}
	}
	// Once the examples' pin PR merged, previous equals current again.
	if p := examplePinProblems(ok, versionAt, "v0.8.2", "v0.8.2"); len(p) != 1 {
		t.Errorf("previous pin after the examples moved: got %q", p)
	}

	git := func(args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "log --format=%H -G DESIRED_VERSION: v HEAD -- integrations/github/run/action.yml":
			return "c\nb\na", nil
		case "show c:integrations/github/run/action.yml", "show b:integrations/github/run/action.yml":
			return "        DESIRED_VERSION: v0.8.2\n", nil
		case "show a:integrations/github/run/action.yml":
			return "        DESIRED_VERSION: v0.7.0\n", nil
		}
		return "", fmt.Errorf("unexpected git %q", args)
	}
	if c, p, err := pinnedVersions(git); err != nil || c != "v0.8.2" || p != "v0.7.0" {
		t.Errorf("pinnedVersions = %q, %q, %v", c, p, err)
	}
}

// Docs name no hard-coded install version that goes stale with the next
// release: the version an action installs is the one its commit pins. Only
// docs/install.md, whose commands pin a version on purpose, and the
// changelog may.
func TestDocsNameNoInstallVersion(t *testing.T) {
	for _, f := range trackedFiles(t, "docs/*.md") {
		if f == "docs/install.md" || f == "docs/changelog.md" {
			continue
		}
		for _, m := range docsInstallVersion.FindAllString(readFile(t, f), -1) {
			t.Errorf("%s: %q: say \"the release the action's commit pins\" or link docs/install.md", f, m)
		}
	}
}

func TestDocsInstallVersionCatchesDrift(t *testing.T) {
	for _, s := range []string{"The actions install aval 0.7.0 until", "needs `aval` v0.8.2", "Aval 1.2.3"} {
		if !docsInstallVersion.MatchString(s) {
			t.Errorf("%q not caught", s)
		}
	}
	for _, s := range []string{"aval upgrade --version v0.4.0", "aval-fingerprint/v1", "cosign v2.0.0"} {
		if docsInstallVersion.MatchString(s) {
			t.Errorf("%q caught", s)
		}
	}
}
