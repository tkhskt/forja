package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tkhskt/forja/internal/config"
)

// loadDepsStub returns a fake LoadDepsFunc. The wrapper calls this in a
// goroutine via tea.Cmd; in tests we drive the Cmd manually so we just
// build the corresponding loadDoneMsg ourselves below.
func loadDepsStub(eff []config.EffectiveRule, ds DeviceStatus, err error) LoadDepsFunc {
	return func(ctx context.Context, app string) ([]config.EffectiveRule, DeviceStatus, error) {
		return eff, ds, err
	}
}

// runCmd executes a returned tea.Cmd (which is just `func() tea.Msg`) and
// returns the resulting message. The wrapper uses tea.Cmd as the async-load
// boundary, so verifying its output is how we test the load step.
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// TestRulesAppModelPresetAppSkipsPicker: when constructed with a preset
// app, the wrapper starts in stageLoadDeps and Init returns the load Cmd.
func TestRulesAppModelPresetAppSkipsPicker(t *testing.T) {
	eff := []config.EffectiveRule{
		{Rule: config.Rule{Name: "r1"}, Scope: config.ScopeProject},
	}
	ds := DeviceStatus{Message: "live", Live: true}
	m := NewRulesAppModel("com.example.app", nil, nil, loadDepsStub(eff, ds, nil))

	if m.stage != stageLoadDeps {
		t.Errorf("preset app should start in stageLoadDeps, got %d", m.stage)
	}
	cmd := m.Init()
	msg := runCmd(cmd)
	if _, ok := msg.(loadDoneMsg); !ok {
		t.Fatalf("Init Cmd should produce loadDoneMsg, got %T", msg)
	}

	// Feed the message back. The wrapper transitions to stagePickRules.
	updatedI, _ := m.Update(msg)
	updated := updatedI.(RulesAppModel)
	if updated.stage != stagePickRules {
		t.Errorf("after loadDoneMsg the wrapper should be in stagePickRules, got %d", updated.stage)
	}
	if !updated.rulesReady {
		t.Errorf("rulesReady should be true after a successful load")
	}
}

// TestRulesAppModelPickerSelectionTransitionsToLoad: with no preset app,
// the wrapper starts in stagePickApp; once the picker emits an enter on a
// row, the wrapper advances to stageLoadDeps and triggers the load Cmd.
func TestRulesAppModelPickerSelectionTransitionsToLoad(t *testing.T) {
	apps := []string{"com.a", "com.b"}
	eff := []config.EffectiveRule{{Rule: config.Rule{Name: "x"}}}
	m := NewRulesAppModel("", apps, nil, loadDepsStub(eff, DeviceStatus{}, nil))
	if m.stage != stagePickApp {
		t.Fatalf("no preset app should start in stagePickApp, got %d", m.stage)
	}

	// Simulate pressing enter on the first row.
	updatedI, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := updatedI.(RulesAppModel)
	if updated.stage != stageLoadDeps {
		t.Fatalf("after picker enter the wrapper should be in stageLoadDeps, got %d", updated.stage)
	}
	if updated.app != "com.a" {
		t.Errorf("picker selection lost: %q", updated.app)
	}
	if msg := runCmd(cmd); msg == nil {
		t.Error("transition to stageLoadDeps must produce a load Cmd")
	}
}

// TestRulesAppModelPickerCancelExitsCleanly: cancelling the picker (q/esc)
// must surface as cancelled=true in Result(), and the wrapper must produce
// a tea.Quit on the transition so the program exits cleanly.
func TestRulesAppModelPickerCancelExitsCleanly(t *testing.T) {
	m := NewRulesAppModel("", []string{"com.a"}, nil, loadDepsStub(nil, DeviceStatus{}, nil))
	updatedI, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedI.(RulesAppModel)
	if updated.stage != stageDone {
		t.Errorf("cancelled picker should advance to stageDone, got %d", updated.stage)
	}
	if cmd == nil {
		t.Error("cancellation should produce tea.Quit Cmd")
	}
	_, _, _, cancelled, err := updated.Result()
	if !cancelled {
		t.Error("Result.cancelled must be true after picker cancel")
	}
	if err != nil {
		t.Errorf("cancellation should not be an error: %v", err)
	}
}

// TestRulesAppModelLoadErrorSurfacesViaResult: when LoadDepsFunc returns an
// error the wrapper quits and Result reports the error rather than crashing
// the program or rendering a half-loaded rules view.
func TestRulesAppModelLoadErrorSurfacesViaResult(t *testing.T) {
	wantErr := errors.New("load failed")
	m := NewRulesAppModel("com.example.app", nil, nil, loadDepsStub(nil, DeviceStatus{}, wantErr))
	msg := runCmd(m.Init())
	updatedI, cmd := m.Update(msg)
	updated := updatedI.(RulesAppModel)
	if updated.stage != stageDone {
		t.Errorf("load error should advance to stageDone, got %d", updated.stage)
	}
	if cmd == nil {
		t.Error("load error should produce tea.Quit Cmd")
	}
	_, _, _, _, gotErr := updated.Result()
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("Result.err should be the load error; got %v", gotErr)
	}
}

// TestRulesAppModelRulesQuitDirty: after the rules view returns with dirty=
// true the wrapper's Result must propagate the toggle changes and the
// dirty flag, so the cmd layer knows to persist + push.
func TestRulesAppModelRulesQuitDirty(t *testing.T) {
	eff := []config.EffectiveRule{
		{Rule: config.Rule{Name: "r1"}, Scope: config.ScopeProject},
	}
	m := NewRulesAppModel("com.example.app", nil, nil, loadDepsStub(eff, DeviceStatus{}, nil))
	// Drive through load.
	loadMsg := runCmd(m.Init())
	mI, _ := m.Update(loadMsg)
	m = mI.(RulesAppModel)
	if m.stage != stagePickRules {
		t.Fatalf("setup: should be in stagePickRules, got %d", m.stage)
	}
	// Toggle the only rule, then quit.
	mI, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = mI.(RulesAppModel)
	mI, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = mI.(RulesAppModel)
	if m.stage != stageDone {
		t.Errorf("after rules quit the wrapper should be in stageDone, got %d", m.stage)
	}
	if cmd == nil {
		t.Error("rules quit should produce tea.Quit Cmd")
	}
	app, out, dirty, cancelled, err := m.Result()
	if err != nil || cancelled {
		t.Errorf("clean quit: want err=nil cancelled=false, got err=%v cancelled=%v", err, cancelled)
	}
	if app != "com.example.app" {
		t.Errorf("app round-trip: %q", app)
	}
	if !dirty {
		t.Error("toggle should set dirty=true")
	}
	if len(out) != 1 || !out[0].Enabled {
		t.Errorf("rule should be enabled after toggle: %+v", out)
	}
}

// TestRulesAppModelViewSwitchesPerStage: each stage's View() must
// produce output recognizable as its stage so the user sees a coherent
// progression (picker → loading → rules).
func TestRulesAppModelViewSwitchesPerStage(t *testing.T) {
	m := NewRulesAppModel("", []string{"com.a"}, nil, loadDepsStub(nil, DeviceStatus{}, nil))
	if v := m.View(); v == "" {
		t.Error("picker View should not be empty in stagePickApp")
	}
	m.stage = stageLoadDeps
	m.app = "com.example.app"
	if v := m.View(); v == "" {
		t.Error("loading View should not be empty in stageLoadDeps")
	}
	eff := []config.EffectiveRule{{Rule: config.Rule{Name: "r1"}}}
	m.rules = NewRulesModel("com.example.app", eff, DeviceStatus{})
	m.stage = stagePickRules
	if v := m.View(); v == "" {
		t.Error("rules View should not be empty in stagePickRules")
	}
}
