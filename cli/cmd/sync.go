package cmd

import (
	"errors"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/tkhskt/forja/internal/rules"
)

// newSyncCmd implements `forja sync` — re-push the current effective rule
// set to every app that already has a status.json entry (or just one
// when --app is given).
//
// The intended workflow is: hand-edit forja/rules.yml or rules.local.yml
// (tweak a body, change a status code, add a header), then run `forja sync`
// to propagate the change to the device(s) that already have the affected
// rules enabled. Without `sync`, hand edits only take effect on the next
// CLI command that happens to push (e.g. `rules update <name>` as a no-op).
//
// `sync` is strictly read-only on status.json. To flip which rules are
// enabled, use `forja apply` (or the TUI). To clear an app, use
// `forja off`.
func newSyncCmd() *cobra.Command {
	var app string
	c := &cobra.Command{
		Use:   "sync [--app APP]",
		Short: "Re-push the current effective rule set to every enabled app (or one)",
		Long: `forja sync re-reads forja/rules.yml + rules.local.yml + status.json and
pushes the resulting effective rule set to every app that already has a
status.json entry. Use this after hand-editing the yml to make the change
visible on the device.

Examples:

  forja sync                # sync every app with a status.json entry
  forja sync --app dev      # only the app "dev" (alias or full name)

sync NEVER writes status.json — it only reads. To flip which rules are
enabled use 'forja apply'; to clear an app use 'forja off'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			paths := rulesPaths()
			st, err := rules.LoadStatus(paths)
			if err != nil {
				return err
			}

			var targets []string
			if app != "" {
				resolved, err := resolveApp(app)
				if err != nil {
					return err
				}
				if _, exists := st[resolved]; !exists {
					return fmt.Errorf(
						"no status.json entry for %s — use `forja apply --app %s --enable …` first",
						resolved, resolved,
					)
				}
				targets = []string{resolved}
			} else {
				for k := range st {
					targets = append(targets, k)
				}
				if len(targets) == 0 {
					return errors.New("status.json has no entries — nothing to sync (use `forja apply` to seed one)")
				}
				sort.Strings(targets)
			}
			return pushToApps(targets, "sync")
		},
	}
	c.Flags().StringVar(&app, "app", "", "limit sync to this app (or alias)")
	return c
}
