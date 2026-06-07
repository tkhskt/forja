package cmd

import (
	"errors"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/tkhskt/forja/internal/rules"
)

// newSyncCmd implements `forja sync` — re-push the current effective rule
// set to every package that already has a status.json entry (or just one
// when --pkg is given).
//
// The intended workflow is: hand-edit forja/rules.yml or rules.local.yml
// (tweak a body, change a status code, add a header), then run `forja sync`
// to propagate the change to the device(s) that already have the affected
// rules enabled. Without `sync`, hand edits only take effect on the next
// CLI command that happens to push (e.g. `rules update <name>` as a no-op).
//
// `sync` is strictly read-only on status.json. To flip which rules are
// enabled, use `forja apply` (or the TUI). To clear a package, use
// `forja off`.
func newSyncCmd() *cobra.Command {
	var pkg string
	c := &cobra.Command{
		Use:   "sync [--pkg PKG]",
		Short: "Re-push the current effective rule set to every enabled package (or one)",
		Long: `forja sync re-reads forja/rules.yml + rules.local.yml + status.json and
pushes the resulting effective rule set to every package that already has a
status.json entry. Use this after hand-editing the yml to make the change
visible on the device.

Examples:

  forja sync                # sync every package with a status.json entry
  forja sync --pkg dev      # only the package "dev" (alias or full name)

sync NEVER writes status.json — it only reads. To flip which rules are
enabled use 'forja apply'; to clear a package use 'forja off'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := rulesPaths()
			st, err := rules.LoadStatus(paths)
			if err != nil {
				return err
			}

			var targets []string
			if pkg != "" {
				resolved, err := resolvePkg(pkg)
				if err != nil {
					return err
				}
				if _, exists := st[resolved]; !exists {
					return fmt.Errorf(
						"no status.json entry for %s — use `forja apply --pkg %s --enable …` first",
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
			return pushToPkgs(targets, "sync")
		},
	}
	c.Flags().StringVar(&pkg, "pkg", "", "limit sync to this package (or alias)")
	return c
}
