package architecture

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/policy"
)

var (
	// A backtick or tilde fenced block, possibly indented (admonitions, tabs):
	// indent, then info string and body in groups 2-3 (backticks) or 4-5 (tildes).
	docFence   = regexp.MustCompile("(?ms)^([ \t]*)(?:```([^\n]*)\n(.*?)^[ \t]*```|~~~([^\n]*)\n(.*?)^[ \t]*~~~)")
	heredoc    = regexp.MustCompile(`<<-?\s*['"]?(\w+)['"]?\s*$`)
	docKind    = regexp.MustCompile(`(?m)^kind:\s*(\S+)`)
	docAval    = regexp.MustCompile(`(?m)^apiVersion:\s*aval\.`)
	docPubKeys = regexp.MustCompile(`(?m)^\s*-?\s*publicKey:\s*['"]?([^\s'"#]+)`)
)

// yamlDocs returns the YAML documents embedded in Markdown: the body of every
// yaml/yml fence and every heredoc inside any other fence (`cat > aval.yaml
// <<'EOF'`).
func yamlDocs(md string) []string {
	var out []string
	for _, m := range docFence.FindAllStringSubmatch(md, -1) {
		info, body := m[2]+m[4], m[3]+m[5]
		lines := strings.Split(body, "\n")
		for i, l := range lines {
			lines[i] = strings.TrimPrefix(l, m[1])
		}
		if lang := strings.Fields(info + " x")[0]; lang == "yaml" || lang == "yml" {
			out = append(out, strings.Join(lines, "\n"))
			continue
		}
		for i := 0; i < len(lines); i++ {
			h := heredoc.FindStringSubmatch(lines[i])
			if h == nil {
				continue
			}
			var body []string
			for i++; i < len(lines) && strings.TrimSpace(lines[i]) != h[1]; i++ {
				body = append(body, lines[i])
			}
			out = append(out, strings.Join(body, "\n"))
		}
	}
	return out
}

// exampleProblems parses every aval.yaml and policy example in md with the
// real parsers. A document is an example when its top-level kind is Test or
// Policy, or when it has an Aval apiVersion; other YAML (fragments, CI
// workflows) is skipped. Policy publicKey files are created in dir as fresh
// P-256 keys, so the check covers the document and not the reader's files.
func exampleProblems(t *testing.T, doc, md, dir string) []string {
	t.Helper()
	var bad []string
	for i, y := range yamlDocs(md) {
		kind := ""
		if k := docKind.FindStringSubmatch(y); k != nil {
			kind = k[1]
		}
		var err error
		switch {
		case kind == contract.Kind:
			_, err = contract.Parse([]byte(y))
		case kind == policy.Kind:
			d := filepath.Join(dir, fmt.Sprintf("%s-%d", strings.NewReplacer("/", "_").Replace(doc), i))
			for _, k := range docPubKeys.FindAllStringSubmatch(y, -1) {
				writePublicKey(t, d, k[1])
			}
			_, err = policy.Parse([]byte(y), d)
		case docAval.MatchString(y):
			err = fmt.Errorf("kind %q: want %s or %s", kind, contract.Kind, policy.Kind)
		default:
			continue
		}
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: example %d (kind %s): %v", doc, i+1, kind, err))
		}
	}
	return bad
}

// writePublicKey writes a fresh P-256 public key to dir/rel. An absolute or
// escaping path is left unwritten, so policy.Parse reports it as missing.
func writePublicKey(t *testing.T, dir, rel string) {
	t.Helper()
	if !filepath.IsLocal(rel) {
		return
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Every aval.yaml and policy example embedded in the docs parses with the
// real parsers (#76): a doc cannot show a contract or policy Aval rejects.
func TestDocExamplesParse(t *testing.T) {
	docs := trackedFiles(t, "*.md", ":!.agents", ":!.claude", ":!.changeset", ":!CHANGELOG.md")
	dir := t.TempDir()
	var tests, policies int
	for _, doc := range docs {
		md := readFile(t, doc)
		for _, y := range yamlDocs(md) {
			switch k := docKind.FindStringSubmatch(y); {
			case k != nil && k[1] == contract.Kind:
				tests++
			case k != nil && k[1] == policy.Kind:
				policies++
			}
		}
		for _, p := range exampleProblems(t, doc, md, dir) {
			t.Error(p)
		}
	}
	// The README quickstart and the specs carry both kinds; finding none
	// means the extraction broke, not that the docs are clean.
	if tests == 0 || policies == 0 {
		t.Fatalf("found %d contract and %d policy examples, want both > 0", tests, policies)
	}
}

func TestDocExamplesCatchInvalid(t *testing.T) {
	md := "```yaml\napiVersion: aval.trianalab.io/v1\nkind: Test\nmetadata: {name: ok}\n" +
		"runner: {command: [\"true\"]}\nscope: {source: [\"src/**\"]}\ncacheability: hermetic\n```\n" +
		"```yaml\nkind: Test\nmetadata: {name: Bad Name}\n```\n" +
		"```sh\ncat > policy.yaml <<'EOF'\napiVersion: aval.trianalab.io/v1\nkind: Policy\n" +
		"minTrustLevel: runner\nidentities:\n  - publicKey: keys/a.pub\n    trustLevel: developer\nEOF\n```\n" +
		"!!! note\n    ```yaml\n    apiVersion: aval.trianalab.io/v1\n    kind: Policy\n    minTrustLevel: root\n" +
		"    identities:\n      - publicKey: ../../etc/k.pub\n        trustLevel: runner\n    ```\n" +
		"```yaml\napiVersion: aval.trianalab.io/v1\nkind: Tset\n```\n" +
		"```yaml\nscope:\n  source: [\"x\"]\n```\n" +
		"~~~yaml\napiVersion: aval.trianalab.io/v1\nkind: Test\nbogus: 1\n~~~\n"
	got := exampleProblems(t, "x.md", md, t.TempDir())
	want := []string{"example 2 (kind Test)", "example 4 (kind Policy)", "example 5 (kind Tset)", "example 7 (kind Test)"}
	if len(got) != len(want) {
		t.Fatalf("problems %q, want %d: %q", got, len(want), want)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("problem %d: %q, want it to name %q", i, got[i], w)
		}
	}
}

// exitCodeTable matches the header row of a Markdown table with an exit-code
// column.
var exitCodeTable = regexp.MustCompile(`(?mi)^\|[^\n]*\b(exit( codes?)?|code)\b[^\n]*\|\n\|[-| :]+\|$`)

// One exit-code table, generated into docs/cli-reference.md; every other page
// includes it with `--8<-- "docs/cli-reference.md:exit-codes"` (#76).
func TestOneExitCodeTable(t *testing.T) {
	for _, doc := range trackedFiles(t, "*.md", ":!.agents", ":!.claude", ":!.changeset", ":!CHANGELOG.md") {
		if doc != "docs/cli-reference.md" && exitCodeTable.MatchString(readFile(t, doc)) {
			t.Errorf("%s: has its own exit-code table; include docs/cli-reference.md:exit-codes instead", doc)
		}
	}
	for _, table := range []string{
		"| Situation | Exit code |\n|---|---|\n",
		"| Code | `aval run` | `aval verify` |\n|---|---|---|\n",
		"| Exit | Meaning |\n|---|---|\n",
	} {
		if !exitCodeTable.MatchString(table) {
			t.Errorf("exitCodeTable no longer matches %q", table)
		}
	}
}

// siteDeepLink matches a link to a page of aval.run (a path ending in / or an
// anchor). The site is versioned with mike, so only pages under a version
// (main/, latest/) resolve; files at the root (install.sh) are not pages.
var siteDeepLink = regexp.MustCompile(`aval\.run/([a-z][^\s)]*(/|#[^\s)]*))`)

// The README is a front door into the site, not a copy of it (#76).
func TestReadmeIsShort(t *testing.T) {
	readme := readFile(t, "README.md")
	if n := strings.Count(readme, "\n"); n >= 150 {
		t.Errorf("README.md: %d lines, keep it under 150 and link into the site", n)
	}
	for _, m := range siteDeepLink.FindAllStringSubmatch(readme, -1) {
		if !strings.HasPrefix(m[1], "main/") && !strings.HasPrefix(m[1], "latest/") {
			t.Errorf("README.md: https://aval.run/%s has no version and returns 404; link https://aval.run/main/%s", m[1], m[1])
		}
	}
}
