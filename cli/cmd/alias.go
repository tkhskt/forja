package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tkhskt/forja/internal/rules"
)

func newAliasCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "alias",
		Short: "Manage personal applicationId aliases (.forja/aliases.local.yml — gitignore it locally)",
		Long: `Define short aliases for frequently-used Android applicationId names. Any forja
command that takes --app accepts an alias in place of the full name:

  forja alias set dev com.tkhskt.forja.sample
  forja apply --app dev --enable teapot         # → com.tkhskt.forja.sample

Aliases live in .forja/aliases.local.yml — per-developer file that you should
gitignore (forja never touches your .gitignore for you). Unknown inputs to
--app pass through unchanged, so literal applicationIds still work.`,
	}
	c.AddCommand(newAliasSetCmd())
	c.AddCommand(newAliasRmCmd())
	c.AddCommand(newAliasListCmd())
	return c
}

func newAliasSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set NAME APP",
		Short: "Map an alias to an applicationId (overwrites existing)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			name, app := args[0], args[1]
			if name == "" || app == "" {
				return errors.New("alias name and applicationId must be non-empty")
			}
			paths, err := rulesPaths()
			if err != nil {
				return err
			}
			a, err := rules.LoadAliases(paths)
			if err != nil {
				return err
			}
			a[name] = app
			if err := rules.SaveAliases(paths, a); err != nil {
				return err
			}
			fmt.Printf("alias %q → %s\n", name, app)
			return nil
		},
	}
}

func newAliasRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm NAME",
		Short: "Delete an alias",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			paths, err := rulesPaths()
			if err != nil {
				return err
			}
			a, err := rules.LoadAliases(paths)
			if err != nil {
				return err
			}
			if _, ok := a[args[0]]; !ok {
				return fmt.Errorf("alias %q not found", args[0])
			}
			delete(a, args[0])
			if err := rules.SaveAliases(paths, a); err != nil {
				return err
			}
			fmt.Printf("removed alias %q\n", args[0])
			return nil
		},
	}
}

func newAliasListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Print all registered aliases",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			paths, err := rulesPaths()
			if err != nil {
				return err
			}
			a, err := rules.LoadAliases(paths)
			if err != nil {
				return err
			}
			if len(a) == 0 {
				fmt.Println("(no aliases set — use `forja alias set NAME APP`)")
				return nil
			}
			for _, k := range a.SortedKeys() {
				fmt.Printf("  %-20s → %s\n", k, a[k])
			}
			return nil
		},
	}
}
