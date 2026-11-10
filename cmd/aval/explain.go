package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
)

func newExplainCmd() *cobra.Command {
	var file string
	var digest bool
	cmd := &cobra.Command{
		Use:   "explain",
		Short: "Print what the execution fingerprint covers, with the digest last",
		Long: `explain prints the fingerprint manifest (every input that was hashed) under a
header, then the fingerprint as the last line. --digest prints only the
fingerprint. explain never looks up a result and never runs the test; the
fingerprint is the one 'aval run' computes with its default --result-file and
without --report-file or --stats-file inside the contract root. With
scope.root it prints the resolved root after the header.

Exit codes: 0 printed, 1 the fingerprint cannot be computed (unprovable
inputs), 2 usage or invalid aval.yaml.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, root, fp, err := loadAndFingerprint(cmd.Context(), file, defaultResult)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
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
	f.BoolVar(&digest, "digest", false, "print only the fingerprint (sha256:...)")
	return cmd
}
