package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/tkhskt/forja/internal/adb"
	"github.com/tkhskt/forja/internal/config"
	"github.com/tkhskt/forja/internal/engine"
	"github.com/tkhskt/forja/internal/rules"
	"github.com/tkhskt/forja/internal/tui"
)

// rulesFlags is the merged set of CLI flags used by add / update / remove.
type rulesFlags struct {
	pkg      string
	host     string
	path     string
	status   int
	body     string
	bodyFile string
	project  bool // --project = target project scope (forja/rules.yml). default = local.
}

func bindRulesFlags(cmd *cobra.Command, f *rulesFlags) {
	cmd.Flags().StringVar(&f.host, "host", "", "match: HTTP host exact match")
	cmd.Flags().StringVar(&f.path, "path", "", "match: URL encoded path (substring)")
	cmd.Flags().IntVar(&f.status, "status", 0, "rewrite: HTTP status code")
	cmd.Flags().StringVar(&f.body, "body", "",
		"rewrite: inline response body. If parseable as JSON object, sent as bodyObject; otherwise as raw string.")
	cmd.Flags().StringVar(&f.bodyFile, "body-file", "",
		"rewrite: path to a file whose content becomes the response body. Path is "+
			"interpreted relative to the yml file's directory. *.json files are "+
			"parsed as JSON objects (bodyObject on device), other extensions are sent "+
			"as raw strings. Mutually exclusive with --body.")
	cmd.Flags().BoolVar(&f.project, "project", false,
		"target the project (shared) rules file (forja/rules.yml). Default is local scope (forja/rules.local.yml).")
}

// rulesPaths resolves the Paths struct from cobra globals. The global --rules
// flag overrides the project rules path; the user / status / aliases siblings
// stay at their defaults regardless (= forja/rules.local.yml etc.).
func rulesPaths() rules.Paths {
	p := rules.DefaultPaths()
	if globals.RulesPath != "" && globals.RulesPath != config.DefaultPath {
		p.Project = globals.RulesPath
	}
	return p
}

// scopeFrom turns the --project flag into a rules.Scope (default = local).
func scopeFrom(f *rulesFlags) rules.Scope {
	if f.project {
		return rules.ScopeProject
	}
	return rules.ScopeLocal
}

// resolvePkg expands an alias name to its full Android package, returning
// the input unchanged when no alias matches (so literal package names keep
// working). Callers should use this on any `--pkg` flag value before passing
// it to the engine layer. Empty input returns empty (= "no pkg specified").
func resolvePkg(input string) (string, error) {
	if input == "" {
		return "", nil
	}
	return rules.ResolveAlias(rulesPaths(), input)
}

func newRulesCmd() *cobra.Command {
	var pkg string
	c := &cobra.Command{
		Use:   "rules",
		Short: "Manage forja rules (TUI by default; add / update / remove available as subcommands)",
		Long: `Without a subcommand, opens a TUI:

  1. lists debuggable packages currently on the device,
  2. lets you pick one,
  3. shows the rule catalog with that package's per-rule enabled state.

Use ↑↓/jk to navigate, space/enter to toggle enabled, q to push the new
state to the chosen package and exit.

  forja rules                       interactive: pick package + edit toggles
  forja rules --pkg com.x.y         interactive: skip picker, edit toggles for that pkg
  forja rules add NAME ...          append a rule to the catalog (yml only)
  forja rules update NAME ...       patch an existing rule (auto-applied to enabled packages)
  forja rules remove NAME           delete a rule (auto-applied to enabled packages)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRulesTUI(pkg)
		},
	}
	c.Flags().StringVar(&pkg, "pkg", "", "skip the package picker and edit toggles for this pkg or alias directly")
	c.AddCommand(newRulesAddCmd())
	c.AddCommand(newRulesUpdateCmd())
	c.AddCommand(newRulesRemoveCmd())
	return c
}

func newRulesAddCmd() *cobra.Command {
	var f rulesFlags
	c := &cobra.Command{
		Use:   "add NAME",
		Short: "Append a rule to the catalog (use --pkg to also enable+push to a package)",
		Long: `Append a new rule to forja/rules.local.yml (local scope; you should
gitignore this file) — or to forja/rules.yml (project scope, committed) when
--project is passed.

By default the new rule is NOT applied to any package. Use 'forja rules'
(TUI), 'forja apply', or pass --pkg X here to also enable it on X and push.

  forja rules add teapot --host example.com --status 418
      # yml only — pick targets later via TUI or 'forja apply'

  forja rules add teapot --host example.com --status 418 --pkg com.tkhskt.forja.sample
      # yml + enable on com.tkhskt.forja.sample + push to that package`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("body") && cmd.Flags().Changed("body-file") {
				return errors.New("--body and --body-file are mutually exclusive")
			}
			body, err := parseBody(f.body)
			if err != nil {
				return err
			}
			opts := rules.AddOptions{
				Name:     args[0],
				Host:     f.host,
				Path:     f.path,
				Status:   f.status,
				Body:     body,
				BodyFile: f.bodyFile,
			}
			scope := scopeFrom(&f)
			paths := rulesPaths()
			if err := rules.Add(paths, scope, opts); err != nil {
				return err
			}
			fmt.Printf("added rule %q to %s scope\n", args[0], scope)
			if f.pkg == "" {
				return nil
			}
			// Sugar path: enable on the named pkg and push.
			pkg, err := resolvePkg(f.pkg)
			if err != nil {
				return err
			}
			if err := rules.Enable(paths, pkg, []string{args[0]}); err != nil {
				return err
			}
			return pushToPkg(pkg, "add")
		},
	}
	bindRulesFlags(c, &f)
	c.Flags().StringVar(&f.pkg, "pkg", "",
		"also enable the new rule on this package (or alias) and push to the device")
	return c
}

func newRulesUpdateCmd() *cobra.Command {
	var f rulesFlags
	var noSync bool
	c := &cobra.Command{
		Use:   "update NAME",
		Short: "Patch an existing rule (auto-pushes to every package where it's enabled)",
		Long: `Patch the fields of an existing rule. Only fields you explicitly pass on the
command line are changed — others stay as they were.

After the yml edit, forja iterates status.json and re-pushes the rule set to
every package where this rule is currently enabled. Pass --no-sync to skip
the auto-push (yml is still updated).

By default the rule is searched across both scopes (local-wins on shadows).
Use --project to force the project file even when a local-scope shadow exists.

  forja rules update teapot --status 503    # patch + auto-push to every pkg where teapot is on
  forja rules update teapot --no-sync       # patch yml only`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("body") && cmd.Flags().Changed("body-file") {
				return errors.New("--body and --body-file are mutually exclusive")
			}
			opts := rules.UpdateOptions{}
			if cmd.Flags().Changed("host") {
				host := f.host
				opts.Host = &host
			}
			if cmd.Flags().Changed("path") {
				path := f.path
				opts.Path = &path
			}
			if cmd.Flags().Changed("status") {
				status := f.status
				opts.Status = &status
			}
			if cmd.Flags().Changed("body") {
				body, err := parseBody(f.body)
				if err != nil {
					return err
				}
				opts.Body = body
			}
			if cmd.Flags().Changed("body-file") {
				bf := f.bodyFile
				opts.BodyFile = &bf
			}
			paths := rulesPaths()
			var scopePtr *rules.Scope
			if f.project {
				s := rules.ScopeProject
				scopePtr = &s
			}
			if err := rules.Update(paths, args[0], scopePtr, opts); err != nil {
				return err
			}
			fmt.Printf("updated rule %q\n", args[0])
			if noSync {
				return nil
			}
			return autoApplyToEnabledPkgs(args[0], "update")
		},
	}
	bindRulesFlags(c, &f)
	c.Flags().BoolVar(&noSync, "no-sync", false, "don't auto-push after patching yml")
	return c
}

func newRulesRemoveCmd() *cobra.Command {
	var project bool
	var noSync bool
	c := &cobra.Command{
		Use:   "remove NAME",
		Short: "Delete a rule (auto-pushes the new set to every package where it was enabled)",
		Long: `Delete a rule. Searches across both scopes (local-wins on shadows). Use
--project to force removal from the project file when a local-scope shadow
exists.

After the yml edit, forja iterates status.json and re-pushes the rule set
(now without the deleted rule) to every package where it was enabled, then
clears the rule name from every package's enabled list.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := rulesPaths()
			var scopePtr *rules.Scope
			if project {
				s := rules.ScopeProject
				scopePtr = &s
			}
			// Snapshot the pkgs that had this rule enabled BEFORE Remove drops
			// the status entries, so we know who to re-push to.
			st, err := rules.LoadStatus(paths)
			if err != nil {
				return err
			}
			pkgs := st.PkgsEnabling(args[0])
			if err := rules.Remove(paths, args[0], scopePtr); err != nil {
				return err
			}
			fmt.Printf("removed rule %q\n", args[0])
			if noSync {
				return nil
			}
			return pushToPkgs(pkgs, "remove")
		},
	}
	c.Flags().BoolVar(&project, "project", false,
		"force removal from the project file even when a local-scope shadow exists")
	c.Flags().BoolVar(&noSync, "no-sync", false, "don't auto-push after deleting the yml entry")
	return c
}

// autoApplyToEnabledPkgs is the propagation engine used by update. It reads
// status.json, finds every pkg where the named rule is currently enabled, and
// pushes the (now updated) effective rule set to each of them.
func autoApplyToEnabledPkgs(name, opLabel string) error {
	paths := rulesPaths()
	st, err := rules.LoadStatus(paths)
	if err != nil {
		return err
	}
	pkgs := st.PkgsEnabling(name)
	return pushToPkgs(pkgs, opLabel)
}

// pushToPkgs pushes the current effective state to each pkg in turn. Pkgs
// whose app isn't running are skipped with a warning so an unrelated dead
// app doesn't block the live ones.
func pushToPkgs(pkgs []string, opLabel string) error {
	if len(pkgs) == 0 {
		fmt.Printf("[%s] no enabled package — yml change only\n", opLabel)
		return nil
	}
	eng, err := engine.New(globals.BundleDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] engine init failed: %v\n", opLabel, err)
		return nil
	}
	paths := rulesPaths()
	pushed := []string{}
	skipped := []string{}
	for _, pkg := range pkgs {
		eff, err := rules.LoadEffective(paths, pkg)
		if err != nil {
			return err
		}
		if err := eng.PushEffective(context.Background(), pkg, eff); err != nil {
			if errors.Is(err, engine.ErrAppNotRunning) {
				skipped = append(skipped, pkg)
				continue
			}
			fmt.Fprintf(os.Stderr, "[%s] push to %s failed: %v\n", opLabel, pkg, err)
			continue
		}
		pushed = append(pushed, pkg)
	}
	report := fmt.Sprintf("[%s] pushed to %d pkg", opLabel, len(pushed))
	if len(pushed) > 0 {
		report += ": " + strings.Join(pushed, ", ")
	}
	if len(skipped) > 0 {
		report += fmt.Sprintf(" (skipped %d not running: %s)", len(skipped), strings.Join(skipped, ", "))
	}
	fmt.Println(report)
	return nil
}

// pushToPkg is the single-pkg variant. Used by `rules add --pkg X`.
func pushToPkg(pkg, opLabel string) error {
	return pushToPkgs([]string{pkg}, opLabel)
}

// runRulesTUI is the two-stage TUI: pick a package, then edit its toggles.
// If pkg is non-empty (from --pkg) the picker is skipped.
//
// Design contract:
//   - Opening the TUI has NO device side effects (no attach, no push). The
//     user may open it just to view the current configuration.
//   - The TUI checkboxes must reflect what's actually effective on the device,
//     not just the user's prior intent. If forja detects that the device has
//     lost the rules (off / PID change), status.json is updated to all-off for
//     the chosen pkg BEFORE display so the checkboxes are honest.
//   - Toggle changes are written to status.json only on quit, and only when
//     something was actually toggled. A view-only quit is truly a no-op.
//   - On dirty quit, push the new effective state to the device so things
//     match what the user just configured.
func runRulesTUI(pkg string) error {
	paths := rulesPaths()
	if pkg != "" {
		resolved, err := resolvePkg(pkg)
		if err != nil {
			return err
		}
		pkg = resolved
	} else {
		picked, err := runPkgPicker()
		if err != nil {
			return err
		}
		if picked == "" {
			return nil // user cancelled
		}
		pkg = picked
	}

	// Load effective rules for the chosen pkg. The caller may have just
	// arrived from --pkg without ever touching status.json, which is fine:
	// LoadEffective returns rules with .Enabled = status.IsEnabled(pkg, name),
	// and absent (= never touched) defaults to false. The TUI shows them as off.
	eff, err := rules.LoadEffective(paths, pkg)
	if err != nil {
		return err
	}

	deviceStatus := tui.DeviceStatus{Message: "device status unavailable"}
	if eng, err := engine.New(globals.BundleDir); err == nil {
		s := eng.QueryAttachStatus(context.Background(), pkg)
		// If the device has demonstrably lost the rules for this pkg, sync
		// status.json[pkg] to that reality so the checkboxes don't lie.
		if s.Kind == engine.StatusAgentLiveButOff || s.Kind == engine.StatusAgentStale {
			if err := rules.ClearPkg(paths, pkg); err == nil {
				if e, lerr := rules.LoadEffective(paths, pkg); lerr == nil {
					eff = e
				}
			}
		}
		deviceStatus = tui.DeviceStatus{Message: s.Message(), Live: s.Live()}
	}

	model := tui.NewRulesModel(pkg, eff, deviceStatus)
	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	updated, dirty := finalModel.(tui.RulesModel).Result()
	if !dirty {
		return nil
	}

	// Materialize the per-pkg enabled list from the toggle result and persist.
	enabledNames := []string{}
	for _, r := range updated {
		if r.Enabled {
			enabledNames = append(enabledNames, r.Name)
		}
	}
	if err := rules.SetEnabledForPkg(paths, pkg, enabledNames); err != nil {
		return err
	}
	// Push the new effective state so device matches the just-saved intent.
	eng, err := engine.New(globals.BundleDir)
	if err != nil {
		return err
	}
	if err := eng.PushEffective(context.Background(), pkg, updated); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] status saved but push failed: %v\n", err)
		return err
	}
	fmt.Printf("[toggled + synced] %s: %d rule(s) enabled\n", pkg, len(enabledNames))
	return nil
}

// runPkgPicker queries the device for debuggable packages, runs the picker
// TUI, and returns the selected pkg name (empty string on cancel).
func runPkgPicker() (string, error) {
	a := adb.New()
	pkgs, err := a.ListDebuggablePackages(context.Background())
	if err != nil {
		return "", fmt.Errorf("list debuggable packages: %w", err)
	}
	// Annotate the picker with any aliases the user has registered. Failure
	// to load is non-fatal — the picker just renders without annotations.
	aliasesByPkg := map[string][]string{}
	if a, err := rules.LoadAliases(rulesPaths()); err == nil {
		for _, pkg := range pkgs {
			if alts := a.AliasesFor(pkg); len(alts) > 0 {
				aliasesByPkg[pkg] = alts
			}
		}
	}
	model := tui.NewPkgPickerModel(pkgs, aliasesByPkg)
	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("tui (pkg picker): %w", err)
	}
	sel, ok := finalModel.(tui.PkgPickerModel).Result()
	if !ok {
		return "", nil
	}
	return sel, nil
}

// parseBody turns a CLI --body string into a BodyValue. JSON-object-looking
// strings (starting with `{`) become structured (= bodyObject on device);
// everything else is a plain string body.
func parseBody(s string) (*config.BodyValue, error) {
	if s == "" {
		return nil, nil
	}
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
			return nil, fmt.Errorf("--body looks like JSON object but failed to parse: %w", err)
		}
		return &config.BodyValue{Object: m}, nil
	}
	return &config.BodyValue{String: s}, nil
}
