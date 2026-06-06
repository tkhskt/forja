package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tkhskt/forja/internal/engine"
	"github.com/tkhskt/forja/internal/rules"
)

func newOffCmd() *cobra.Command {
	var pkg string
	c := &cobra.Command{
		Use:   "off --pkg PKG",
		Short: "Clear rules on a package: push [] to device AND empty its enabled list in status.json",
		Long: `Writes [] to /data/data/<pkg>/files/rules.json AND empties the package's
enabled list in forja/status.json. The yml rule catalog is NOT modified — you
can re-enable individual rules via 'forja apply --pkg X --enable ...' or the
TUI when you want to re-engage on this package.

Only the named package is affected; other packages' status entries stay intact.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if pkg == "" {
				return errors.New("--pkg is required")
			}
			resolvedPkg, err := resolvePkg(pkg)
			if err != nil {
				return err
			}
			pkg = resolvedPkg
			eng, err := engine.New(globals.BundleDir)
			if err != nil {
				return err
			}
			if err := eng.Off(context.Background(), pkg); err != nil {
				return err
			}
			paths := rulesPaths()
			if err := rules.ClearPkg(paths, pkg); err != nil {
				return fmt.Errorf("update status.json: %w", err)
			}
			fmt.Printf("[off] cleared rules on %s\n", pkg)
			return nil
		},
	}
	c.Flags().StringVar(&pkg, "pkg", "", "target Android package or alias (required)")
	return c
}
