package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

func TestExitCode(t *testing.T) {
	cases := map[error]int{
		nil:                                ExitOK,
		usageError{errors.New("bad flag")}: ExitUsage,
		fmt.Errorf("x: %w", evidence.ErrNotFound):        ExitMiss,
		&evidence.VerificationError{Reason: "untrusted"}: ExitVerificationFailed,
		fmt.Errorf("x: %w", evidence.ErrNotImplemented):  ExitNotImplemented,
		errors.New("boom"): ExitError,
	}
	for err, want := range cases {
		if got := exitCode(err); got != want {
			t.Errorf("exitCode(%v) = %d, want %d", err, got, want)
		}
	}
	if ExitMiss == ExitVerificationFailed {
		t.Fatal("miss and verification failure must have distinct exit codes")
	}
}

func TestStrayArgsAreUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {"run", "extra"}, {"verify", "extra"}} {
		root := newRootCmd()
		root.SetArgs(args)
		if got := exitCode(root.Execute()); got != ExitUsage {
			t.Errorf("aval %v: exit %d, want %d", args, got, ExitUsage)
		}
	}
}
