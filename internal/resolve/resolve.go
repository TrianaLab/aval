//go:build !js

// Package resolve turns the artifacts a contract declares by name in
// scope.resolve (an image tag, a package version range, a git branch, a
// URL) into immutable ids (a digest, a checksum, a commit) at fingerprint
// time, so the fingerprint changes when the content behind the name does.
// A package spec that is not one exact version binds the hash of the
// package's whole release list instead (see floating).
//
// Resolution is opt-in: only contracts with scope.resolve touch the
// network. It is stateless (no cache) and fails closed: a reference that
// cannot be resolved fails the fingerprint, so nothing is looked up and the
// test runs (invariant 3). One small resolver per ecosystem keeps registry
// specifics out of the core (invariant 8).
package resolve

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/TrianaLab/aval/internal/contract"
)

const (
	// Timeout bounds the resolution of every reference of one contract.
	Timeout = 2 * time.Minute
	// MaxDownload bounds a file Aval downloads to hash (url, a Go module zip).
	MaxDownload = 256 << 20
	// maxMetadata bounds one registry metadata document.
	maxMetadata = 64 << 20
)

// Version is the Aval version sent in the User-Agent of every resolver
// request: Maven Central throttles Go's default one. cmd/aval sets it.
var Version = "dev"

// Resolver resolves one reference of its ecosystem. It returns the version
// the name resolved to and the immutable id of its content.
type Resolver func(ctx context.Context, ref string) (version, id string, err error)

// Resolvers has one entry per contract.ResolveKinds.
var Resolvers = map[string]Resolver{
	"image": image, "npm": npm, "pypi": pypi, "go": gomod, "maven": maven,
	"crate": crate, "gem": gem, "git": git, "url": download,
}

// All resolves refs in order, within Timeout. The first failure is returned
// with the reference it is about.
func All(ctx context.Context, refs []contract.Reference) ([]Result, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	out := make([]Result, 0, len(refs))
	for _, r := range refs {
		start := time.Now()
		var v, id string
		err := fmt.Errorf("unknown kind %q", r.Kind)
		if res, ok := Resolvers[r.Kind]; ok {
			v, id, err = res(ctx, r.Ref)
		}
		if err == nil && (!token(v) || !token(id)) {
			// The ids go into a line-oriented manifest: a space or a
			// newline from a server could forge lines.
			err = fmt.Errorf("the registry answered an id that is not one token: %q %q", v, id)
		}
		if err != nil {
			return nil, fmt.Errorf("resolve %s %s: %w (see docs/resolve/index.md)", r.Kind, r.Ref, err)
		}
		out = append(out, Result{Kind: r.Kind, Ref: r.Ref, Version: v, ID: id, Took: time.Since(start)})
	}
	return out, nil
}

func token(s string) bool {
	return s != "" && strings.IndexFunc(s, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) < 0
}

// client is the HTTP client of every resolver but image and git. The
// context deadline of All bounds it too. net/http keeps credentials on a
// redirect to a subdomain or to plain http, so any host change or a
// downgrade to http drops them.
var client = &http.Client{Timeout: time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if !secure(req.URL) || req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
	}
	return nil
}}

var errNotFound = errors.New("404 Not Found")

// get writes the body of url to w, at most limit bytes. Credentials come
// from ~/.npmrc or ~/.netrc (see authorize); they are sent as headers, so
// they never appear in a URL or an error. net/http drops them on a redirect
// to another host. A base URL taken from an environment variable may carry
// userinfo, so every error shows the URL redacted.
// A 404 wraps errNotFound.
func get(ctx context.Context, url, accept string, limit int64, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("GET %s: invalid URL", redact(url))
	}
	shown := redact(url)
	if req.URL.User != nil && !secure(req.URL) {
		return fmt.Errorf("GET %s: credentials in an http URL would be sent in clear: use https", shown)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "aval/"+Version+" (+https://github.com/TrianaLab/aval)")
	authorize(req)
	resp, err := client.Do(req)
	if err != nil {
		// net/http masks only the password in *url.Error; show our own URL.
		if ue, ok := err.(*neturl.Error); ok {
			err = ue.Err
		}
		return fmt.Errorf("GET %s: %w", shown, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("GET %s: %w", shown, errNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", shown, resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("GET %s: larger than %d bytes", shown, limit)
	}
	return err
}

func getJSON(ctx context.Context, url, accept string, v any) error {
	var b bytes.Buffer
	if err := get(ctx, url, accept, maxMetadata, &b); err != nil {
		return err
	}
	if err := json.Unmarshal(b.Bytes(), v); err != nil {
		return fmt.Errorf("GET %s: %w", redact(url), err)
	}
	return nil
}

// redact returns s with its userinfo, path and query masked: a token may
// sit in the user name, the password, or the path of a registry URL
// (Gemfury: https://npm-proxy.fury.io/<TOKEN>/user/). The error names the
// reference, which tells the path.
// ponytail: masks every path, also a public one; mask only the path from a
// registry variable if the host alone proves too little.
func redact(s string) string {
	u, err := neturl.Parse(s)
	if err != nil {
		return "<url>"
	}
	if u.User != nil {
		u.User = neturl.User("xxxxx")
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" {
		u.Path, u.RawPath, u.RawQuery = "/xxxxx", "", ""
	}
	u.Fragment = ""
	return u.String()
}

// sha256Of returns "sha256:<hex>" of the body of url.
func sha256Of(ctx context.Context, url string, limit int64) (string, error) {
	h := sha256.New()
	if err := get(ctx, url, "", limit, h); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// sumLines returns "sha256:<hex>" over lines, which the caller sorted: the
// id of a version made of several files (wheels, platform gems).
func sumLines(lines []string) string {
	h := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return "sha256:" + hex.EncodeToString(h[:])
}

// baseURL is the registry or mirror from the environment variable env,
// without a trailing slash, or def.
func baseURL(def string, env ...string) string {
	for _, e := range env {
		def = cmp.Or(os.Getenv(e), def)
	}
	return strings.TrimSuffix(def, "/")
}

// authorize adds the npm token configured for the request URL in .npmrc
// or, failing that, the .netrc machine entry for its host. Credentials go
// only over https or to a loopback host, so a contract naming an http URL
// never receives them in clear.
func authorize(req *http.Request) {
	if !secure(req.URL) {
		return
	}
	if tok := npmToken(req.URL.Host + req.URL.Path); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
		return
	}
	if login, pass, ok := netrc(req.URL.Hostname()); ok {
		req.SetBasicAuth(login, pass)
	}
}

// secure reports whether credentials may be sent to u.
func secure(u *neturl.URL) bool {
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "https" || u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
}

// configFile reads the file at p, else name in the home directory.
func configFile(p, name string) string {
	if p == "" {
		home, _ := os.UserHomeDir() // ponytail: no home means no file
		p = filepath.Join(home, name)
	}
	b, _ := os.ReadFile(p) // a missing file means no credentials
	return string(b)
}

// npmToken returns the _authToken of the longest `//host/path/:_authToken=`
// line in .npmrc that prefixes hostPath on a path boundary, with ${VAR}
// expanded: //host:_authToken never goes to host.attacker.tld.
// ponytail: only per-registry tokens; _auth, username and password lines
// are ignored.
func npmToken(hostPath string) string {
	vals, err := npmrc()
	if err != nil {
		return "" // which .npmrc npm reads, or what it says, is unclear: send no token
	}
	tok, best := "", 0
	for k, v := range vals {
		k, ok := strings.CutSuffix(k, ":_authToken")
		k = strings.TrimPrefix(k, "//")
		match := k == hostPath || strings.HasSuffix(k, "/") && strings.HasPrefix(hostPath, k) || strings.HasPrefix(hostPath, k+"/")
		if ok && match && len(k) > best {
			tok, best = v, len(k)
		}
	}
	return tok
}

// npmrc returns the settings of the .npmrc npm reads
// ($npm_config_userconfig, else ~/.npmrc), read as npm's ini parser reads
// them: comments, quotes and escapes, the last line wins, ${VAR} expanded
// in keys and values as npm expands it (an undefined variable stays
// literal). A line with U+2028 or U+2029, which npm drops, fails.
// Keys under a [section] are not npm settings and are skipped. A line npm
// reads as a flag (no =) or a list (key[]=), a quoted key or value that is
// JSON other than a string, or a section named like a setting, fails: Aval
// cannot tell what npm would use.
func npmrc() (map[string]string, error) {
	_, userconfig, err := npmEnv("userconfig")
	if err != nil {
		return nil, err
	}
	vals := map[string]string{}
	inSection := false
	lines := strings.Split(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(configFile(userconfig, ".npmrc")), "\n")
	for i, l := range lines {
		bad := fmt.Errorf(".npmrc line %d: Aval cannot read it as a key=value setting: fix it", i+1)
		if t := strings.TrimSpace(l); t == "" || t[0] == ';' || t[0] == '#' {
			continue
		}
		// ini's (.*)$ does not match across U+2028/U+2029: npm drops the line.
		if strings.ContainsAny(l, "\u2028\u2029") {
			return nil, bad
		}
		if m := iniSectionRe.FindStringSubmatch(l); m != nil {
			// ini nests [a.b] under a, replacing a setting a (npm then errors).
			name, _, _ := strings.Cut(iniValue(m[1]), ".")
			if _, ok := vals[name]; ok {
				return nil, bad
			}
			inSection = true
			continue
		}
		if inSection {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if k = npmEnvReplace(iniValue(k)); !ok || k == "" || strings.HasSuffix(k, "[]") || strings.Contains(k, "\n") {
			return nil, bad
		}
		if v = npmEnvReplace(iniValue(v)); strings.Contains(v, "\n") {
			return nil, bad
		}
		vals[k] = v
	}
	return vals, nil
}

var npmEnvRe = regexp.MustCompile(`(\\*)\$\{([^${}?]+)(\?)?\}`)

// npmEnvReplace expands ${VAR} as @npmcli/config does: an undefined
// variable stays literal unless written ${VAR?}, and an odd run of
// backslashes escapes the expression.
func npmEnvReplace(s string) string {
	return npmEnvRe.ReplaceAllStringFunc(s, func(m string) string {
		p := npmEnvRe.FindStringSubmatch(m)
		esc := p[1]
		if len(esc)%2 == 1 {
			return m[(len(esc)+1)/2:]
		}
		v, ok := os.LookupEnv(p[2])
		if !ok && p[3] != "?" {
			v = "${" + p[2] + "}"
		}
		return esc[len(esc)/2:] + v
	})
}

var iniSectionRe = regexp.MustCompile(`^\[([^\]]*)\]\s*$`)

// iniValue is a key or value as the ini package (npm) reads it: a quoted
// string unquoted, else cut at the first unescaped ; or #, with \; \# \\
// unescaped, and trimmed.
func iniValue(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		if s[0] == '\'' {
			s = s[1 : len(s)-1]
		}
		var u any
		if json.Unmarshal([]byte(s), &u) == nil {
			if str, ok := u.(string); ok {
				return str
			}
			return "\n" // npm reads a JSON array, number, bool or null here (['registry'] is the key registry): npmrc fails the line
		}
		return s
	}
	var b strings.Builder
	esc := false
loop:
	for _, c := range s {
		switch {
		case esc:
			if !strings.ContainsRune(`\;#`, c) {
				b.WriteByte('\\')
			}
			b.WriteRune(c)
			esc = false
		case c == ';' || c == '#':
			break loop
		case c == '\\':
			esc = true
		default:
			b.WriteRune(c)
		}
	}
	if esc {
		b.WriteByte('\\')
	}
	return strings.TrimSpace(b.String())
}

// netrc returns the login and password of the machine entry for host in
// .netrc ($NETRC).
// ponytail: tokens only, no macdef or quoting. The default entry is never
// used (curl would send it to any host), so a contract naming a host cannot
// collect it.
func netrc(host string) (login, pass string, ok bool) {
	f := strings.Fields(configFile(os.Getenv("NETRC"), ".netrc"))
	cur := ""
	for i := 0; i < len(f); i++ {
		if f[i] == "default" {
			cur = ""
			continue
		}
		if i+1 >= len(f) {
			break
		}
		k, v := f[i], f[i+1]
		i++
		switch {
		case k == "machine":
			cur = v
		case cur == "" || cur != host:
		case k == "login":
			login, ok = v, true
		case k == "password":
			pass, ok = v, true
		}
	}
	return login, pass, ok
}
