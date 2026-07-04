// rules_app.go wraps the multi-stage `forja rules` flow — optional device
// picker → optional app picker → device status + rules load (async) → rules
// toggle view — into a single bubbletea program. Running each stage as its own
// tea.Program used to leave alt-screen mode between stages and the user saw a
// perceptible terminal flicker; sharing one program over the whole flow keeps
// the alt screen held end to end.
//
// The device picker is only shown when the caller couldn't resolve a single
// device up front (i.e. several are connected and no --device was given). When
// a device is preset, execution skips straight to the app picker (or the rules
// load, if an app was preset too), so the single-device experience is
// unchanged.
package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tkhskt/forja/internal/config"
)

// stage identifies which sub-model the wrapper is currently rendering.
// Transitions are linear, skipping any stage whose input was resolved up front:
//
//	stagePickDevice → stageLoadApps → stagePickApp → stageLoadDeps →
//	stagePickRules → stageDone
type stage int

const (
	stagePickDevice stage = iota
	stageLoadApps
	stagePickApp
	stageLoadDeps
	stagePickRules
	stageDone
)

// LoadAppsFunc enumerates the debuggable apps on a chosen device, plus their
// alias annotations. It runs in a goroutine via tea.Cmd (device app-listing is
// an adb round-trip) so the event loop keeps rendering while it's in flight.
type LoadAppsFunc func(ctx context.Context, serial string) (apps []string, aliasesByApp map[string][]string, err error)

// LoadDepsFunc resolves the per-app state the rules view needs: the merged
// effective rule slice (.Enabled reflecting this device's status) and a
// DeviceStatus snapshot. It runs in a goroutine via tea.Cmd. A non-nil error
// makes the wrapper quit with that error attached to its Result.
type LoadDepsFunc func(ctx context.Context, serial, app string) ([]config.EffectiveRule, DeviceStatus, error)

// appsLoadedMsg is the result of the async app-enumeration step.
type appsLoadedMsg struct {
	apps    []string
	aliases map[string][]string
	err     error
}

// loadDoneMsg is the result of the async per-app load step.
type loadDoneMsg struct {
	eff          []config.EffectiveRule
	deviceStatus DeviceStatus
	err          error
}

// RulesAppConfig is the wrapper's construction input. Resolving a field up
// front (Device, App) skips the corresponding picker stage.
type RulesAppConfig struct {
	// Device, when non-empty, is the already-resolved target serial and the
	// device picker is skipped. When empty, Devices is shown in a picker.
	Device  string
	Devices []DeviceChoice

	// App, when non-empty, is the already-resolved target app and the app
	// picker is skipped. Apps/AliasesByApp feed the app picker when Device was
	// preset (single-device path); in the device-picker path the app list is
	// loaded via LoadApps after the device is chosen.
	App          string
	Apps         []string
	AliasesByApp map[string][]string

	LoadApps LoadAppsFunc
	LoadDeps LoadDepsFunc
}

// RulesAppModel is the top-level model for `forja rules`. It composes the
// device picker, app picker, and rules sub-models (rather than subclassing
// their logic) so each stays independently testable and the wrapper only owns
// the transitions.
type RulesAppModel struct {
	stage stage

	// Device stage. devicePresent is set when the device picker is shown
	// (used in Result() to distinguish "cancelled at device pick" from "no
	// device picker was ever shown").
	devicePicker  DevicePickerModel
	devicePresent bool
	device        string // resolved serial (preset or picked)

	// App stage. pickerPresent is set when the app picker is shown.
	picker        AppPickerModel
	pickerPresent bool
	presetApp     string
	aliasesByApp  map[string][]string

	// Load stages.
	loadApps   LoadAppsFunc
	loadDeps   LoadDepsFunc
	loadingErr error

	// Rules stage. rulesReady distinguishes "we reached the rules view at
	// least once" from "we quit during loading".
	rules      RulesModel
	rulesReady bool

	// Cross-stage data.
	app string
}

// NewRulesAppModel constructs the wrapper from cfg. The starting stage depends
// on what cfg already resolved:
//
//   - Device == ""            → stagePickDevice
//   - Device set, App == ""   → stagePickApp
//   - Device set, App set     → stageLoadDeps
func NewRulesAppModel(cfg RulesAppConfig) RulesAppModel {
	m := RulesAppModel{
		device:       cfg.Device,
		presetApp:    cfg.App,
		app:          cfg.App,
		aliasesByApp: cfg.AliasesByApp,
		loadApps:     cfg.LoadApps,
		loadDeps:     cfg.LoadDeps,
	}
	if cfg.Device == "" {
		m.devicePicker = NewDevicePickerModel(cfg.Devices)
		m.devicePresent = true
		m.stage = stagePickDevice
		return m
	}
	// Device is preset (single device or --device). Skip straight to the app
	// picker, or to the load when an app was preset too.
	if cfg.App != "" {
		m.stage = stageLoadDeps
		return m
	}
	m.picker = NewAppPickerModel(cfg.Apps, cfg.AliasesByApp)
	m.pickerPresent = true
	m.stage = stagePickApp
	return m
}

func (m RulesAppModel) Init() tea.Cmd {
	switch m.stage {
	case stagePickDevice:
		return m.devicePicker.Init()
	case stageLoadDeps:
		return m.startLoadCmd()
	default:
		return m.picker.Init()
	}
}

// startLoadAppsCmd runs LoadAppsFunc for the chosen device in a goroutine.
func (m RulesAppModel) startLoadAppsCmd() tea.Cmd {
	serial := m.device
	fn := m.loadApps
	return func() tea.Msg {
		apps, aliases, err := fn(context.Background(), serial)
		return appsLoadedMsg{apps: apps, aliases: aliases, err: err}
	}
}

// startLoadCmd runs LoadDepsFunc for the chosen (device, app) in a goroutine.
func (m RulesAppModel) startLoadCmd() tea.Cmd {
	serial := m.device
	app := m.app
	fn := m.loadDeps
	return func() tea.Msg {
		eff, ds, err := fn(context.Background(), serial, app)
		return loadDoneMsg{eff: eff, deviceStatus: ds, err: err}
	}
}

func (m RulesAppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.stage {
	case stagePickDevice:
		modelI, cmd := m.devicePicker.Update(msg)
		m.devicePicker = modelI.(DevicePickerModel)
		if !m.devicePicker.quitting {
			return m, cmd
		}
		if m.devicePicker.cancelled {
			m.stage = stageDone
			return m, tea.Quit
		}
		m.device = m.devicePicker.selected
		// App may still be preset (--app with several devices): skip the app
		// picker and load directly; otherwise enumerate this device's apps.
		if m.presetApp != "" {
			m.stage = stageLoadDeps
			return m, m.startLoadCmd()
		}
		m.stage = stageLoadApps
		return m, m.startLoadAppsCmd()

	case stageLoadApps:
		if done, ok := msg.(appsLoadedMsg); ok {
			if done.err != nil {
				m.loadingErr = done.err
				m.stage = stageDone
				return m, tea.Quit
			}
			m.picker = NewAppPickerModel(done.apps, done.aliases)
			m.pickerPresent = true
			m.stage = stagePickApp
			return m, m.picker.Init()
		}
		return m, nil

	case stagePickApp:
		modelI, cmd := m.picker.Update(msg)
		m.picker = modelI.(AppPickerModel)
		if !m.picker.quitting {
			return m, cmd
		}
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
	case stagePickDevice:
		return m.devicePicker.View()
	case stageLoadApps:
		header := titleStyle.Render("forja rules — " + m.device)
		return header + "\n\n" + dimStyle.Render("loading apps…") + "\n"
	case stagePickApp:
		return m.picker.View()
	case stageLoadDeps:
		header := titleStyle.Render("forja rules — " + m.app)
		return header + "\n\n" + dimStyle.Render("loading device status…") + "\n"
	case stagePickRules:
		return m.rules.View()
	}
	return ""
}

// Result is the wrapper's hand-off to the cmd layer after the program exits.
//
//   - cancelled: the user pressed q/esc at the device or app picker.
//   - err: LoadApps/LoadDeps returned an error.
//   - device: the chosen (or preset) serial.
//   - app: the chosen (or preset) app, empty when cancelled before picking.
//   - eff: the (possibly toggled) effective rules — non-nil only when the
//     rules view actually rendered.
//   - dirty: whether the rules view recorded any toggle change.
func (m RulesAppModel) Result() (device, app string, eff []config.EffectiveRule, dirty bool, cancelled bool, err error) {
	if m.loadingErr != nil {
		return m.device, m.app, nil, false, false, m.loadingErr
	}
	if m.devicePresent && m.devicePicker.cancelled {
		return "", "", nil, false, true, nil
	}
	if m.pickerPresent && m.picker.cancelled {
		return m.device, "", nil, false, true, nil
	}
	if !m.rulesReady {
		return m.device, m.app, nil, false, false, nil
	}
	rules, dirtyFlag := m.rules.Result()
	return m.device, m.app, rules, dirtyFlag, false, nil
}
