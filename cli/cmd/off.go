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
		Short: "Clear rules on an app: push [] to device AND empty its enabled list in status.json",
		Long: `Clear all rewrites on the given app's
enabled list in forja/status.json. The yml rule catalog is NOT modified — you
can re-enable individual rules via 'forja apply --app X --enable ...' or the
TUI when you want to re-engage on this app.

Only the named app is affected; other apps' status entries stay intact.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app == "" {
				return errors.New("--app is required")
			}
			resolvedApp, err := resolveApp(app)
			if err != nil {
				return err
			}
			app = resolvedApp
			eng, err := engine.New(globals.BundleDir)
			if err != nil {
				return err
			}
			if err := eng.Off(context.Background(), app); err != nil {
				return err
			}
			paths := rulesPaths()
			if err := rules.ClearApp(paths, app); err != nil {
				return fmt.Errorf("update status.json: %w", err)
			}
			fmt.Printf("[off] cleared rules on %s\n", app)
			return nil
		},
	}
	c.Flags().StringVar(&app, "app", "", "target Android applicationId or alias (required)")
	return c
}
