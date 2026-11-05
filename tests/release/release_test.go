package release

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"gopkg.in/yaml.v3"
)

// The release workflow and `make test-release` set these. AVAL_RELEASE_DIST
// is GoReleaser's dist/ directory. AVAL_RELEASE_TAG is set only for a tagged
// release: then dist/ must also be signed and carry provenance. A dry-run
// (no tag) is unsigned by design: the PR that runs it has no OIDC token.
const (
	envDist = "AVAL_RELEASE_DIST"
	envTag  = "AVAL_RELEASE_TAG"

	// Bundle the release workflow copies from actions/attest-build-provenance.
	provenanceFile = "provenance.sigstore.json"
	checksumsFile  = "checksums.txt"
	slsaPredicate  = "https://slsa.dev/provenance/v1"
	githubIssuer   = "https://token.actions.githubusercontent.com"
	releaseSigner  = "https://github.com/TrianaLab/aval/.github/workflows/release.yaml@refs/tags/"
	modulePath     = "github.com/TrianaLab/aval"
)

// platforms is what a release claims to ship: one archive per entry.
var platforms = []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64", "windows_arm64"}

type metadata struct {
	Tag     string `json:"tag"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type artifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Type   string `json:"type"`
	Goos   string `json:"goos"`
	Goarch string `json:"goarch"`
}

type release struct {
	dir       string
	tag       string
	meta      metadata
	archives  []artifact
	checksums map[string]string // file name -> sha256 hex
}

func load(t *testing.T) release {
	t.Helper()
	dir := os.Getenv(envDist)
	if dir == "" {
		t.Fatalf("%s is not set: run `make test-release` (snapshot dry-run) or set it to a GoReleaser dist/", envDist)
	}
	r := release{dir: dir, tag: os.Getenv(envTag)}
	readJSON(t, filepath.Join(dir, "metadata.json"), &r.meta)
	var all []artifact
	readJSON(t, filepath.Join(dir, "artifacts.json"), &all)
	for _, a := range all {
		if a.Type == "Archive" {
			r.archives = append(r.archives, a)
		}
	}
	r.checksums = parseChecksums(t, filepath.Join(dir, checksumsFile))
	return r
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func parseChecksums(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sums := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sum, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok || len(sum) != 64 || name == "" || sums[name] != "" {
			t.Fatalf("%s: malformed or duplicate line %q", path, sc.Text())
		}
		sums[name] = sum
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return sums
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// The release ships exactly one archive per claimed platform, and its
// version is the tag (when tagged), which Changesets wrote to package.json.
func TestReleaseClaims(t *testing.T) {
	r := load(t)
	var got []string
	for _, a := range r.archives {
		got = append(got, a.Goos+"_"+a.Goarch)
	}
	slices.Sort(got)
	if !slices.Equal(got, platforms) {
		t.Errorf("archives for %v, want %v", got, platforms)
	}
	if len(r.meta.Commit) != 40 || r.meta.Version == "" {
		t.Errorf("metadata.json: version %q, commit %q", r.meta.Version, r.meta.Commit)
	}
	if r.tag == "" {
		return
	}
	var pkg struct {
		Version string `json:"version"`
	}
	readJSON(t, filepath.Join("..", "..", "package.json"), &pkg)
	if want := "v" + pkg.Version; r.tag != want || r.meta.Tag != want || r.meta.Version != pkg.Version {
		t.Errorf("tag %q, metadata tag %q version %q; package.json (Changesets) says %s", r.tag, r.meta.Tag, r.meta.Version, want)
	}
}

// unlisted are the files in dist/ that may be absent from checksums.txt:
// GoReleaser's own metadata (never published) and the signature and
// provenance over checksums.txt itself.
var unlisted = map[string]bool{
	checksumsFile:                    true,
	checksumsFile + ".sigstore.json": true,
	provenanceFile:                   true,
	"artifacts.json":                 true,
	"metadata.json":                  true,
	"config.yaml":                    true,
}

// checksums.txt lists exactly the archives and their SBOMs, each with its
// real digest, and every other file in dist/ is in unlisted. The release job
// publishes only checksums.txt, its entries and its two bundles
// (TestReleaseWorkflowVerifiesBeforePublishing), so nothing ships unverified.
func TestChecksumsMatchFiles(t *testing.T) {
	r := load(t)
	for name, want := range r.checksums {
		if got := sha256File(t, filepath.Join(r.dir, name)); got != want {
			t.Errorf("%s: sha256 %s, checksums.txt says %s", name, got, want)
		}
	}
	want := map[string]bool{}
	for _, a := range r.archives {
		want[a.Name], want[a.Name+".sbom.json"] = true, true
	}
	for name := range want {
		if r.checksums[name] == "" {
			t.Errorf("%s is not in checksums.txt", name)
		}
	}
	for name := range r.checksums {
		if !want[name] {
			t.Errorf("checksums.txt lists %s, which is not an archive or SBOM the release claims", name)
		}
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() && !unlisted[e.Name()] && r.checksums[e.Name()] == "" {
			t.Errorf("%s is in dist/ but not in checksums.txt", e.Name())
		}
	}
}

// Each archive holds the aval binary for its platform, built from the
// release commit with the release version, CGO off and -trimpath, plus the
// license. The binary for this host also runs and reports its version
// (-trimpath keeps -ldflags out of the build info, so running is the check).
func TestArchivesShipTheClaimedBinary(t *testing.T) {
	r := load(t)
	ran := false
	for _, a := range r.archives {
		files := readArchive(t, filepath.Join(r.dir, a.Name))
		bin := "aval"
		if a.Goos == "windows" {
			bin += ".exe"
		}
		if files["LICENSE"] == nil {
			t.Errorf("%s: no LICENSE", a.Name)
		}
		exe := files[bin]
		if exe == nil {
			t.Errorf("%s: no %s", a.Name, bin)
			continue
		}
		checkBuildInfo(t, r, a, exe)
		if a.Goos == runtime.GOOS && a.Goarch == runtime.GOARCH {
			ran = true
			checkRuns(t, r, exe)
		}
	}
	if !ran {
		t.Errorf("no archive for this host (%s/%s) to run", runtime.GOOS, runtime.GOARCH)
	}
}

func checkBuildInfo(t *testing.T, r release, a artifact, exe []byte) {
	t.Helper()
	info, err := buildinfo.Read(bytes.NewReader(exe))
	if err != nil {
		t.Errorf("%s: build info: %v", a.Name, err)
		return
	}
	want := map[string]string{
		"GOOS":         a.Goos,
		"GOARCH":       a.Goarch,
		"CGO_ENABLED":  "0",
		"-trimpath":    "true",
		"vcs.revision": r.meta.Commit,
	}
	if r.tag != "" {
		want["vcs.modified"] = "false"
	}
	got := map[string]string{}
	for _, s := range info.Settings {
		got[s.Key] = s.Value
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: build setting %s = %q, want %q", a.Name, k, got[k], v)
		}
	}
	if info.Main.Path != modulePath {
		t.Errorf("%s: main module %q, want %q", a.Name, info.Main.Path, modulePath)
	}
	checkSBOM(t, filepath.Join(r.dir, a.Name+".sbom.json"), info)
}

func checkRuns(t *testing.T, r release, exe []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aval")
	if err := os.WriteFile(path, exe, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(path, "--version").CombinedOutput()
	if want := "aval version " + r.meta.Version + "\n"; err != nil || string(out) != want {
		t.Errorf("aval --version: %v, output %q, want %q", err, out, want)
	}
}

// The SBOM of an archive lists every module compiled into its binary, at
// the version the binary was built with.
func checkSBOM(t *testing.T, path string, info *debug.BuildInfo) {
	t.Helper()
	var doc struct {
		SPDXVersion string `json:"spdxVersion"`
		Packages    []struct {
			Name    string `json:"name"`
			Version string `json:"versionInfo"`
		} `json:"packages"`
	}
	readJSON(t, path, &doc)
	if doc.SPDXVersion != "SPDX-2.3" {
		t.Errorf("%s: spdxVersion %q, want SPDX-2.3", path, doc.SPDXVersion)
	}
	listed := map[string]bool{}
	for _, p := range doc.Packages {
		listed[p.Name+"@"+p.Version] = true
	}
	for _, d := range info.Deps {
		if d.Replace != nil {
			d = d.Replace
		}
		if !listed[d.Path+"@"+d.Version] {
			t.Errorf("%s: does not list %s@%s", filepath.Base(path), d.Path, d.Version)
		}
	}
}

// readArchive returns the regular files of a .tar.gz or .zip by name.
func readArchive(t *testing.T, path string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	if strings.HasSuffix(path, ".zip") {
		zr, err := zip.OpenReader(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = zr.Close() }()
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			files[f.Name] = readAll(t, rc)
			_ = rc.Close()
		}
		return files
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if h.Typeflag == tar.TypeReg {
			files[h.Name] = readAll(t, tr)
		}
	}
}

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A tagged release is signed: a keyless Sigstore signature over
// checksums.txt and SLSA provenance whose subjects are exactly the files in
// checksums.txt, both from the release workflow at that tag. A dry-run has
// neither (it has no OIDC token), and must not pretend to.
func TestTaggedReleaseIsSignedWithProvenance(t *testing.T) {
	r := load(t)
	sig := filepath.Join(r.dir, checksumsFile+".sigstore.json")
	prov := filepath.Join(r.dir, provenanceFile)
	if r.tag == "" {
		for _, p := range []string{sig, prov} {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("%s exists in an untagged dry-run", filepath.Base(p))
			}
		}
		return
	}
	v := sigstoreVerifier(t)
	id, err := verify.NewShortCertificateIdentity(githubIssuer, "", releaseSigner+r.tag, "")
	if err != nil {
		t.Fatal(err)
	}
	sums, err := os.Open(filepath.Join(r.dir, checksumsFile))
	if err != nil {
		t.Fatal(err)
	}
	defer sums.Close()
	if _, err := v.Verify(loadBundle(t, sig), verify.NewPolicy(verify.WithArtifact(sums), verify.WithCertificateIdentity(id))); err != nil {
		t.Errorf("checksums.txt signature: %v", err)
	}
	res, err := v.Verify(loadBundle(t, prov), verify.NewPolicy(verify.WithoutArtifactUnsafe(), verify.WithCertificateIdentity(id)))
	if err != nil {
		t.Fatalf("provenance: %v", err)
	}
	checkProvenanceSubjects(t, r, res)
}

func checkProvenanceSubjects(t *testing.T, r release, res *verify.VerificationResult) {
	t.Helper()
	if res.Statement == nil || res.Statement.GetPredicateType() != slsaPredicate {
		t.Fatalf("provenance predicate type %q, want %s", res.Statement.GetPredicateType(), slsaPredicate)
	}
	subjects := map[string]string{}
	for _, s := range res.Statement.GetSubject() {
		subjects[s.GetName()] = s.GetDigest()["sha256"]
	}
	if fmt.Sprint(subjects) != fmt.Sprint(r.checksums) {
		t.Errorf("provenance subjects %v, want checksums.txt %v", subjects, r.checksums)
	}
}

// sigstoreVerifier trusts the Sigstore public-good instance (fetched with
// TUF), which signs both cosign and GitHub attestations for public repos.
func sigstoreVerifier(t *testing.T) *verify.Verifier {
	t.Helper()
	tr, err := root.FetchTrustedRoot()
	if err != nil {
		t.Fatal(err)
	}
	v, err := verify.NewVerifier(tr, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func loadBundle(t *testing.T, path string) *bundle.Bundle {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bytes.TrimSpace(data)); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return &b
}

// The release job verifies dist/ (with the tag set) before it publishes, and
// it is the workflow file whose identity TestTaggedReleaseIsSignedWithProvenance
// requires; renaming it would only surface at release time.
func TestReleaseWorkflowVerifiesBeforePublishing(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatalf("%v (signatures are verified against %s<tag>)", err, releaseSigner)
	}
	var wf struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Steps []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatal(err)
	}
	job := wf.Jobs["release"]
	if job.If != "startsWith(github.ref, 'refs/tags/v')" {
		t.Errorf("release job runs on %q, want tags only", job.If)
	}
	verified, published, e2e := false, false, false
	for _, s := range job.Steps {
		switch {
		// #28: no release without a verified e2e attestation for the commit,
		// under the main policy.
		case strings.Contains(s.Run, "aval verify -f aval.e2e.yaml") && strings.Contains(s.Run, "--policy .github/aval/main.yaml"):
			e2e = true
		case strings.Contains(s.Run, "./tests/release/") && s.Env[envTag] != "" && s.Env[envDist] != "":
			verified = true
		case strings.Contains(s.Run, "gh release create"):
			published = true
			if !verified {
				t.Error("release job publishes before tests/release verifies the tagged dist/")
			}
			if !e2e {
				t.Error("release job publishes without verifying the e2e attestation (aval.e2e.yaml) for the commit")
			}
			checkPublishedFiles(t, s.Run)
		}
	}
	if !verified {
		t.Error("release job never runs tests/release with the tag set")
	}
	if !published {
		t.Error("release job has no gh release create step")
	}
}

// publishRun is the only upload the release job may run: the entries of
// checksums.txt, checksums.txt and its two bundles. A glob or any other path
// could upload a file tests/release never checked, so the step is pinned.
const publishRun = `mapfile -t files < <(awk '{print "dist/" $2}' dist/checksums.txt)
gh release create "$TAG" --verify-tag --title "$TAG" --notes-file notes.md \
  "${files[@]}" dist/checksums.txt dist/checksums.txt.sigstore.json dist/provenance.sigstore.json`

func checkPublishedFiles(t *testing.T, run string) {
	t.Helper()
	if strings.TrimSpace(run) != publishRun {
		t.Errorf("publish step must be exactly\n%s\ngot\n%s", publishRun, run)
	}
}
