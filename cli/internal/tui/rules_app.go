// rules_app.go wraps the multi-stage `forja rules` flow — optional app
// picker → device status + rules load (async) → rules toggle view — into a
// single bubbletea program. Running each stage as its own tea.Program used
// to leave alt-screen mode between stages and the user saw a perceptible
// terminal flicker; sharing one program over the whole flow keeps the alt
// screen held end to end.
package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tkhskt/forja/internal/config"
)

// stage identifies which sub-model the wrapper is currently rendering. The
// transitions are linear: stagePickApp → stageLoadDeps → stagePickRules →
// stageDone. When the wrapper is constructed with a preset app, stagePickApp
// is skipped and execution starts in stageLoadDeps.
type stage int

const (
	stagePickApp stage = iota
	stageLoadDeps
	stagePickRules
	stageDone
)

// LoadDepsFunc resolves the per-app state the rules view needs: the merged
// effective rule slice (post-shadow filtering, with .Enabled reflecting
// status.json) and a DeviceStatus snapshot (live/stale/unknown). It runs in
// a goroutine via tea.Cmd so the bubbletea event loop keeps rendering the
// loading view while the device query and yml reload are in flight.
//
// Returning a non-nil error makes the wrapper quit with that error attached
// to its Result; callers handle the surface-level reporting.
type LoadDepsFunc func(ctx context.Context, app string) ([]config.EffectiveRule, DeviceStatus, error)

// loadDoneMsg is the result of the async load step. The wrapper's Update
// dispatches on this message to construct the RulesModel and advance to
// stagePickRules.
type loadDoneMsg struct {
	eff          []config.EffectiveRule
	deviceStatus DeviceStatus
	err          error
}

// RulesAppModel is the top-level model for `forja rules`. It composes the
// AppPickerModel and the RulesModel (rather than subclassing or duplicating
// their logic) so each sub-model stays independently testable and the
// wrapper only owns the transitions.
type RulesAppModel struct {
	stage stage

	// Picker stage state. pickerPresent is set when the wrapper was
	// constructed without a preset app — used in Result() to distinguish
	// "cancelled at the picker" from "no picker was ever shown".
	picker        AppPickerModel
	pickerPresent bool

	// Load stage state. loadingErr is set when LoadDepsFunc returned an
	// error; the wrapper quits and surfaces it via Result.
	loadDeps   LoadDepsFunc
	loadingErr error

	// Rules stage state. rulesReady distinguishes "we reached the rules
	// view at least once" from "we quit during loading" — only the former
	// has a meaningful toggle result to report back.
	rules      RulesModel
	rulesReady bool

	// Cross-stage data.
	app string
}

// NewRulesAppModel constructs the wrapper. If presetApp is non-empty the
// picker is skipped and the model starts in the loading stage. apps and
// aliasesByApp are forwarded to NewAppPickerModel when the picker is shown.
//
// loadDeps is invoked exactly once, after the app is chosen (or immediately
// when presetApp is set). It must be cheap to construct — the cwd-resolved
// paths and engine references should be closed over by the caller.
func NewRulesAppModel(presetApp string, apps []string, aliasesByApp map[string][]string, loadDeps LoadDepsFunc) RulesAppModel {
	m := RulesAppModel{
		loadDeps: loadDeps,
		app:      presetApp,
	}
	if presetApp != "" {
		m.stage = stageLoadDeps
		return m
	}
	m.picker = NewAppPickerModel(apps, aliasesByApp)
	m.pickerPresent = true
	m.stage = stagePickApp
	return m
}

func (m RulesAppModel) Init() tea.Cmd {
	if m.stage == stageLoadDeps {
		return m.startLoadCmd()
	}
	return m.picker.Init()
}

// startLoadCmd builds the tea.Cmd that runs LoadDepsFunc in a goroutine.
// The result lands as a loadDoneMsg in the next Update call.
func (m RulesAppModel) startLoadCmd() tea.Cmd {
	app := m.app
	fn := m.loadDeps
	return func() tea.Msg {
		eff, ds, err := fn(context.Background(), app)
		return loadDoneMsg{eff: eff, deviceStatus: ds, err: err}
	}
}

func (m RulesAppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.stage {
	case stagePickApp:
		modelI, cmd := m.picker.Update(msg)
		m.picker = modelI.(AppPickerModel)
		if !m.picker.quitting {
			return m, cmd
		}
		// Picker is signalling end-of-stage. Discard the tea.Quit it
		// emitted (it was meant to exit a standalone-picker program; here
		// the wrapper takes over routing).
		if m.picker.cancelled {
			m.stage = stageDone
			return m, tea.Quit
		}
		m.app = m.picker.selected
		m.stage = stageLoadDeps
		return m, m.startLoadCmd()
	case stageLoadDeps:
		if done, ok := msg.(loadDoneMsg); ok {
			if done.err != nil {
				m.loadingErr = done.err
				m.stage = stageDone
				return m, tea.Quit
			}
			m.rules = NewRulesModel(m.app, done.eff, done.deviceStatus)
			m.rulesReady = true
			m.stage = stagePickRules
			return m, m.rules.Init()
		}
		return m, nil
	case stagePickRules:
		modelI, cmd := m.rules.Update(msg)
		m.rules = modelI.(RulesModel)
		if m.rules.quitting {
			m.stage = stageDone
			return m, tea.Quit
		}
		return m, cmd
	}
	return m, nil
}

func (m RulesAppModel) View() string {
	switch m.stage {
	case stagePickApp:
		return m.picker.View()
	case stageLoadDeps:
		header := titleStyle.Render("forja rules — " + m.app)
		body := dimStyle.Render("loading device status…")
		return header + "\n\n" + body + "\n"
	case stagePickRules:
		return m.rules.View()
	}
	return ""
}

// Result is the wrapper's hand-off to the cmd layer after the program exits.
// The fields cover every termination path:
//
//   - cancelled: the user pressed q/esc at the picker stage.
//   - err: LoadDepsFunc returned an error.
//   - app: the chosen (or preset) app, empty when cancelled before picking.
//   - eff: the (possibly toggled) effective rules — non-nil only when the
//     rules view actually rendered.
//   - dirty: whether the rules view recorded any toggle change.
func (m RulesAppModel) Result() (app string, eff []config.EffectiveRule, dirty bool, cancelled bool, err error) {
	if m.loadingErr != nil {
		return m.app, nil, false, false, m.loadingErr
	}
	if m.pickerPresent && m.picker.cancelled {
		return "", nil, false, true, nil
	}
	if !m.rulesReady {
		return m.app, nil, false, false, nil
	}
	rules, dirtyFlag := m.rules.Result()
	return m.app, rules, dirtyFlag, false, nil
}
