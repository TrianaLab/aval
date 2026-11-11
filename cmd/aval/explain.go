package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
)

func newExplainCmd() *cobra.Command {
	var file, test, output string
	var digest, expand, all bool
	cmd := &cobra.Command{
		Use:   "explain",
		Short: "Print what the execution fingerprint covers, with the digest last",
		Long: `explain prints the fingerprint manifest (every input that was hashed) under a
header, then the fingerprint as the last line. --digest prints only the
fingerprint. explain never looks up a result and never runs the test; the
fingerprint is the one 'aval run' computes with its default --result-file and
without --report-file or --stats-file inside the contract root. With
scope.root it prints the resolved root after the header.

--expand prints the test as the standalone kind: Test it expands to (a test
of a TestSet with defaults, fragments and matrix applied). --all checks every
test of the file: it prints one "name fingerprint" line per test (-o json:
one object per test), reports every failing test by name instead of stopping
at the first, and executes nothing, not even environment.probe.

Exit codes: 0 printed, 1 the fingerprint cannot be computed (unprovable
inputs), 2 usage or invalid aval.yaml. With --all, 2 when any failing test
is invalid, else 1 when any fingerprint cannot be computed.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if err := checkExplainFlags(output, test, all, expand, digest); err != nil {
				return err
			}
			switch {
			case all:
				return explainAll(cmd.Context(), out, file, output)
			case expand:
				return explainExpand(out, file, test)
			}
			result := defaultResult
			testOutputs(cmd, test, testOutput{&result, "result.json", nil})
			c, root, fp, err := loadAndFingerprint(cmd.Context(), file, test, result)
			if err != nil {
				return err
			}
			if !digest {
				fmt.Fprintf(out, "fingerprint manifest for %s (%s)\n", c.Metadata.Name, fingerprint.Version)
				if c.Scope.Root != "" {
					fmt.Fprintf(out, "root %s\n", root)
				}
			}
			// ponytail: run's own printer, so explain and run --explain cannot drift;
			// its error is always nil because fpErr is nil.
			_ = printFingerprint(out, cmd.ErrOrStderr(), fp, nil, runFlags{explain: !digest, fingerprintOnly: digest})
			if !digest {
				printFindings(cmd.ErrOrStderr(), fp)
				fmt.Fprintf(out, "fingerprint %s\n", fp.Digest)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "contract", "f", contract.DefaultFile, "path to the test contract")
	testFlag(f, &test)
	f.BoolVar(&digest, "digest", false, "print only the fingerprint (sha256:...)")
	f.BoolVar(&expand, "expand", false, "print the test as the standalone kind: Test it expands to")
	f.BoolVar(&all, "all", false, "check and fingerprint every test of the file, one line per test")
	f.StringVarP(&output, "output", "o", "text", "output format of --all: text or json")
	return cmd
}

// checkExplainFlags rejects flag combinations explain does not take.
func checkExplainFlags(output, test string, all, expand, digest bool) error {
	switch {
	case output != "text" && output != "json":
		return usageError{fmt.Errorf("--output must be text or json, got %q", output)}
	case all && (test != "" || expand || digest):
		return usageError{errors.New("--all excludes --test, --expand and --digest")}
	case output == "json" && !all:
		return usageError{errors.New("--output json needs --all")}
	case expand && digest:
		return usageError{errors.New("--expand and --digest exclude each other")}
	}
	return nil
}

// explainExpand prints the test named test of file as the standalone
// kind: Test it expands to.
func explainExpand(w io.Writer, file, test string) error {
	c, err := loadContract(file, test)
	if err != nil {
		return err
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	_ = enc.Encode(c) // ponytail: stdout errors are ignored, as by every Fprintf here
	return nil
}

// explained is one test in the output of explain --all -o json: its
// fingerprint, or why it has none.
type explained struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Error       string `json:"error,omitempty"`
}

// explainAll fingerprints every test of file, as 'aval run --test' would,
// and prints one "name fingerprint" line per test (output text) or one
// object per test (json). It checks every test and returns one error per
// failing test, naming it. It executes nothing.
func explainAll(ctx context.Context, w io.Writer, file, output string) error {
	cs, err := contract.LoadAll(file)
	if err != nil {
		return contractErr(err) // every problem of every test, listed
	}
	list := make([]explained, len(cs))
	var errs []error
	for i, c := range cs {
		_, _, fp, err := rootAndFingerprint(ctx, file, c, "", filepath.Join(fingerprint.OutputDir, c.Metadata.Name, "result.json"))
		list[i] = explained{Name: c.Metadata.Name, Fingerprint: fp.Digest}
		if err != nil {
			list[i].Error = reason(err)
			errs = append(errs, fmt.Errorf("%s: %w", c.Metadata.Name, err))
		}
	}
	if output == "json" {
		raw, _ := json.MarshalIndent(struct {
			Tests []explained `json:"tests"`
		}{list}, "", "  ") // ponytail: plain structs, cannot fail
		fmt.Fprintf(w, "%s\n", raw)
	} else {
		for _, e := range list {
			if e.Error == "" {
				fmt.Fprintf(w, "%s %s\n", e.Name, e.Fingerprint)
			}
		}
	}
	return errors.Join(errs...)
}
