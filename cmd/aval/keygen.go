package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/attest"
)

func newKeygenCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "keygen [name]",
		Short: "Create a signing key: name.key and name.pub (default name: aval)",
		Long: `keygen writes a new ECDSA P-256 signing key to name.key (unencrypted PKCS#8
PEM, mode 0600) and its public key to name.pub, the file a policy trusts with
publicKey. It prints the key id the verify report shows for this signer. It
refuses to replace either file without --force.`,
		Args: func(c *cobra.Command, args []string) error {
			if err := cobra.MaximumNArgs(1)(c, args); err != nil {
				return usageError{err}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := "aval"
			if len(args) == 1 {
				name = args[0]
			}
			kid, err := attest.WriteKeyPair(name, force)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %[1]s.key (private, keep it secret) and %[1]s.pub\nkey id: %[2]s\nnext: trust %[1]s.pub in your policy (publicKey: %[1]s.pub) and sign with --key %[1]s.key\n", name, kid)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace name.key and name.pub if they exist")
	return cmd
}
