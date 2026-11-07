//go:build !js

package resolve

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"golang.org/x/mod/sumdb/dirhash"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/registry"
)

// image resolves a container image (name:tag, also on a mirror host) to its
// manifest digest: for a multi-arch image, the digest of the index.
// Credentials come from the Docker configuration (internal/registry).
func image(ctx context.Context, ref string) (string, string, error) {
	d, err := registry.ResolveImage(ctx, ref)
	return "-", d, err
}

// split checks ref again as contract validation does (the package name
// against its ecosystem's rules, the spec against other sources) and
// returns its name and spec; def is the spec when there is none.
func split(kind, ref, def string) (string, string, error) {
	name, spec, err := contract.Reference{Kind: kind, Ref: ref}.Package()
	return name, cmp.Or(spec, def), err
}

// floating is the version of a reference that names no exact version (a
// range, latest, a tag): its id is the hash of the package's whole release
// list with every checksum and tag, so any publish, yank or retag changes
// it. Aval never picks a version from a range: it would have to match each
// tool's range rules exactly, and every difference is a false hit.
const floating = "floating"

// releases returns the id of a floating reference: lines, one per release
// (or file, or tag), sorted. A registry that lists nothing fails.
func releases(lines []string) (string, string, error) {
	if len(lines) == 0 {
		return "", "", errors.New("the registry lists no version")
	}
	slices.Sort(lines)
	return floating, sumLines(lines), nil
}

// entry is one release-list line. Fields are quoted, so a server cannot
// make two different lists hash the same with a space or newline.
func entry(fields ...string) string {
	for i, f := range fields {
		fields[i] = strconv.Quote(f)
	}
	return strings.Join(fields, " ")
}

// semverRe is an exact semver version: x.y.z[-pre][+build], no operator.
var semverRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// npmRegistry fails when ~/.npmrc sends pkg to another registry than base:
// a @scope:registry line for its scope (npm prefers it to the environment),
// or a registry line when no environment variable sets one. The last line
// wins, as in npm.
func npmRegistry(pkg, base string) error {
	vals := map[string]string{}
	for _, l := range strings.Split(configFile("NPM_CONFIG_USERCONFIG", ".npmrc"), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			vals[strings.TrimSpace(k)] = strings.TrimSuffix(os.ExpandEnv(strings.TrimSpace(v)), "/")
		}
	}
	key := "registry"
	if os.Getenv("NPM_CONFIG_REGISTRY") != "" || os.Getenv("npm_config_registry") != "" {
		key = ""
	}
	if scope, _, _ := strings.Cut(pkg, "/"); strings.HasPrefix(pkg, "@") {
		if _, ok := vals[scope+":registry"]; ok {
			key = scope + ":registry"
		}
	}
	if v, ok := vals[key]; ok && v != base {
		return fmt.Errorf(".npmrc sets %s to %s, not %s: npm would install from there; set npm_config_registry to match", key, redact(v), redact(base))
	}
	return nil
}

// npm resolves name@version on the npm registry ($npm_config_registry) to
// that version's dist.integrity. Any other spec (a range, a dist-tag such as
// latest) binds every version's checksum and deprecation and every
// dist-tag.
func npm(ctx context.Context, ref string) (string, string, error) {
	pkg, want, err := split("npm", ref, "latest")
	if err != nil {
		return "", "", err
	}
	base := baseURL("https://registry.npmjs.org", "NPM_CONFIG_REGISTRY", "npm_config_registry")
	if err := npmRegistry(pkg, base); err != nil {
		return "", "", err
	}
	var doc struct {
		Tags     map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Dist       struct{ Integrity, Shasum string }
			Deprecated json.RawMessage
		}
	}
	if err := getJSON(ctx, base+"/"+strings.Replace(pkg, "/", "%2f", 1), "application/vnd.npm.install-v1+json", &doc); err != nil {
		return "", "", err
	}
	if semverRe.MatchString(want) {
		d, ok := doc.Versions[want]
		if !ok {
			return "", "", fmt.Errorf("version %s is not published", want)
		}
		return idOrErr(want, cmp.Or(d.Dist.Integrity, d.Dist.Shasum))
	}
	var lines []string
	for v, d := range doc.Versions {
		_, sum, err := idOrErr(v, cmp.Or(d.Dist.Integrity, d.Dist.Shasum))
		if err != nil {
			return "", "", err
		}
		lines = append(lines, entry("version", v, sum, string(d.Deprecated)))
	}
	for tag, v := range doc.Tags {
		lines = append(lines, entry("tag", tag, v))
	}
	return releases(lines)
}

// pypi resolves name==version on PyPI's JSON API ($AVAL_PYPI_URL) to a
// sha256 over all that version's files. Any other specifier binds every
// file of every release, with its yanked flag.
func pypi(ctx context.Context, ref string) (string, string, error) {
	pkg, spec, err := split("pypi", ref, "")
	if err != nil {
		return "", "", err
	}
	base := baseURL("https://pypi.org", "AVAL_PYPI_URL")
	if err := pipIndex(base); err != nil {
		return "", "", err
	}
	var doc struct{ Releases map[string][]pyFile }
	if err := getJSON(ctx, base+"/pypi/"+url.PathEscape(pkg)+"/json", "", &doc); err != nil {
		return "", "", err
	}
	if v, ok := strings.CutPrefix(spec, "=="); ok && pyExactRe.MatchString(v) {
		// pip's ==v matches every release equal to v, with any local label
		// when v has none: bind them all.
		local := strings.Contains(v, "+")
		var lines []string
		for k, files := range doc.Releases {
			if pyCanon(k, local) != pyCanon(v, local) {
				continue
			}
			for _, f := range files {
				if f.Digests.Sha256 == "" {
					return "", "", fmt.Errorf("version %s: file %s has no sha256 in the registry", k, f.Filename)
				}
				lines = append(lines, entry(k, f.Filename, f.Digests.Sha256))
			}
		}
		if len(lines) == 0 {
			return "", "", fmt.Errorf("version %s is not published", v)
		}
		slices.Sort(lines)
		return v, sumLines(lines), nil
	}
	var lines []string
	for v, files := range doc.Releases {
		for _, f := range files {
			if f.Digests.Sha256 == "" {
				return "", "", fmt.Errorf("version %s: file %s has no sha256 in the registry", v, f.Filename)
			}
			lines = append(lines, entry(v, f.Filename, f.Digests.Sha256, strconv.FormatBool(f.Yanked)))
		}
	}
	return releases(lines)
}

// pipIndex fails when the environment points pip to another index than
// base.
func pipIndex(base string) error {
	for _, e := range []string{"PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL"} {
		// pip reads the simple index at <base>/simple: the same registry.
		if v := strings.TrimSuffix(os.Getenv(e), "/"); v != "" && (e == "PIP_EXTRA_INDEX_URL" || v != base && v != base+"/simple") {
			return fmt.Errorf("%s is %s, not %s: pip would install from there; set AVAL_PYPI_URL to match", e, redact(v), redact(base))
		}
	}
	return nil
}

// pyExactRe is the version of an exact ==X: no wildcard, no second clause.
var pyExactRe = regexp.MustCompile(`^[0-9A-Za-z.!+_-]+$`)

// pyCanon is v as pip compares it for ==: lowercase, leading zeros and
// trailing .0 release segments dropped (1.0 equals 1.00.0), and the local
// label dropped unless local is set.
// ponytail: pre, post and dev spellings (1.0c1 for 1.0rc1) are not
// normalized; a private index that lists both spellings is not grouped.
func pyCanon(v string, local bool) string {
	v = strings.ToLower(v)
	if !local {
		v, _, _ = strings.Cut(v, "+")
	}
	m := pyReleaseRe.FindStringSubmatch(v)
	if m == nil {
		return v
	}
	epoch := strings.TrimLeft(m[1], "0") // 0! is no epoch
	parts := strings.Split(m[2], ".")
	for i, p := range parts {
		parts[i] = cmp.Or(strings.TrimLeft(p, "0"), "0")
	}
	for len(parts) > 1 && parts[len(parts)-1] == "0" {
		parts = parts[:len(parts)-1]
	}
	return strings.TrimPrefix(epoch, "!") + strings.Join(parts, ".") + m[3]
}

// pyReleaseRe splits a version into an optional v, epoch, release segment
// and the rest.
var pyReleaseRe = regexp.MustCompile(`^v?([0-9]+!)?([0-9]+(?:\.[0-9]+)*)(.*)$`)

type pyFile struct {
	Filename string
	Digests  struct{ Sha256 string }
	Yanked   bool
}

// goExactRe is an exact module version: vX.Y.Z[-pre], pseudo-versions
// included. +incompatible and +build are left floating.
var goExactRe = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)

// gomod resolves module@version on the first proxy in $GOPROXY to the h1:
// hash go.sum records for it. A branch or commit is resolved by the proxy
// (/@v/<query>.info, as the go command does) to its pseudo-version first.
// Any other query (latest, a prefix v1.2, <v1.3, +incompatible) binds the
// proxy's version list and its @latest.
func gomod(ctx context.Context, ref string) (string, string, error) {
	mod, query, err := split("go", ref, "latest")
	if err != nil {
		return "", "", err
	}
	if err := goVerified(mod); err != nil {
		return "", "", err
	}
	base, err := goProxy()
	if err == nil {
		mod, err = module.EscapePath(mod)
	}
	if err != nil {
		return "", "", err
	}
	base += "/" + mod + "/@"
	if query == "latest" || strings.ContainsAny(query[:1], "<>") || semver.IsValid(query) && !goExactRe.MatchString(query) {
		return goReleases(ctx, base)
	}
	version := query
	if !goExactRe.MatchString(query) {
		var info struct{ Version string }
		q, err := module.EscapeVersion(query)
		if err == nil {
			err = getJSON(ctx, base+"v/"+q+".info", "", &info)
		}
		if err != nil {
			return "", "", err
		}
		version = info.Version
	}
	h, err := goZipHash(ctx, base, version)
	return version, h, err
}

// goZipHash downloads version's zip from base (".../<module>/@") and
// returns its h1: hash.
func goZipHash(ctx context.Context, base, version string) (string, error) {
	v, err := module.EscapeVersion(version)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "aval-gomod-*.zip")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer tmp.Close()
	if err := get(ctx, base+"v/"+v+".zip", "", MaxDownload, tmp); err != nil {
		return "", err
	}
	return hashModuleZip(ctx, tmp.Name())
}

// goReleases binds the proxy's version list, @latest and the h1: hash of
// the version @latest names, at base (".../<module>/@"). A public version
// is immutable (checksum database), so the list names the content; a new
// version or retraction changes it. A private (GONOSUMDB) proxy can
// republish a version: only the @latest one is bound by content, older
// ones by name.
func goReleases(ctx context.Context, base string) (string, string, error) {
	var list bytes.Buffer
	var info struct{ Version string }
	err := get(ctx, base+"v/list", "", maxMetadata, &list)
	if err == nil {
		err = getJSON(ctx, base+"latest", "", &info)
	}
	var h string
	if err == nil {
		h, err = goZipHash(ctx, base, info.Version)
	}
	if err != nil {
		return "", "", err
	}
	lines := []string{entry("latest", info.Version, h)}
	for _, v := range strings.Fields(list.String()) {
		lines = append(lines, entry("version", v))
	}
	return releases(lines)
}

// hashModuleZip returns the h1: hash of the module zip at name. HashZip
// ignores ctx, so a zip that expands beyond MaxDownload is refused first and
// the deadline holds.
func hashModuleZip(ctx context.Context, name string) (string, error) {
	r, err := zip.OpenReader(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	var size uint64
	for _, f := range r.File {
		if f.UncompressedSize64 > MaxDownload-size { // never overflows
			return "", fmt.Errorf("module zip expands beyond %d bytes", MaxDownload)
		}
		size += f.UncompressedSize64
	}
	h, err := dirhash.HashZip(name, dirhash.Hash1)
	return h, cmp.Or(err, ctx.Err())
}

// goProxy is the first entry of $GOPROXY (default proxy.golang.org). It
// fails when that entry is direct or off: Aval would bind a proxy the go
// command does not use.
func goProxy() (string, error) {
	p := baseURL("https://proxy.golang.org", "GOPROXY")
	if i := strings.IndexAny(p, ",|"); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimSuffix(strings.TrimSpace(p), "/")
	if p == "direct" || p == "off" || p == "" {
		return "", fmt.Errorf("GOPROXY starts with %q: %w", p, errDirect)
	}
	return p, nil
}

var errDirect = errors.New("go would fetch this module directly: pin it or use the git kind")

// goVerified fails when go would fetch mod directly (GOPRIVATE, GONOPROXY)
// or would not check it against the checksum database (GOSUMDB=off,
// GONOSUMDB, GOINSECURE, -insecure in GOFLAGS): a proxy could then serve
// other content for a version than the one Aval hashed.
func goVerified(mod string) error {
	for _, e := range []string{"GOPRIVATE", "GONOPROXY"} {
		if module.MatchPrefixPatterns(os.Getenv(e), mod) {
			return fmt.Errorf("%s matches the module: %w", e, errDirect)
		}
	}
	insecure := os.Getenv("GOSUMDB") == "off"
	for _, e := range []string{"GONOSUMDB", "GOINSECURE"} {
		insecure = insecure || module.MatchPrefixPatterns(os.Getenv(e), mod)
	}
	for _, f := range strings.Fields(os.Getenv("GOFLAGS")) {
		insecure = insecure || strings.HasPrefix(strings.TrimLeft(f, "-"), "insecure")
	}
	if insecure {
		return errors.New("go would not verify this module against the checksum database: unset GOSUMDB=off, GONOSUMDB, GOINSECURE and -insecure, or pin")
	}
	return nil
}

// mavenExactRe is a plain Maven version: no range, no Gradle +.
var mavenExactRe = regexp.MustCompile(`^[0-9A-Za-z_][0-9A-Za-z._-]*$`)

// mavenExact reports whether v names one version: not LATEST, RELEASE or a
// Gradle latest.* keyword, in any case.
func mavenExact(v string) bool {
	l := strings.ToLower(v)
	return mavenExactRe.MatchString(v) && !strings.Contains(v, "..") && l != "latest" && l != "release" && !strings.HasPrefix(l, "latest.")
}

// maven resolves group:artifact:version on a Maven repository
// ($AVAL_MAVEN_URL) to a sha256 over its POM and the published checksum of
// its main artifact (none for packaging pom). An unknown packaging or a
// missing checksum fails. Any other version (a range [1,2), 5.+, LATEST,
// RELEASE, latest.release) binds the versioning block of
// maven-metadata.xml and the POM and checksum of every listed version (at
// most maxMavenVersions). A SNAPSHOT is redeployed under the same name, so
// it fails either way.
func maven(ctx context.Context, ref string) (string, string, error) {
	name, v, err := split("maven", ref, "")
	if err != nil {
		return "", "", err
	}
	group, artifact, _ := strings.Cut(name, ":")
	dir := baseURL("https://repo.maven.apache.org/maven2", "AVAL_MAVEN_URL") + "/" + strings.ReplaceAll(group, ".", "/") + "/" + artifact + "/"
	if mavenExact(v) {
		if isSnapshot(v) {
			return "", "", errSnapshot
		}
		id, err := mavenArtifact(ctx, dir+v+"/"+artifact+"-"+v)
		return v, id, err
	}
	var meta bytes.Buffer
	var doc struct {
		Latest   string   `xml:"versioning>latest"`
		Release  string   `xml:"versioning>release"`
		Versions []string `xml:"versioning>versions>version"`
	}
	err = get(ctx, dir+"maven-metadata.xml", "", maxMetadata, &meta)
	if err == nil {
		err = xml.Unmarshal(meta.Bytes(), &doc)
	}
	if err != nil {
		return "", "", err
	}
	if len(doc.Versions) == 0 {
		return "", "", errors.New("maven-metadata.xml names no version")
	}
	if slices.ContainsFunc(doc.Versions, isSnapshot) {
		return "", "", errSnapshot
	}
	if len(doc.Versions) > maxMavenVersions {
		return "", "", fmt.Errorf("%d versions: too many versions to bind: use an exact version", len(doc.Versions))
	}
	// A private repository can redeploy a release: bind each version's
	// content, as an exact version does.
	lines := []string{entry("latest", doc.Latest), entry("release", doc.Release)}
	for _, x := range doc.Versions {
		id, err := mavenArtifact(ctx, dir+x+"/"+artifact+"-"+x)
		if err != nil {
			return "", "", err
		}
		lines = append(lines, entry("version", x, id))
	}
	return releases(lines)
}

// maxMavenVersions caps the versions a floating Maven version fetches (two
// requests each); above it the reference fails.
const maxMavenVersions = 100

var errSnapshot = errors.New("SNAPSHOT versions are not supported: resolve a release, or pin")

func isSnapshot(v string) bool { return strings.HasSuffix(strings.ToUpper(v), "-SNAPSHOT") }

// mavenArtifact returns a sha256 over the POM at base.pom and the checksum
// of the main artifact its packaging names.
func mavenArtifact(ctx context.Context, base string) (string, error) {
	var pom bytes.Buffer
	if err := get(ctx, base+".pom", "", maxMetadata, &pom); err != nil {
		return "", err
	}
	pomSum := sha256.Sum256(pom.Bytes())
	lines := []string{"pom sha256:" + hex.EncodeToString(pomSum[:])}
	var proj struct {
		Packaging string `xml:"packaging"`
	}
	if err := xml.Unmarshal(pom.Bytes(), &proj); err != nil {
		return "", fmt.Errorf("POM: %w", err)
	}
	packaging := cmp.Or(strings.TrimSpace(proj.Packaging), "jar")
	if packaging != "pom" {
		ext, ok := mavenExt[packaging]
		if !ok {
			return "", fmt.Errorf("packaging %q: unknown artifact file, cannot hash its content", packaging)
		}
		sum, err := mavenChecksum(ctx, base+"."+ext)
		if err != nil {
			return "", err
		}
		lines = append(lines, ext+" "+sum)
	}
	return sumLines(lines), nil
}

// mavenExt maps a POM packaging to the file extension of its main artifact.
var mavenExt = map[string]string{"jar": "jar", "bundle": "jar", "maven-plugin": "jar", "ejb": "jar", "war": "war", "ear": "ear", "rar": "rar", "aar": "aar"}

// mavenChecksum returns the checksum the repository publishes beside file
// (.sha256, else .sha1), so a jar redeployed under the same version changes
// the id without downloading it. The id is only as fresh as that published
// checksum: a jar redeployed without its checksum file keeps the id. A body
// that is not a checksum (a proxy's page for a missing file) fails, so it
// never stands in for the jar.
func mavenChecksum(ctx context.Context, file string) (string, error) {
	var err error
	for _, alg := range []string{"sha256", "sha1"} {
		var b bytes.Buffer
		if err = get(ctx, file+"."+alg, "", maxMetadata, &b); err == nil {
			if f := strings.Fields(b.String()); len(f) > 0 && checksumRe[alg].MatchString(f[0]) {
				return alg + ":" + strings.ToLower(f[0]), nil
			}
			return "", fmt.Errorf("GET %s.%s: not a checksum", redact(file), alg)
		}
	}
	return "", err
}

var checksumRe = map[string]*regexp.Regexp{
	"sha256": regexp.MustCompile(`^[0-9a-fA-F]{64}$`),
	"sha1":   regexp.MustCompile(`^[0-9a-fA-F]{40}$`),
}

// crate resolves name@=version (Cargo's exact requirement; a bare 1.2.3
// means ^1.2.3 to Cargo) on the crates.io sparse index ($AVAL_CRATES_INDEX)
// to its .crate checksum. Any other requirement binds every index entry's
// version, checksum and yanked flag.
func crate(ctx context.Context, ref string) (string, string, error) {
	pkg, want, err := split("crate", ref, "*")
	if err != nil {
		return "", "", err
	}
	lc := strings.ToLower(pkg)
	var prefix string
	switch len(lc) {
	case 1, 2:
		prefix = fmt.Sprint(len(lc))
	case 3:
		prefix = "3/" + lc[:1]
	default:
		prefix = lc[:2] + "/" + lc[2:4]
	}
	var body bytes.Buffer
	if err := get(ctx, baseURL("https://index.crates.io", "AVAL_CRATES_INDEX")+"/"+prefix+"/"+lc, "", maxMetadata, &body); err != nil {
		return "", "", err
	}
	exact, isExact := strings.CutPrefix(want, "=")
	isExact = isExact && semverRe.MatchString(exact)
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(body.String()), "\n") {
		var e struct {
			Vers, Cksum string
			Yanked      bool
		}
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			return "", "", fmt.Errorf("index entry: %w", err)
		}
		if _, _, err := idOrErr(e.Vers, e.Cksum); err != nil {
			return "", "", err
		}
		if isExact && e.Vers == exact && !e.Yanked { // Cargo never picks a yanked version
			return exact, "sha256:" + e.Cksum, nil
		}
		lines = append(lines, entry(e.Vers, e.Cksum, strconv.FormatBool(e.Yanked)))
	}
	if isExact {
		return "", "", fmt.Errorf("version %s is not published", exact)
	}
	return releases(lines)
}

// gemExactRe is a plain RubyGems version (7.1.3, 7.2.0.beta1), no operator.
var gemExactRe = regexp.MustCompile(`^[0-9]+(\.[0-9A-Za-z]+)*$`)

// gem resolves name@version on RubyGems ($AVAL_RUBYGEMS_URL) to a sha256
// over the checksums of all its platform gems. Any other requirement
// (~>7.1, >=1) binds every version, platform and checksum.
func gem(ctx context.Context, ref string) (string, string, error) {
	pkg, want, err := split("gem", ref, "")
	if err != nil {
		return "", "", err
	}
	var doc []struct{ Number, Sha, Platform string }
	if err := getJSON(ctx, baseURL("https://rubygems.org", "AVAL_RUBYGEMS_URL")+"/api/v1/versions/"+url.PathEscape(pkg)+".json", "", &doc); err != nil {
		return "", "", err
	}
	exact := gemExactRe.MatchString(want)
	var lines []string
	for _, d := range doc {
		if d.Sha == "" {
			return "", "", fmt.Errorf("version %s (%s) has no checksum in the registry", d.Number, d.Platform)
		}
		if !exact || gemCanon(d.Number) == gemCanon(want) {
			lines = append(lines, entry(d.Number, d.Platform, d.Sha))
		}
	}
	if !exact {
		return releases(lines)
	}
	if len(lines) == 0 {
		return "", "", fmt.Errorf("version %s is not published", want)
	}
	slices.Sort(lines)
	return want, sumLines(lines), nil
}

// gemCanon is v as RubyGems compares it: 1.2 equals 1.2.0 and 1.2a equals
// 1.2.a, so an exact version binds every published number equal to it.
// ponytail: it may group more numbers than RubyGems does; that only makes
// the id change more often.
func gemCanon(v string) string {
	var parts [2][]string // the release, then the pre-release from its first word
	for _, t := range gemTokenRe.FindAllString(strings.ToLower(v), -1) {
		i := 0
		if t[0] >= 'a' || len(parts[1]) > 0 {
			i = 1
		}
		parts[i] = append(parts[i], strings.TrimLeft(t, "0")) // "" is zero
	}
	for i, p := range parts {
		for len(p) > 0 && p[len(p)-1] == "" {
			p = p[:len(p)-1]
		}
		parts[i] = p
	}
	return strings.Join(parts[0], ".") + "|" + strings.Join(parts[1], ".")
}

var gemTokenRe = regexp.MustCompile(`[0-9]+|[a-z]+`)

var commitRe = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// git resolves url@ref (a branch, a tag, HEAD or a full ref name) to the
// commit it points to, with git ls-remote; git brings its own credential
// helpers. A full commit id is accepted only when no branch or tag has that
// name: git would check out the ref instead.
func git(ctx context.Context, ref string) (string, string, error) {
	i := strings.LastIndex(ref, "@")
	if i < 0 || i == len(ref)-1 {
		return "", "", errors.New("want <url>@<ref>")
	}
	repo, want := ref[:i], ref[i+1:]
	u, err := url.Parse(repo)
	switch {
	case err != nil:
		return "", "", err
	case !slices.Contains([]string{"https", "http", "ssh", "git", "file"}, u.Scheme):
		return "", "", fmt.Errorf("unsupported URL scheme %q (want https, http, ssh, git or file)", u.Scheme)
	case strings.HasPrefix(want, "-"):
		return "", "", errors.New("a ref must not start with -")
	}
	// The patterns match ref names by their tail; ^{} lists the commit of
	// an annotated tag.
	cmd := exec.CommandContext(ctx, "git", "ls-remote", repo, want, want+"^{}")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("git ls-remote: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	refs, found := findRefs(string(out), want)
	if commitRe.MatchString(want) {
		if len(found) > 0 {
			return "", "", fmt.Errorf("ref %q is both a commit id and a ref name", want)
		}
		return want, want, nil
	}
	return pickRef(refs, found, repo, want)
}

// findRefs parses git ls-remote output and returns it with the names want
// matches: as a full name (HEAD, refs/...), a branch or a tag.
func findRefs(out, want string) (map[string]string, []string) {
	refs := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if sha, name, ok := strings.Cut(l, "\t"); ok {
			refs[name] = sha
		}
	}
	var found []string
	for _, n := range []string{want, "refs/heads/" + want, "refs/tags/" + want} {
		if _, ok := refs[n]; ok && (n == "HEAD" || strings.HasPrefix(n, "refs/")) {
			found = append(found, n)
		}
	}
	return refs, found
}

// pickRef returns the commit of the one ref found, and fails if want is
// both a branch and a tag.
func pickRef(refs map[string]string, found []string, repo, want string) (string, string, error) {
	switch len(found) {
	case 0:
		return "", "", fmt.Errorf("no ref %q in %s", want, repo)
	case 1:
		sha := refs[found[0]]
		if peeled, ok := refs[found[0]+"^{}"]; ok { // an annotated tag
			sha = peeled
		}
		return found[0], sha, nil
	}
	return "", "", fmt.Errorf("ref %q is ambiguous: %s", want, strings.Join(found, ", "))
}

// download resolves a URL to the sha256 of what it serves, up to
// MaxDownload bytes.
func download(ctx context.Context, ref string) (string, string, error) {
	id, err := sha256Of(ctx, ref, MaxDownload)
	return "-", id, err
}

// idOrErr fails when the registry lists a version without a checksum.
func idOrErr(v, id string) (string, string, error) {
	if id == "" || id == "sha256:" {
		return "", "", fmt.Errorf("version %s has no checksum in the registry", v)
	}
	return v, id, nil
}
