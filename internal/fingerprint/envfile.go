package fingerprint

import (
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/TrianaLab/aval/internal/contract"
)

// The env file carries the env section of a manifest from the job that ran
// the test to the job that publishes it ('aval run' writes it, 'aval push
// --env-file' reads it): Version on the first line, then the manifest's
// "env NAME unset|<sha256 hex>" lines exactly as hashed. It holds no values,
// only what 'aval explain' prints, and is neither signed nor published.

// EnvFile returns the env file for f: Version, then the env lines of its
// manifest (the top contract's; referenced contracts are in their digests).
func (f Fingerprint) EnvFile() string {
	var b strings.Builder
	b.WriteString(Version + "\n")
	for l := range strings.Lines(f.Manifest) {
		if strings.HasPrefix(l, "env ") {
			b.WriteString(l)
		}
	}
	return b.String()
}

// ParseEnvFile returns the env lines of an env file. A different manifest
// version, a malformed line, a hash that is not "unset" or lowercase sha256
// hex, or a name given twice is an error.
func ParseEnvFile(raw []byte) ([]string, error) {
	text, ok := strings.CutSuffix(string(raw), "\n")
	if !ok {
		return nil, fmt.Errorf("env file does not end in a newline")
	}
	lines := strings.Split(text, "\n")
	if lines[0] != Version {
		return nil, fmt.Errorf("env file manifest version %q, want %q", lines[0], Version)
	}
	seen := map[string]bool{}
	for i, l := range lines[1:] {
		f := strings.Split(l, " ")
		if len(f) != 3 || f[0] != "env" || f[1] == "" || !isHash(f[2]) {
			return nil, fmt.Errorf("env file line %d: want \"env NAME unset|<sha256 hex>\", got %q", i+2, l)
		}
		if seen[f[1]] {
			return nil, fmt.Errorf("env file line %d: %s given twice", i+2, f[1])
		}
		seen[f[1]] = true
	}
	return lines[1:], nil
}

func isHash(s string) bool {
	if s == "unset" {
		return true
	}
	_, err := hex.DecodeString(s)
	return err == nil && len(s) == 64 && s == strings.ToLower(s)
}

// ComputeRecorded is Compute with the env section of c taken from env, the
// lines of an env file (ParseEnvFile), instead of the process environment.
// They must be lines the environment.env of c could give (recordedEnv).
// Everything else is hashed from dir as Compute does, and a contract in
// scope.contracts still reads the process environment.
func ComputeRecorded(ctx context.Context, dir string, c *contract.Contract, env []string, outputs ...string) (Fingerprint, error) {
	if env == nil {
		env = []string{} // non-nil: recorded, even when empty
	}
	return compute(ctx, dir, c, nil, env, outputs)
}

// recordedEnv checks that the recorded env lines are lines envLines could
// give for entries, the environment.env of c, and returns them. The lines
// must be sorted; every exact name must have a line; every other line must
// be a set variable (not "unset") whose name a prefix pattern matches, as
// envKey writes it. A name no entry covers, a missing exact name or an
// unset pattern line is an error. Which variables a pattern matched in the
// run job cannot be checked here: a line left out changes the digest.
func recordedEnv(lines, entries []string) ([]string, error) {
	exact := map[string]bool{}
	var prefixes []string
	for _, e := range entries {
		if p, ok := strings.CutSuffix(e, "*"); ok {
			prefixes = append(prefixes, envKey(p))
		} else {
			exact[e] = true
		}
	}
	if !slices.IsSorted(lines) {
		return nil, fmt.Errorf("env file names are not sorted")
	}
	seen := map[string]bool{}
	for _, l := range lines {
		f := strings.Split(l, " ") // ParseEnvFile checked the shape
		name := f[1]
		seen[name] = true
		matched := name == envKey(name) && f[2] != "unset" &&
			slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(name, p) })
		if !exact[name] && !matched {
			return nil, fmt.Errorf("env file names %s, which no environment.env entry %v gives", name, entries)
		}
	}
	for _, e := range slices.Sorted(maps.Keys(exact)) {
		if !seen[e] {
			return nil, fmt.Errorf("env file names lack %s from environment.env %v", e, entries)
		}
	}
	return lines, nil
}
