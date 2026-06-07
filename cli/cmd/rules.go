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
	app      string
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

// rulesPaths resolves the Paths struct from the defaults. Paths are not
// individually overridable from the CLI — to operate on a different forja/
// directory, run forja from a different cwd.
func rulesPaths() rules.Paths {
	return rules.DefaultPaths()
}

// scopeFrom turns the --project flag into a rules.Scope (default = local).
func scopeFrom(f *rulesFlags) rules.Scope {
	if f.project {
		return rules.ScopeProject
	}
	return rules.ScopeLocal
}

// resolveApp expands an alias name to its full Android applicationId, returning
// the input unchanged when no alias matches (so literal applicationIds keep
// working). Callers should use this on any `--app` flag value before passing
// it to the engine layer. Empty input returns empty (= "no app specified").
func resolveApp(input string) (string, error) {
	if input == "" {
		return "", nil
	}
	return rules.ResolveAlias(rulesPaths(), input)
}

func newRulesCmd() *cobra.Command {
	var app string
	c := &cobra.Command{
		Use:   "rules",
		Short: "Manage forja rules (TUI by default; add / update / remove available as subcommands)",
		Long: `Without a subcommand, opens a TUI:

  1. lists debuggable apps currently on the device,
  2. lets you pick one,
  3. shows the rule catalog with that app's per-rule enabled state.

Use ↑↓/jk to navigate, space/enter to toggle enabled, q to push the new
state to the chosen app and exit.

  forja rules                       interactive: pick app + edit toggles
  forja rules --app com.x.y         interactive: skip picker, edit toggles for that app
  forja rules add NAME ...          append a rule to the catalog (yml only)
  forja rules update NAME ...       patch an existing rule (auto-applied to enabled apps)
  forja rules remove NAME           delete a rule (auto-applied to enabled apps)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRulesTUI(app)
		},
	}
	c.Flags().StringVar(&app, "app", "", "skip the app picker and edit toggles for this app or alias directly")
	c.AddCommand(newRulesAddCmd())
	c.AddCommand(newRulesUpdateCmd())
	c.AddCommand(newRulesRemoveCmd())
	return c
}

func newRulesAddCmd() *cobra.Command {
	var f rulesFlags
	c := &cobra.Command{
		Use:   "add NAME",
		Short: "Append a rule to the catalog (use --app to also enable+push to an app)",
		Long: `Append a new rule to forja/rules.local.yml (local scope; you should
gitignore this file) — or to forja/rules.yml (project scope, committed) when
--project is passed.

By default the new rule is NOT applied to any app. Use 'forja rules'
(TUI), 'forja apply', or pass --app X here to also enable it on X and push.

  forja rules add teapot --host example.com --status 418
      # yml only — pick targets later via TUI or 'forja apply'

  forja rules add teapot --host example.com --status 418 --app com.tkhskt.forja.sample
      # yml + enable on com.tkhskt.forja.sample + push to that app`,
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
			if f.app == "" {
				return nil
			}
			// Sugar path: enable on the named app and push.
			app, err := resolveApp(f.app)
			if err != nil {
				return err
			}
			if err := rules.Enable(paths, app, []string{args[0]}); err != nil {
				return err
			}
			return pushToApp(app, "add")
		},
	}
	bindRulesFlags(c, &f)
	c.Flags().StringVar(&f.app, "app", "",
		"also enable the new rule on this app (or alias) and push to the device")
	return c
}

func newRulesUpdateCmd() *cobra.Command {
	var f rulesFlags
	var noSync bool
	c := &cobra.Command{
		Use:   "update NAME",
		Short: "Patch an existing rule (auto-pushes to every app where it's enabled)",
		Long: `Patch the fields of an existing rule. Only fields you explicitly pass on the
command line are changed — others stay as they were.

After the yml edit, forja iterates status.json and re-pushes the rule set to
every app where this rule is currently enabled. Pass --no-sync to skip
the auto-push (yml is still updated).

By default the rule is searched across both scopes (local-wins on shadows).
Use --project to force the project file even when a local-scope shadow exists.

  forja rules update teapot --status 503    # patch + auto-push to every app where teapot is on
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
			return autoApplyToEnabledApps(args[0], "update")
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
		Short: "Delete a rule (auto-pushes the new set to every app where it was enabled)",
		Long: `Delete a rule. Searches across both scopes (local-wins on shadows). Use
--project to force removal from the project file when a local-scope shadow
exists.

After the yml edit, forja iterates status.json and re-pushes the rule set
(now without the deleted rule) to every app where it was enabled, then
clears the rule name from every app's enabled list.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := rulesPaths()
			var scopePtr *rules.Scope
			if project {
				s := rules.ScopeProject
				scopePtr = &s
			}
			// Snapshot the apps that had this rule enabled BEFORE Remove drops
			// the status entries, so we know who to re-push to.
			st, err := rules.LoadStatus(paths)
			if err != nil {
				return err
			}
			apps := st.AppsEnabling(args[0])
			if err := rules.Remove(paths, args[0], scopePtr); err != nil {
				return err
			}
			fmt.Printf("removed rule %q\n", args[0])
			if noSync {
				return nil
			}
			return pushToApps(apps, "remove")
		},
	}
	c.Flags().BoolVar(&project, "project", false,
		"force removal from the project file even when a local-scope shadow exists")
	c.Flags().BoolVar(&noSync, "no-sync", false, "don't auto-push after deleting the yml entry")
	return c
}

// autoApplyToEnabledApps is the propagation engine used by update. It reads
// status.json, finds every app where the named rule is currently enabled, and
// pushes the (now updated) effective rule set to each of them.
func autoApplyToEnabledApps(name, opLabel string) error {
	paths := rulesPaths()
	st, err := rules.LoadStatus(paths)
	if err != nil {
		return err
	}
	apps := st.AppsEnabling(name)
	return pushToApps(apps, opLabel)
}

// pushToApps pushes the current effective state to each app in turn. Apps
// whose app isn't running are skipped with a warning so an unrelated dead
// app doesn't block the live ones.
func pushToApps(apps []string, opLabel string) error {
	if len(apps) == 0 {
		fmt.Printf("[%s] no enabled app — yml change only\n", opLabel)
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
	for _, app := range apps {
		eff, err := rules.LoadEffective(paths, app)
		if err != nil {
			return err
		}
		if err := eng.PushEffective(context.Background(), app, eff); err != nil {
			if errors.Is(err, engine.ErrAppNotRunning) {
				skipped = append(skipped, app)
				continue
			}
			fmt.Fprintf(os.Stderr, "[%s] push to %s failed: %v\n", opLabel, app, err)
			continue
		}
		pushed = append(pushed, app)
	}
	report := fmt.Sprintf("[%s] pushed to %d app", opLabel, len(pushed))
	if len(pushed) > 0 {
		report += ": " + strings.Join(pushed, ", ")
	}
	if len(skipped) > 0 {
		report += fmt.Sprintf(" (skipped %d not running: %s)", len(skipped), strings.Join(skipped, ", "))
	}
	fmt.Println(report)
	return nil
}

// pushToApp is the single-app variant. Used by `rules add --app X`.
func pushToApp(app, opLabel string) error {
	return pushToApps([]string{app}, opLabel)
}

// runRulesTUI is the two-stage TUI: pick an app, then edit its toggles.
// If app is non-empty (from --app) the picker is skipped.
//
// Design contract:
//   - Opening the TUI has NO device side effects (no attach, no push). The
//     user may open it just to view the current configuration.
//   - The TUI checkboxes must reflect what's actually effective on the device,
//     not just the user's prior intent. If forja detects that the device has
//     lost the rules (off / PID change), status.json is updated to all-off for
//     the chosen app BEFORE display so the checkboxes are honest.
//   - Toggle changes are written to status.json only on quit, and only when
//     something was actually toggled. A view-only quit is truly a no-op.
//   - On dirty quit, push the new effective state to the device so things
//     match what the user just configured.
func runRulesTUI(app string) error {
	paths := rulesPaths()
	if app != "" {
		resolved, err := resolveApp(app)
		if err != nil {
			return err
		}
		app = resolved
	} else {
		picked, err := runAppPicker()
		if err != nil {
			return err
		}
		if picked == "" {
			return nil // user cancelled
		}
		app = picked
	}

	// Load effective rules for the chosen app. The caller may have just
	// arrived from --app without ever touching status.json, which is fine:
	// LoadEffective returns rules with .Enabled = status.IsEnabled(app, name),
	// and absent (= never touched) defaults to false. The TUI shows them as off.
	eff, err := rules.LoadEffective(paths, app)
	if err != nil {
		return err
	}

	deviceStatus := tui.DeviceStatus{Message: "device status unavailable"}
	if eng, err := engine.New(globals.BundleDir); err == nil {
		s := eng.QueryAttachStatus(context.Background(), app)
		// If the device has demonstrably lost the rules for this app, sync
		// status.json[app] to that reality so the checkboxes don't lie.
		if s.Kind == engine.StatusAgentLiveButOff || s.Kind == engine.StatusAgentStale {
			if err := rules.ClearApp(paths, app); err == nil {
				if e, lerr := rules.LoadEffective(paths, app); lerr == nil {
					eff = e
				}
			}
		}
		deviceStatus = tui.DeviceStatus{Message: s.Message(), Live: s.Live()}
	}

	model := tui.NewRulesModel(app, eff, deviceStatus)
	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	updated, dirty := finalModel.(tui.RulesModel).Result()
	if !dirty {
		return nil
	}

	// Materialize the per-app enabled list from the toggle result and persist.
	enabledNames := []string{}
	for _, r := range updated {
		if r.Enabled {
			enabledNames = append(enabledNames, r.Name)
		}
	}
	if err := rules.SetEnabledForApp(paths, app, enabledNames); err != nil {
		return err
	}
	// Push the new effective state so device matches the just-saved intent.
	eng, err := engine.New(globals.BundleDir)
	if err != nil {
		return err
	}
	if err := eng.PushEffective(context.Background(), app, updated); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] status saved but push failed: %v\n", err)
		return err
	}
	fmt.Printf("[toggled + synced] %s: %d rule(s) enabled\n", app, len(enabledNames))
	return nil
}

// runAppPicker queries the device for debuggable apps, runs the picker
// TUI, and returns the selected app name (empty string on cancel).
func runAppPicker() (string, error) {
	a := adb.New()
	apps, err := a.ListDebuggableApps(context.Background())
	if err != nil {
		return "", fmt.Errorf("list debuggable apps: %w", err)
	}
	// Annotate the picker with any aliases the user has registered. Failure
	// to load is non-fatal — the picker just renders without annotations.
	aliasesByApp := map[string][]string{}
	if a, err := rules.LoadAliases(rulesPaths()); err == nil {
		for _, app := range apps {
			if alts := a.AliasesFor(app); len(alts) > 0 {
				aliasesByApp[app] = alts
			}
		}
	}
	model := tui.NewAppPickerModel(apps, aliasesByApp)
	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("tui (app picker): %w", err)
	}
	sel, ok := finalModel.(tui.AppPickerModel).Result()
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
