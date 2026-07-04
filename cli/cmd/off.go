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
	var app string
	c := &cobra.Command{
		Use:   "off --app APP",
		Short: "Turn off every rewrite on an app (other apps untouched)",
		Long: `Turn off every rewrite on the given app so it sees the original (real)
responses again. The yml rule catalog is NOT modified — you can re-enable
individual rules via 'forja apply --app X --enable ...' or the TUI when
you want to re-engage on this app.

Only the named app is affected; other apps' state stays intact.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			if app == "" {
				return errors.New("--app is required")
			}
			resolvedApp, err := resolveApp(app)
			if err != nil {
				return err
			}
			app = resolvedApp
			serial, err := resolveDevice("")
			if err != nil {
				return err
			}
			eng, err := engine.NewWithDevice(globals.BundleDir, serial)
			if err != nil {
				return err
			}
			if err := eng.Off(context.Background(), app); err != nil {
				return err
			}
			paths, err := rulesPaths()
			if err != nil {
				return err
			}
			if err := rules.ClearApp(paths, serial, app); err != nil {
				return fmt.Errorf("update status.json: %w", err)
			}
			fmt.Printf("[off] cleared rules on %s (%s)\n", app, serial)
			return nil
		},
	}
	c.Flags().StringVar(&app, "app", "", "target Android applicationId or alias (required)")
	return c
}
