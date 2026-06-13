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
		Short: "Manage applicationId aliases (project: .forja/aliases.yml, local: .forja/aliases.local.yml)",
		Long: `Define short aliases for frequently-used Android applicationId names. Any forja
command that takes --app accepts an alias in place of the full name:

  forja alias set dev com.tkhskt.forja.sample
  forja apply --app dev --enable teapot         # → com.tkhskt.forja.sample

Aliases come in two scopes, mirroring the rules files:

  .forja/aliases.yml         - project scope, committed, shared by the team
  .forja/aliases.local.yml   - local scope, personal (gitignore it)

set/rm default to the project scope; pass --local for the personal file. They
are merged when resolving --app, with local entries overriding project ones.
Unknown inputs to --app pass through unchanged, so literal applicationIds still
work.`,
	}
	c.AddCommand(newAliasSetCmd())
	c.AddCommand(newAliasRmCmd())
	c.AddCommand(newAliasListCmd())
	return c
}

// aliasScope maps the --local flag to a rules.Scope. Default is project
// (committed, shared), mirroring `rules add`.
func aliasScope(local bool) rules.Scope {
	if local {
		return rules.ScopeLocal
	}
	return rules.ScopeProject
}

func newAliasSetCmd() *cobra.Command {
	var local bool
	c := &cobra.Command{
		Use:   "set NAME APP",
		Short: "Map an alias to an applicationId (overwrites existing in the target scope)",
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
			scope := aliasScope(local)
			a, err := rules.LoadAliasesScope(paths, scope)
			if err != nil {
				return err
			}
			a[name] = app
			if err := rules.SaveAliasesScope(paths, scope, a); err != nil {
				return err
			}
			fmt.Printf("alias %q → %s (%s scope)\n", name, app, scope)
			return nil
		},
	}
	c.Flags().BoolVar(&local, "local", false,
		"write to the local (personal) alias file (.forja/aliases.local.yml). Default is project scope (.forja/aliases.yml).")
	return c
}

func newAliasRmCmd() *cobra.Command {
	var local bool
	c := &cobra.Command{
		Use:   "rm NAME",
		Short: "Delete an alias from the target scope",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			paths, err := rulesPaths()
			if err != nil {
				return err
			}
			scope := aliasScope(local)
			a, err := rules.LoadAliasesScope(paths, scope)
			if err != nil {
				return err
			}
			if _, ok := a[args[0]]; !ok {
				return fmt.Errorf("alias %q not found in %s scope (pass --local to target the personal file)", args[0], scope)
			}
			delete(a, args[0])
			if err := rules.SaveAliasesScope(paths, scope, a); err != nil {
				return err
			}
			fmt.Printf("removed alias %q (%s scope)\n", args[0], scope)
			return nil
		},
	}
	c.Flags().BoolVar(&local, "local", false,
		"remove from the local (personal) alias file (.forja/aliases.local.yml). Default is project scope (.forja/aliases.yml).")
	return c
}

func newAliasListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Print all registered aliases, grouped by scope",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			paths, err := rulesPaths()
			if err != nil {
				return err
			}
			project, err := rules.LoadAliasesScope(paths, rules.ScopeProject)
			if err != nil {
				return err
			}
			local, err := rules.LoadAliasesScope(paths, rules.ScopeLocal)
			if err != nil {
				return err
			}
			if len(project) == 0 && len(local) == 0 {
				fmt.Println("(no aliases set — use `forja alias set NAME APP`)")
				return nil
			}
			if len(project) > 0 {
				fmt.Println("project:")
				for _, k := range project.SortedKeys() {
					fmt.Printf("  %-20s → %s\n", k, project[k])
				}
			}
			if len(local) > 0 {
				fmt.Println("local:")
				for _, k := range local.SortedKeys() {
					line := fmt.Sprintf("  %-20s → %s", k, local[k])
					if _, shadows := project[k]; shadows {
						line += "   (overrides project)"
					}
					fmt.Println(line)
				}
			}
			return nil
		},
	}
}
