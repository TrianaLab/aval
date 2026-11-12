package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/gen"
	"github.com/TrianaLab/aval/internal/gen/rust"
)

// adapters are the languages aval gen knows, by name.
var adapters = map[string]gen.Adapter{
	"rust": rust.Adapter{},
}

func newGenCmd() *cobra.Command {
	var file string
	var check bool
	cmd := &cobra.Command{
		Use:   "gen <language>",
		Short: "Generate one test per unit from a dependency graph into a TestSet",
		Long: `gen writes declared scope from a repository's dependency graph into the
kind: TestSet in -f. For each unit (a package, module or project) it writes
a fragment with the unit's own files and a test that uses the fragments of
the unit and of every unit it depends on, both named LANGUAGE.UNIT (such as
go.internal.contract). A change to a unit then re-runs its test and the
tests of the units that depend on it.

gen owns only the fragments and tests whose names start with "LANGUAGE.";
every other line, comments included, is kept as it is. Shared inputs
(lockfiles, root build files, toolchain, cacheability) belong in the
hand-written defaults. aval run, verify and push never run gen: commit its
output and check it in CI with --check.

Languages: ` + languages() + `.

Exit codes: 0 written or up to date, 1 error (the language tool failed, the
dependency graph is invalid, the file cannot be written) or with --check out
of date (the diff is printed), 2 usage (an unknown language, no TestSet at
-f, or a result that is not a valid TestSet).`,
		Args: func(c *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(c, args); err != nil {
				return usageError{err}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			a, ok := adapters[args[0]]
			if !ok {
				return usageError{fmt.Errorf("gen: unknown language %q (available: %s)", args[0], languages())}
			}
			return runGen(cmd, a, args[0], file, check)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "aval.yaml", "the kind: TestSet to write")
	cmd.Flags().BoolVar(&check, "check", false, "write nothing; exit 1 with a diff when the generated entries are out of date")
	return cmd
}

// languages lists the adapters for help and errors.
func languages() string {
	return strings.Join(slices.Sorted(maps.Keys(adapters)), ", ")
}

func runGen(cmd *cobra.Command, a gen.Adapter, lang, file string, check bool) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return usageError{fmt.Errorf("gen: %w (start a TestSet with aval init --set)", err)}
	}
	root, err := gen.Root(data, filepath.Dir(file))
	if err != nil {
		return usageError{fmt.Errorf("gen: %s: %w", file, err)}
	}
	units, err := a.Units(root)
	if err != nil {
		return fmt.Errorf("gen %s: %w", lang, err)
	}
	out, err := gen.Rewrite(data, lang, units)
	switch {
	case errors.Is(err, gen.ErrUnits):
		return fmt.Errorf("gen %s: %w", lang, err)
	case err != nil:
		return usageError{fmt.Errorf("gen %s: %s: %w", lang, file, err)}
	}
	w := cmd.OutOrStdout()
	if bytes.Equal(out, data) {
		fmt.Fprintf(w, "%s: the %s entries are up to date\n", file, lang)
		return nil
	}
	if check {
		fmt.Fprint(w, gen.Diff(file, file+" (generated)", data, out))
		return fmt.Errorf("gen %s --check: %s is out of date: run aval gen %s -f %s and commit it", lang, file, lang, file)
	}
	if err := replaceFile(file, out); err != nil {
		return fmt.Errorf("gen %s: %w", lang, err)
	}
	fmt.Fprintf(w, "%s: wrote the %s entries for %d units\n", file, lang, len(units))
	return nil
}

// replaceFile replaces file with data in one rename, keeping its mode, so a
// failure leaves the old file whole.
func replaceFile(file string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(file), ".aval-gen-"+filepath.Base(file))
	info, err := os.Stat(file)
	if err == nil {
		err = os.WriteFile(tmp, data, info.Mode()&fs.ModePerm)
	}
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
