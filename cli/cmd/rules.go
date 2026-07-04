package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
	description string
	host        string
	path        string
	status      int
	body        string
	bodyFile    string
	headers     []string
	local       bool // --local = target personal scope (.forja/rules.local.yml). default = project (.forja/rules.yml).
}

func bindRulesFlags(cmd *cobra.Command, f *rulesFlags) {
	cmd.Flags().StringVar(&f.description, "description", "",
		"authoring note describing the rule's intent (why it exists). Not pushed to the device; "+
			"prefer the intent ('simulate login outage') over restating status/path.")
	cmd.Flags().StringVar(&f.host, "host", "", "match: HTTP host exact match")
	cmd.Flags().StringVar(&f.path, "path", "", "match: URL encoded path (substring)")
	cmd.Flags().IntVar(&f.status, "status", 0, "rewrite: HTTP status code")
	cmd.Flags().StringVar(&f.body, "body", "",
		"rewrite: inline response body. If parseable as JSON object, sent as bodyObject; "+
			"otherwise as raw string. Pass an empty string ('') to force the response body to be empty.")
	cmd.Flags().StringVar(&f.bodyFile, "body-file", "",
		"rewrite: path to a file whose content becomes the response body. Path is "+
			"interpreted relative to the yml file's directory. *.json files are "+
			"parsed as JSON objects (bodyObject on device), other extensions are sent "+
			"as raw strings. Mutually exclusive with --body.")
	cmd.Flags().StringArrayVar(&f.headers, "header", nil,
		"rewrite: response header override as KEY=VALUE. Repeatable. The Content-Type entry "+
			"also drives the body's MIME type on the device (default application/json). "+
			"On update, passing --header replaces the entire header map; pass --header '' to clear.")
	cmd.Flags().BoolVar(&f.local, "local", false,
		"target the local (personal) rules file (.forja/rules.local.yml). Default is project scope (.forja/rules.yml) — the team-shared catalog.")
}

// rulesPaths resolves the Paths struct from the defaults. Paths are not
// individually overridable from the CLI — to operate on a different .forja/
// directory, run forja from a different cwd. It can error because the status
// file path is resolved against the user cache (home dir / cwd lookup).
func rulesPaths() (rules.Paths, error) {
	return rules.DefaultPaths()
}

// scopeFrom turns the --local flag into a rules.Scope. Default is project
// scope (the team-shared rules.yml) so the default workflow stays one file;
// --local opts into the personal override file (rules.local.yml).
func scopeFrom(f *rulesFlags) rules.Scope {
	if f.local {
		return rules.ScopeLocal
	}
	return rules.ScopeProject
}

// resolveApp expands an alias name to its full Android applicationId, returning
// the input unchanged when no alias matches (so literal applicationIds keep
// working). Callers should use this on any `--app` flag value before passing
// it to the engine layer. Empty input returns empty (= "no app specified").
func resolveApp(input string) (string, error) {
	if input == "" {
		return "", nil
	}
	paths, err := rulesPaths()
	if err != nil {
		return "", err
	}
	return rules.ResolveAlias(paths, input)
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
  forja rules --app com.example.app interactive: skip picker, edit toggles for that app
  forja rules add NAME ...          append a rule to the catalog (yml only)
  forja rules update NAME ...       patch an existing rule (auto-applied to enabled apps)
  forja rules remove NAME           delete a rule (auto-applied to enabled apps)
  forja rules list                  print the catalog (no device side effects)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			return runRulesTUI(app)
		},
	}
	c.Flags().StringVar(&app, "app", "", "skip the app picker and edit toggles for this app or alias directly")
	c.AddCommand(newRulesAddCmd())
	c.AddCommand(newRulesUpdateCmd())
	c.AddCommand(newRulesRemoveCmd())
	c.AddCommand(newRulesListCmd())
	return c
}

func newRulesListCmd() *cobra.Command {
	var app string
	c := &cobra.Command{
		Use:   "list",
		Short: "List rules in the catalog (yml only — does not touch any device)",
		Long: `List the merged rule catalog from .forja/rules.yml (project) and
.forja/rules.local.yml (local). Rules render in the same order the OkHttp
interceptor would scan them (local rules first, then project rules — the
on-device match precedence) and are labeled by their handle —
<bundle>/<name>, or just <name> for rules in the root rules.yml. The catalog
spans the root files plus any rules.yml / rules.local.yml in bundle
subdirectories under .forja/.

With --app, each rule line is prefixed with [on] / [off] to show whether
it's currently enabled for that app per .forja/status.json. Without --app,
only catalog data is shown.

  forja rules list
  forja rules list --app dev
  forja rules list --app com.example.app`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			paths, err := rulesPaths()
			if err != nil {
				return err
			}
			serial := ""
			if app != "" {
				resolved, err := resolveApp(app)
				if err != nil {
					return err
				}
				app = resolved
				// Per-rule enabled state is per-device, so we need a device to
				// report it against. One device (or --device) resolves cleanly;
				// several without --device is genuinely ambiguous.
				serial, err = resolveDevice("")
				if err != nil {
					return err
				}
			}
			eff, err := rules.LoadEffective(paths, serial, app)
			if err != nil {
				return err
			}
			return printRulesList(os.Stdout, eff, app)
		},
	}
	c.Flags().StringVar(&app, "app", "", "show per-rule enabled state for the given app or alias")
	return c
}

// printRulesList renders the effective rule list grouped by scope. When app
// is non-empty, each rule is prefixed with [on]/[off] indicating its status
// for that app. The output is purely informational — no device side effects.
func printRulesList(w io.Writer, eff []config.EffectiveRule, app string) error {
	if len(eff) == 0 {
		fmt.Fprintln(w, "(no rules — add some with `forja rules add NAME ...`)")
		return nil
	}

	showEnabled := app != ""
	byScope := map[string][]config.EffectiveRule{}
	for _, r := range eff {
		byScope[r.Scope] = append(byScope[r.Scope], r)
	}

	// Print local first since on-device match order is local-then-project.
	first := true
	for _, scope := range []string{config.ScopeLocal, config.ScopeProject} {
		rs := byScope[scope]
		if len(rs) == 0 {
			continue
		}
		if !first {
			fmt.Fprintln(w)
		}
		first = false
		fmt.Fprintf(w, "%s:\n", scope)
		for _, r := range rs {
			fmt.Fprintf(w, "  %s\n", formatRuleLine(r, showEnabled))
		}
	}

	if showEnabled {
		fmt.Fprintf(w, "\ntarget: %s\n", app)
	}
	return nil
}

// formatRuleLine builds a single-line summary of one rule. The match and
// response fields are joined with single spaces and only non-zero ones appear,
// so a rule that only sets a status renders as `name  status=418` rather than
// padding empty slots.
func formatRuleLine(r config.EffectiveRule, showEnabled bool) string {
	var sb strings.Builder
	if showEnabled {
		if r.Enabled {
			sb.WriteString("[on]  ")
		} else {
			sb.WriteString("[off] ")
		}
	} else {
		sb.WriteString("- ")
	}
	sb.WriteString(r.DisplayHandle())

	fields := []string{}
	if r.Description != "" {
		fields = append(fields, "desc="+strconv.Quote(r.Description))
	}
	if r.Match.Host != "" {
		fields = append(fields, "host="+r.Match.Host)
	}
	if r.Match.Path != "" {
		fields = append(fields, "path="+r.Match.Path)
	}
	if r.Response.Status != 0 {
		fields = append(fields, "status="+strconv.Itoa(r.Response.Status))
	}
	if r.Response.Body != nil {
		// Object form only ever appears in-memory (it gets serialized to a
		// JSON-encoded scalar on yml round-trip), but if a fresh in-process
		// rule is being inspected, show the JSON so the preview is still
		// useful instead of an opaque "object" label.
		if r.Response.Body.Object != nil {
			if b, err := json.Marshal(r.Response.Body.Object); err == nil {
				fields = append(fields, "body="+tui.FormatBodyPreview(string(b)))
			} else {
				fields = append(fields, "body=object")
			}
		} else {
			fields = append(fields, "body="+tui.FormatBodyPreview(r.Response.Body.String))
		}
	}
	if r.Response.BodyFile != "" {
		fields = append(fields, "bodyFile="+r.Response.BodyFile)
	}
	if n := len(r.Response.Headers); n > 0 {
		fields = append(fields, fmt.Sprintf("headers=%d", n))
	}

	if len(fields) > 0 {
		sb.WriteString("  ")
		sb.WriteString(strings.Join(fields, " "))
	}
	return sb.String()
}

func newRulesAddCmd() *cobra.Command {
	var f rulesFlags
	var dir string
	c := &cobra.Command{
		Use:   "add NAME",
		Short: "Append a rule to the catalog (yml only — does not touch any device)",
		Long: `Append a new rule to .forja/rules.yml (project scope, committed) by
default — pass --local to append to .forja/rules.local.yml (your personal
gitignored override file) instead.

The newly added rule is NOT applied to any app. To turn it on, run
'forja rules' (TUI) or 'forja apply --app X --enable NAME'.

  forja rules add teapot --host example.com --status 418
  forja apply --app com.tkhskt.forja.sample --enable teapot`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			if cmd.Flags().Changed("body") && cmd.Flags().Changed("body-file") {
				return errors.New("--body and --body-file are mutually exclusive")
			}
			var body *config.BodyValue
			if cmd.Flags().Changed("body") {
				b, err := parseBody(f.body)
				if err != nil {
					return err
				}
				body = b
			}
			headers, err := parseHeaders(f.headers)
			if err != nil {
				return err
			}
			opts := rules.AddOptions{
				Name:        args[0],
				Description: f.description,
				Host:        f.host,
				Path:        f.path,
				Status:      f.status,
				Body:        body,
				BodyFile:    f.bodyFile,
				Headers:     headers,
				Dir:         dir,
			}
			scope := scopeFrom(&f)
			paths, err := rulesPaths()
			if err != nil {
				return err
			}
			if err := rules.Add(paths, scope, opts); err != nil {
				return err
			}
			where := scope.String() + " scope"
			if dir != "" {
				where = filepath.ToSlash(filepath.Join(config.DefaultDir, dir))
			}
			fmt.Printf("added rule %q to %s\n", args[0], where)
			return nil
		},
	}
	bindRulesFlags(c, &f)
	c.Flags().StringVar(&dir, "dir", "", "write the rule into .forja/<dir>/rules.yml (a shareable bundle directory) instead of the root rules.yml")
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

Rules are addressed by handle: a bare name when it's unique, or <bundle>/<name>
when the same name lives in multiple bundles (update lists the candidates if a
bare name is ambiguous). --local is accepted for explicitness.

  forja rules update teapot --status 503    # patch + auto-push to every app where teapot is on
  forja rules update teapot --no-sync       # patch yml only`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			if cmd.Flags().Changed("body") && cmd.Flags().Changed("body-file") {
				return errors.New("--body and --body-file are mutually exclusive")
			}
			opts := rules.UpdateOptions{}
			if cmd.Flags().Changed("description") {
				description := f.description
				opts.Description = &description
			}
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
			if cmd.Flags().Changed("header") {
				headers, err := parseHeaders(f.headers)
				if err != nil {
					return err
				}
				opts.Headers = &headers
			}
			paths, err := rulesPaths()
			if err != nil {
				return err
			}
			var scopePtr *rules.Scope
			if f.local {
				s := rules.ScopeLocal
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
	var local bool
	var noSync bool
	c := &cobra.Command{
		Use:   "remove NAME",
		Short: "Delete a rule (auto-pushes the new set to every app where it was enabled)",
		Long: `Delete a rule from whichever scope it lives in. Rule names are unique
across both scopes, so the lookup is unambiguous; --local is accepted for
explicitness but isn't strictly required.

After the yml edit, forja iterates status.json and re-pushes the rule set
(now without the deleted rule) to every app where it was enabled, then
clears the rule name from every app's enabled list.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForjaDir(); err != nil {
				return err
			}
			paths, err := rulesPaths()
			if err != nil {
				return err
			}
			var scopePtr *rules.Scope
			if local {
				s := rules.ScopeLocal
				scopePtr = &s
			}
			// Snapshot the (device, app) pairs that had this rule enabled BEFORE
			// Remove drops the status entries, so we know who to re-push to.
			st, err := rules.LoadStatus(paths)
			if err != nil {
				return err
			}
			targets := st.TargetsEnabling(args[0])
			if err := rules.Remove(paths, args[0], scopePtr); err != nil {
				return err
			}
			fmt.Printf("removed rule %q\n", args[0])
			if noSync {
				return nil
			}
			return pushToTargets(targets, "remove")
		},
	}
	c.Flags().BoolVar(&local, "local", false,
		"force removal from the local file even when a project-scope rule of the same name exists")
	c.Flags().BoolVar(&noSync, "no-sync", false, "don't auto-push after deleting the yml entry")
	return c
}

// autoApplyToEnabledApps is the propagation engine used by update. It reads
// status.json, finds every (device, app) where the named rule is currently
// enabled, and pushes the (now updated) effective rule set to each of them —
// across every device that had it on.
func autoApplyToEnabledApps(name, opLabel string) error {
	paths, err := rulesPaths()
	if err != nil {
		return err
	}
	st, err := rules.LoadStatus(paths)
	if err != nil {
		return err
	}
	return pushToTargets(st.TargetsEnabling(name), opLabel)
}

// pushToApps pushes the current effective state to each app on a single
// device. Thin wrapper over pushToTargets for callers that already resolved a
// serial (sync, and the TUI/apply paths).
func pushToApps(serial string, apps []string, opLabel string) error {
	targets := make([]config.DeviceApp, 0, len(apps))
	for _, app := range apps {
		targets = append(targets, config.DeviceApp{Serial: serial, App: app})
	}
	return pushToTargets(targets, opLabel)
}

// pushToTargets pushes the current effective rule set to each (device, app)
// target, grouping by device so one engine is reused per serial. Targets whose
// app isn't running (or whose device is unreachable) are skipped with a
// warning so one dead target doesn't block the live ones.
func pushToTargets(targets []config.DeviceApp, opLabel string) error {
	if len(targets) == 0 {
		fmt.Printf("[%s] no enabled app on any device — yml change only\n", opLabel)
		return nil
	}
	paths, err := rulesPaths()
	if err != nil {
		return err
	}

	// Group apps by serial, preserving first-seen serial order.
	bySerial := map[string][]string{}
	serialOrder := []string{}
	for _, t := range targets {
		if _, seen := bySerial[t.Serial]; !seen {
			serialOrder = append(serialOrder, t.Serial)
		}
		bySerial[t.Serial] = append(bySerial[t.Serial], t.App)
	}

	pushed := []string{}
	skipped := []string{}
	for _, serial := range serialOrder {
		eng, err := engine.NewWithDevice(globals.BundleDir, serial)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] engine init failed for %s: %v\n", opLabel, serial, err)
			continue
		}
		for _, app := range bySerial[serial] {
			eff, err := rules.LoadEffective(paths, serial, app)
			if err != nil {
				return err
			}
			if err := eng.PushEffective(context.Background(), app, eff); err != nil {
				if errors.Is(err, engine.ErrAppNotRunning) {
					skipped = append(skipped, serial+"/"+app)
					continue
				}
				fmt.Fprintf(os.Stderr, "[%s] push to %s/%s failed: %v\n", opLabel, serial, app, err)
				continue
			}
			pushed = append(pushed, serial+"/"+app)
		}
	}
	report := fmt.Sprintf("[%s] pushed to %d target", opLabel, len(pushed))
	if len(pushed) > 0 {
		report += ": " + strings.Join(pushed, ", ")
	}
	if len(skipped) > 0 {
		report += fmt.Sprintf(" (skipped %d not running: %s)", len(skipped), strings.Join(skipped, ", "))
	}
	fmt.Println(report)
	return nil
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
//
// All three stages (optional picker → device load → rules toggles) run
// inside a single tea.Program so the alt screen stays held throughout. An
// earlier implementation ran the picker and the rules view as two separate
// programs, which produced a visible flicker each time the alt screen was
// torn down and re-entered between them.
func runRulesTUI(app string) error {
	paths, err := rulesPaths()
	if err != nil {
		return err
	}
	if app != "" {
		resolved, err := resolveApp(app)
		if err != nil {
			return err
		}
		app = resolved
	}

	// Resolve the device up front. With --device or exactly one usable device
	// we get a fixed serial and no device picker; with several and no --device
	// we present a device picker (and defer app enumeration until it's chosen).
	presetDevice := ""
	var deviceChoices []tui.DeviceChoice
	devs, err := adb.New().Devices(context.Background())
	if err != nil {
		return err
	}
	usable := make([]adb.Device, 0, len(devs))
	for _, d := range devs {
		if d.State == "device" {
			usable = append(usable, d)
		}
	}
	if globals.Device != "" {
		serial, err := resolveDevice("") // validates --device against the connected set
		if err != nil {
			return err
		}
		presetDevice = serial
	} else {
		switch len(usable) {
		case 0:
			return fmt.Errorf("no device connected%s", deviceListHint(devs))
		case 1:
			presetDevice = usable[0].Serial
		default:
			for _, d := range usable {
				deviceChoices = append(deviceChoices, tui.DeviceChoice{Serial: d.Serial, Label: deviceLabel(d)})
			}
		}
	}

	loadAliases := func(list []string) map[string][]string {
		out := map[string][]string{}
		if al, err := rules.LoadAliases(paths); err == nil {
			for _, p := range list {
				if alts := al.AliasesFor(p); len(alts) > 0 {
					out[p] = alts
				}
			}
		}
		return out
	}

	// For the fixed-device path, enumerate apps synchronously (unchanged
	// single-device behavior). For the device-picker path, apps are loaded via
	// loadApps after the device is chosen.
	var apps []string
	aliasesByApp := map[string][]string{}
	if presetDevice != "" && app == "" {
		list, err := adb.NewWithSerial(presetDevice).ListDebuggableApps(context.Background())
		if err != nil {
			return fmt.Errorf("list debuggable apps: %w", err)
		}
		apps = list
		aliasesByApp = loadAliases(list)
	}

	// loadApps enumerates the debuggable apps on a device chosen in the picker.
	loadApps := func(ctx context.Context, serial string) ([]string, map[string][]string, error) {
		list, err := adb.NewWithSerial(serial).ListDebuggableApps(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("list debuggable apps: %w", err)
		}
		return list, loadAliases(list), nil
	}

	// loadDeps runs in the wrapper's tea.Cmd after the (serial, app) pair is
	// known. It performs the device-side status query and reads back the
	// effective rule set for that device, returning both for the rules view.
	loadDeps := func(ctx context.Context, serial, picked string) ([]config.EffectiveRule, tui.DeviceStatus, error) {
		eff, err := rules.LoadEffective(paths, serial, picked)
		if err != nil {
			return nil, tui.DeviceStatus{}, err
		}
		deviceStatus := tui.DeviceStatus{Message: "device status unavailable"}
		if eng, err := engine.NewWithDevice(globals.BundleDir, serial); err == nil {
			s := eng.QueryAttachStatus(ctx, picked)
			// If the device has demonstrably lost the rules for this app, sync
			// this device's status to that reality so the checkboxes don't lie
			// before they're rendered. Scoped to serial, so it can't clobber
			// another device's state for the same app.
			if s.Kind == engine.StatusAgentLiveButOff || s.Kind == engine.StatusAgentStale {
				if err := rules.ClearApp(paths, serial, picked); err == nil {
					if e, lerr := rules.LoadEffective(paths, serial, picked); lerr == nil {
						eff = e
					}
				}
			}
			deviceStatus = tui.DeviceStatus{Message: s.Message(), Live: s.Live()}
		}
		return eff, deviceStatus, nil
	}

	model := tui.NewRulesAppModel(tui.RulesAppConfig{
		Device:       presetDevice,
		Devices:      deviceChoices,
		App:          app,
		Apps:         apps,
		AliasesByApp: aliasesByApp,
		LoadApps:     loadApps,
		LoadDeps:     loadDeps,
	})
	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	device, picked, updated, dirty, cancelled, loadErr := finalModel.(tui.RulesAppModel).Result()
	if loadErr != nil {
		return loadErr
	}
	if cancelled {
		return nil
	}
	app = picked
	if !dirty {
		return nil
	}

	// Materialize the per-app enabled list from the toggle result and persist
	// it against the chosen device.
	enabledNames := []string{}
	for _, r := range updated {
		if r.Enabled {
			enabledNames = append(enabledNames, r.Handle)
		}
	}
	if err := rules.SetEnabledForApp(paths, device, app, enabledNames); err != nil {
		return err
	}
	// Push the new effective state so the device matches the just-saved intent.
	eng, err := engine.NewWithDevice(globals.BundleDir, device)
	if err != nil {
		return err
	}
	if err := eng.PushEffective(context.Background(), app, updated); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] status saved but push failed: %v\n", err)
		return err
	}
	fmt.Printf("[toggled + synced] %s on %s: %d rule(s) enabled\n", app, device, len(enabledNames))
	return nil
}

// parseBody turns a CLI --body string into a BodyValue. JSON-object-looking
// strings (starting with `{`) become structured (= bodyObject on device);
// everything else is a plain string body. The caller is expected to have
// gated on cmd.Flags().Changed("body") — an empty string here is treated
// as the explicit "force empty body" case (returns &BodyValue{String: ""}),
// distinct from "body not provided" (nil).
func parseBody(s string) (*config.BodyValue, error) {
	if s == "" {
		return &config.BodyValue{String: ""}, nil
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

// parseHeaders parses a slice of `KEY=VALUE` flag values into a header map.
// A single empty entry (`--header ”`) is the documented way to express
// "clear all headers" on update; it returns an empty (non-nil) map.
//
// Validation:
//   - KEY must be non-empty and not contain whitespace, ':' or newline
//   - VALUE may be any string (including empty)
//   - duplicate KEY entries: last write wins (mirroring map semantics)
func parseHeaders(entries []string) (map[string]string, error) {
	out := map[string]string{}
	for _, e := range entries {
		if e == "" {
			// Single empty entry → "clear" sentinel. If mixed with non-empty
			// entries, that's almost certainly user confusion — reject.
			if len(entries) > 1 {
				return nil, errors.New("--header '': cannot be combined with other --header entries (use it alone to clear)")
			}
			return out, nil
		}
		idx := strings.Index(e, "=")
		if idx <= 0 {
			return nil, fmt.Errorf("--header %q: expected KEY=VALUE with non-empty KEY", e)
		}
		k := e[:idx]
		v := e[idx+1:]
		if err := validateHeaderName(k); err != nil {
			return nil, fmt.Errorf("--header %q: %w", e, err)
		}
		if err := validateHeaderValue(v); err != nil {
			return nil, fmt.Errorf("--header %q: %w", e, err)
		}
		out[k] = v
	}
	return out, nil
}

// validateHeaderName rejects header names that would break the wire format
// or the on-device JSON parser. HTTP RFCs allow a wider character set in
// header names but forja only needs the common ASCII subset.
func validateHeaderName(k string) error {
	if k == "" {
		return errors.New("header KEY cannot be empty")
	}
	for _, r := range k {
		if r <= 0x20 || r == 0x7F || r == ':' {
			return fmt.Errorf("header KEY contains invalid character %q (U+%04X)", r, r)
		}
	}
	return nil
}

// validateHeaderValue rejects header values that would either:
//   - allow HTTP response splitting via embedded CR/LF, or
//   - get rejected by OkHttp's Headers.checkValue on the device (which would
//     surface as a confusing runtime exception in logcat rather than an early
//     CLI error).
//
// We only reject the unambiguously-dangerous bytes (CR, LF, NUL) — anything
// else, including tab (HTAB) and the full UTF-8 range, is passed through so
// users can encode the same values OkHttp itself would accept.
func validateHeaderValue(v string) error {
	for _, r := range v {
		if r == '\r' || r == '\n' || r == 0 {
			return fmt.Errorf("header VALUE contains forbidden control character U+%04X", r)
		}
	}
	return nil
}
