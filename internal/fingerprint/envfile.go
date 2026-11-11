package fingerprint

import (
	"context"
	"encoding/hex"
	"fmt"
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
// Their names must be exactly the sorted, deduplicated environment.env of c.
// Everything else is hashed from dir as Compute does, and a contract in
// scope.contracts still reads the process environment.
func ComputeRecorded(ctx context.Context, dir string, c *contract.Contract, env []string, outputs ...string) (Fingerprint, error) {
	if env == nil {
		env = []string{} // non-nil: recorded, even when empty
	}
	return compute(ctx, dir, c, nil, env, outputs)
}

// recordedEnv checks that the names of the recorded env lines are exactly
// the sorted, deduplicated names, and returns the lines.
func recordedEnv(lines, names []string) ([]string, error) {
	want := slices.Compact(slices.Sorted(slices.Values(names)))
	got := make([]string, len(lines))
	for i, l := range lines {
		got[i] = strings.Split(l, " ")[1] // ParseEnvFile checked the shape
	}
	if !slices.Equal(got, want) {
		return nil, fmt.Errorf("env file names %v, want environment.env %v", got, want)
	}
	return lines, nil
}
