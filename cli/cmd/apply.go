package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tkhskt/forja/internal/engine"
	"github.com/tkhskt/forja/internal/rules"
)

// newApplyCmd is the non-interactive equivalent of opening `forja rules`,
// toggling something, and quitting: it patches status.json[pkg].enabled in
// one or both directions and pushes the resulting effective state to the
// device.
//
// At least one of --enable / --disable must be passed — there is intentionally
// no "re-push current state" mode. The mental model is "status.json mirrors
// device state", so a no-op apply would have nothing to do.
func newApplyCmd() *cobra.Command {
	var (
		pkg     string
		enable  []string
		disable []string
	)
	c := &cobra.Command{
		Use:   "apply --pkg PKG [--enable a,b] [--disable c,d]",
		Short: "Enable/disable rules on a package and push to the device",
		Long: `Patch the per-package enabled state in forja/status.json and push the new
effective rule set to the device.

  forja apply --pkg com.x --enable teapot,dev-mock
  forja apply --pkg com.x --disable teapot
  forja apply --pkg com.x --enable teapot --disable dev-mock

At least one of --enable / --disable is required; pass --enable to add rule
names to the package's enabled list, --disable to remove them.

Unknown rule names in --enable cause an error (typo guard). Unknown names in
--disable are silently no-op'd (so you can safely scrub stale entries).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if pkg == "" {
				return errors.New("--pkg is required")
			}
			if len(enable) == 0 && len(disable) == 0 {
				return errors.New("at least one of --enable / --disable is required")
			}
			resolvedPkg, err := resolvePkg(pkg)
			if err != nil {
				return err
			}
			pkg = resolvedPkg
			paths := rulesPaths()
			if len(enable) > 0 {
				if err := rules.Enable(paths, pkg, enable); err != nil {
					return err
				}
			}
			if len(disable) > 0 {
				if err := rules.Disable(paths, pkg, disable); err != nil {
					return err
				}
			}
			eng, err := engine.New(globals.BundleDir)
			if err != nil {
				return err
			}
			eff, err := rules.LoadEffective(paths, pkg)
			if err != nil {
				return err
			}
			if err := eng.PushEffective(context.Background(), pkg, eff); err != nil {
				return err
			}
			enabledCount := 0
			for _, r := range eff {
				if r.Enabled {
					enabledCount++
				}
			}
			fmt.Printf("[apply] %s: %d rule(s) enabled, pushed to device\n", pkg, enabledCount)
			return nil
		},
	}
	c.Flags().StringVar(&pkg, "pkg", "", "target Android package or alias (required)")
	c.Flags().StringSliceVar(&enable, "enable", nil, "rule names to enable on the package (comma- or repeat-flag)")
	c.Flags().StringSliceVar(&disable, "disable", nil, "rule names to disable on the package (comma- or repeat-flag)")
	return c
}
