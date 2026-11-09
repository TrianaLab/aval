package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/TrianaLab/aval/internal/attest"
	"github.com/TrianaLab/aval/internal/cliout"
	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/policy"
)

// defaultPolicy is the trust policy aval init writes.
const defaultPolicy = "policy.yaml"

// cacheabilities are the classes init offers, in prompt order.
var cacheabilities = []contract.Cacheability{contract.Hermetic, contract.ControlledEnvironment, contract.ExternalDependencies, contract.NonDeterministic}

// initOpts are aval init's values. Scope and cacheability have no default:
// what the test reads is declared, never inferred (invariant 2).
type initOpts struct {
	name, cacheability, key string
	sources, command        []string
	keyless, force          bool
}

// missing lists the flags with no value, in prompt order.
func (o *initOpts) missing() []string {
	var m []string
	for _, v := range []struct {
		flag  string
		unset bool
	}{
		{"--name", o.name == ""},
		{"-- <command>", len(o.command) == 0},
		{"--source", len(o.sources) == 0},
		{"--cacheability", o.cacheability == ""},
		{"--key or --keyless", o.key == "" && !o.keyless},
	} {
		if v.unset {
			m = append(m, v.flag)
		}
	}
	return m
}

func newInitCmd() *cobra.Command {
	var o initOpts
	cmd := &cobra.Command{
		Use:   "init --name NAME --source GLOB... --cacheability CLASS --key NAME|--keyless [--force] -- <command>",
		Short: "Write aval.yaml, a signing key and a trust policy for a test",
		Long: `init writes aval.yaml for one test, validated before it is written. With
--key NAME it also writes the signing key pair (NAME.key and NAME.pub, as
keygen does; a trailing .key is dropped) and ` + defaultPolicy + ` trusting that key at trust level developer. It adds
the key and .aval/ to .gitignore. --keyless writes no key and no policy.

Nothing is inferred: --source and --cacheability have no default, and init
never traces the test. It lists the files the source globs match and rejects a
glob that matches nothing. It never replaces aval.yaml, ` + defaultPolicy + ` or the key
without --force. If a write fails after the key pair was written, init
removes the key pair again.

When a value is missing and both stdin and stdout are terminals, init asks for
it. Otherwise it exits 2 and names the missing flags.

Exit codes: 0 written, 1 error (a file exists without --force, or cannot be
written), 2 usage (a missing value, an invalid contract, a glob that matches
nothing, or init cancelled).`,
		Args: func(c *cobra.Command, args []string) error {
			if c.ArgsLenAtDash() != 0 && len(args) > 0 {
				return usageError{fmt.Errorf("unexpected argument %q: the test command goes after --", args[0])}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			o.command = args
			return runInit(cmd, &o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.name, "name", "", "test name (metadata.name): what the test checks")
	f.StringArrayVar(&o.sources, "source", nil, "source glob relative to the current directory (scope.source); repeat it for more")
	f.StringVar(&o.cacheability, "cacheability", "", "hermetic, controlled-environment, external-dependencies or non-deterministic")
	f.StringVar(&o.key, "key", "", "signing key pair NAME.key and NAME.pub to create and trust in "+defaultPolicy+"; a trailing .key is dropped")
	f.BoolVar(&o.keyless, "keyless", false, "sign keyless with Sigstore: no key and no policy are written")
	f.BoolVar(&o.force, "force", false, "replace existing aval.yaml, "+defaultPolicy+" and key files")
	return cmd
}

func runInit(cmd *cobra.Command, o *initOpts) error {
	if o.key != "" && o.keyless {
		return usageError{errors.New("--key and --keyless exclude each other")}
	}
	if err := checkKeyPath(o.key); err != nil {
		return usageError{err}
	}
	out := cmd.OutOrStdout()
	if m := o.missing(); len(m) > 0 {
		if !isTerminal() || !isTTY(out) {
			return usageError{fmt.Errorf("init: missing %s; there are no defaults, and init asks only on a terminal", strings.Join(m, ", "))}
		}
		if err := prompt(bufio.NewReader(cmd.InOrStdin()), out, o); err != nil {
			return err
		}
	}
	doc, files, err := checkInit(o)
	if err != nil {
		return err
	}
	styled, color := outputStyle(isTTY(out), plainFlag(cmd), os.Getenv)
	line := func(glyph, s string) {
		if styled {
			cliout.Step(out, color, glyph, s)
			return
		}
		fmt.Fprintln(out, s)
	}
	line(cliout.GlyphNeutral, previewFiles(files))
	if err := writeInit(o, doc, line); err != nil {
		return err
	}
	next := "next: export AVAL_REGISTRY=<oci repository> AVAL_POLICY=" + defaultPolicy + ", then aval run --push --key " + cliout.OneLine(o.keyName()) + ".key"
	if o.keyless {
		next = "next: write a trust policy for your OIDC identity (docs: guides/trust-policy), export AVAL_REGISTRY=<oci repository> AVAL_POLICY=<policy file>, then aval run --push"
	}
	if o.cacheability == string(contract.NonDeterministic) {
		next += "; non-deterministic results are never reused"
	}
	if styled {
		next = "Wrote the contract for " + o.name + "; " + next + "."
	}
	fmt.Fprintln(out, next)
	return nil
}

// checkKeyPath rejects a --key outside the current directory: its
// .gitignore line would ignore nothing and the policy would point outside
// the repository.
func checkKeyPath(key string) error {
	if key != "" && !filepath.IsLocal(key) {
		return fmt.Errorf("--key %q must be a relative path inside the current directory", key)
	}
	return nil
}

// keyName is --key without its .key suffix, the name keygen takes.
func (o *initOpts) keyName() string { return strings.TrimSuffix(o.key, ".key") }

// checkInit validates everything before a file is written: the contract
// through internal/contract, the globs against the tree, and that no file
// would be replaced without --force. It returns the contract document and
// the matched files.
func checkInit(o *initOpts) ([]byte, []string, error) {
	doc := marshalYAML(contract.Contract{
		APIVersion: contract.APIVersion, Kind: contract.Kind,
		Metadata:     contract.Metadata{Name: o.name},
		Runner:       contract.Runner{Command: o.command},
		Scope:        contract.Scope{Source: o.sources},
		Cacheability: contract.Cacheability(o.cacheability),
	})
	if _, err := contract.Parse(doc); err != nil {
		return nil, nil, usageError{listError{"init: invalid contract:", err}}
	}
	files, err := matched(o.sources)
	if err != nil {
		return nil, nil, usageError{err}
	}
	paths := []string{contract.DefaultFile}
	if !o.keyless {
		paths = append(paths, defaultPolicy, o.keyName()+".key", o.keyName()+".pub")
	}
	if err := checkAbsent(paths, o.force); err != nil {
		return nil, nil, err
	}
	return doc, files, nil
}

// checkAbsent fails when one of paths exists and force is not set.
func checkAbsent(paths []string, force bool) error {
	for _, p := range paths {
		_, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return err
		case !force:
			return fmt.Errorf("%s already exists; pass --force to replace it", cliout.OneLine(p))
		}
	}
	return nil
}

// writeFile writes p as a new file. With force it first removes what is
// there, and O_EXCL never follows a symlink into another file, as keygen.
func writeFile(p string, data []byte, force bool) error {
	if force {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}

// writeInit writes the key pair and policy (unless keyless), the contract
// and the .gitignore lines, reporting each through line. A failure after the
// key pair was written removes it, so a rerun needs no --force for it.
func writeInit(o *initOpts, doc []byte, line func(glyph, s string)) (err error) {
	ignore := []string{fingerprint.OutputDir + "/"}
	if !o.keyless {
		name := o.keyName()
		var kid string
		if kid, err = attest.WriteKeyPair(name, o.force); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				_ = os.Remove(name + ".key")
				_ = os.Remove(name + ".pub")
			}
		}()
		shown := cliout.OneLine(name)
		line(cliout.GlyphDone, fmt.Sprintf("wrote %[1]s.key (private: keep it secret, do not commit it) and %[1]s.pub, key id %[2]s", shown, kid))
		pol := marshalYAML(policyDoc{contract.APIVersion, policy.Kind, "developer", []policyIdentity{{name + ".pub", "developer"}}})
		_, err = policy.Parse(pol, ".")
		if err == nil {
			err = writeFile(defaultPolicy, pol, o.force)
		}
		if err != nil {
			return err
		}
		line(cliout.GlyphDone, fmt.Sprintf("wrote %s: trusts %s.pub at trust level developer", defaultPolicy, shown))
		ignore = append([]string{name + ".key"}, ignore...)
	}
	if err := writeFile(contract.DefaultFile, doc, o.force); err != nil {
		return err
	}
	line(cliout.GlyphDone, "wrote "+contract.DefaultFile)
	added, err := gitignore(ignore)
	if err != nil {
		return err
	}
	if len(added) > 0 {
		line(cliout.GlyphDone, "added "+strings.Join(added, ", ")+" to .gitignore")
	}
	return nil
}

// policyDoc is the policy init writes, in this field order.
type policyDoc struct {
	APIVersion    string           `yaml:"apiVersion"`
	Kind          string           `yaml:"kind"`
	MinTrustLevel string           `yaml:"minTrustLevel"`
	Identities    []policyIdentity `yaml:"identities"`
}

type policyIdentity struct {
	PublicKey  string `yaml:"publicKey"`
	TrustLevel string `yaml:"trustLevel"`
}

// marshalYAML encodes v with two-space indentation. ponytail: v is always one
// of init's own structs of strings, which cannot fail to encode.
func marshalYAML(v any) []byte {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	_ = e.Encode(v)
	return b.Bytes()
}

// matched returns the files the source globs select in the current
// directory, through the fingerprint itself, so the preview is exactly what
// run hashes. A glob that matches nothing is an error: it is a typo, and
// the fingerprint rejects it too.
func matched(sources []string) ([]string, error) {
	for _, g := range sources {
		if err := contract.ValidateGlob(g); err != nil {
			return nil, fmt.Errorf("source glob %w", err)
		}
	}
	fp, err := fingerprint.ComputeFS(os.DirFS("."), &contract.Contract{Scope: contract.Scope{Source: sources}}, os.LookupEnv)
	if err != nil {
		return nil, err
	}
	var files []string
	for l := range strings.Lines(fp.Manifest) {
		if f := strings.SplitN(strings.TrimSuffix(l, "\n"), " ", 4); len(f) == 4 && f[0] == "source" {
			files = append(files, f[3])
		}
	}
	return files, nil
}

// maxPreview is how many matched files init lists.
const maxPreview = 10

// previewFiles is the one-line preview of the matched files.
func previewFiles(files []string) string {
	// File names come from the tree: OneLine keeps their escape sequences
	// out of the terminal.
	shown, more := slices.Clone(files[:min(len(files), maxPreview)]), ""
	if len(files) > maxPreview {
		more = fmt.Sprintf(" and %d more", len(files)-maxPreview)
	}
	for i, f := range shown {
		shown[i] = cliout.OneLine(f)
	}
	noun := "files"
	if len(files) == 1 {
		noun = "file"
	}
	return fmt.Sprintf("source matches %d %s: %s%s", len(files), noun, strings.Join(shown, ", "), more)
}

// gitignore appends the lines .gitignore lacks and returns them.
func gitignore(lines []string) ([]string, error) {
	old, err := os.ReadFile(".gitignore")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	have := strings.Split(strings.ReplaceAll(string(old), "\r\n", "\n"), "\n")
	var add []string
	for _, l := range lines {
		if !slices.Contains(have, l) && !slices.Contains(have, "/"+l) {
			add = append(add, l)
		}
	}
	if len(add) == 0 {
		return nil, nil
	}
	body := strings.Join(add, "\n") + "\n"
	if len(old) > 0 && !bytes.HasSuffix(old, []byte("\n")) {
		body = "\n" + body
	}
	f, err := os.OpenFile(".gitignore", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	_, err = f.WriteString(body)
	return add, errors.Join(err, f.Close())
}

// errCancelled: the user closed stdin at a prompt.
var errCancelled = usageError{errors.New("init cancelled: nothing written")}

// prompt asks, in numbered plain questions, for each value o lacks. A blank
// or invalid answer asks again; end of input cancels.
func prompt(in *bufio.Reader, out io.Writer, o *initOpts) error {
	var classes strings.Builder
	for i, c := range cacheabilities {
		fmt.Fprintf(&classes, " %d) %s", i+1, c)
	}
	questions := []struct {
		unset  bool
		q      string
		accept func(string) error
	}{
		{o.name == "", "Test name (what it checks, e.g. unit)", func(s string) error {
			if err := contract.ValidateName(s); err != nil {
				return err
			}
			o.name = s
			return nil
		}},
		{len(o.command) == 0, "Test command (split on spaces, no shell)", func(s string) error { o.command = strings.Fields(s); return nil }},
		{len(o.sources) == 0, "Source globs the test reads (space-separated, e.g. src/** go.mod)", func(s string) error {
			_, err := matched(strings.Fields(s))
			if err == nil {
				o.sources = strings.Fields(s)
			}
			return err
		}},
		{o.cacheability == "", "Cacheability" + classes.String(), func(s string) error {
			if slices.Contains(cacheabilities, contract.Cacheability(s)) {
				o.cacheability = s
				return nil
			}
			i, err := strconv.Atoi(s)
			if err != nil || i < 1 || i > len(cacheabilities) {
				return fmt.Errorf("answer 1 to %d or a class name", len(cacheabilities))
			}
			o.cacheability = string(cacheabilities[i-1])
			return nil
		}},
		{o.key == "" && !o.keyless, "Signing key file to create (e.g. aval.key), or keyless", func(s string) error {
			if s == "keyless" {
				o.keyless = true
				return nil
			}
			if err := checkKeyPath(s); err != nil {
				return err
			}
			name := strings.TrimSuffix(s, ".key")
			if err := checkAbsent([]string{name + ".key", name + ".pub"}, o.force); err != nil {
				return err
			}
			o.key = s
			return nil
		}},
	}
	n, total := 0, len(o.missing())
	for _, q := range questions {
		if !q.unset {
			continue
		}
		n++
		if err := ask(in, out, fmt.Sprintf("%d/%d %s: ", n, total, q.q), q.accept); err != nil {
			return err
		}
	}
	return nil
}

// ask writes q until accept takes a non-blank answer, printing why it did
// not. End of input cancels.
func ask(in *bufio.Reader, out io.Writer, q string, accept func(string) error) error {
	for {
		fmt.Fprint(out, q)
		s, err := in.ReadString('\n')
		s = strings.TrimSpace(s)
		if err != nil && s == "" {
			fmt.Fprintln(out)
			return errCancelled
		}
		if s == "" {
			continue
		}
		if err := accept(s); err != nil {
			fmt.Fprintln(out, "  "+cliout.OneLine(err.Error()))
			continue
		}
		return nil
	}
}
