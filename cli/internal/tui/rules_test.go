package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tkhskt/forja/internal/config"
)

func makeModel() RulesModel {
	rules := []config.EffectiveRule{
		{Rule: config.Rule{Name: "a", Enabled: true, Host: "x.com", Status: 500},
			Scope: config.ScopeProject},
		{Rule: config.Rule{Name: "b", Enabled: false, Host: "y.com", Status: 200},
			Scope: config.ScopeProject},
		{Rule: config.Rule{Name: "c", Enabled: true, Host: "z.com"},
			Scope: config.ScopeLocal},
	}
	return NewRulesModel("com.example.app", rules, DeviceStatus{})
}

func sendKey(t *testing.T, m RulesModel, key string) RulesModel {
	t.Helper()
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	switch key {
	case "down":
		newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	case "up":
		newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	case "home":
		newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	case "end":
		newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	case "enter":
		newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	case "ctrl+c":
		newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	}
	return newModel.(RulesModel)
}

func TestCursorMovement(t *testing.T) {
	m := makeModel()
	if m.cursor != 0 {
		t.Errorf("initial cursor should be 0, got %d", m.cursor)
	}
	m = sendKey(t, m, "down")
	if m.cursor != 1 {
		t.Errorf("down: want 1, got %d", m.cursor)
	}
	m = sendKey(t, m, "down")
	m = sendKey(t, m, "down")
	if m.cursor != 0 {
		t.Errorf("wrap-around: want 0, got %d", m.cursor)
	}
	m = sendKey(t, m, "up")
	if m.cursor != 2 {
		t.Errorf("up wrap-around: want 2, got %d", m.cursor)
	}
	m = sendKey(t, m, "home")
	if m.cursor != 0 {
		t.Errorf("home: want 0, got %d", m.cursor)
	}
	m = sendKey(t, m, "end")
	if m.cursor != 2 {
		t.Errorf("end: want 2, got %d", m.cursor)
	}
}

func TestSpaceTogglesAndSetsDirty(t *testing.T) {
	m := makeModel()
	if m.dirty {
		t.Error("model should start clean")
	}
	// Toggle rules[0] from enabled → disabled
	m = sendKey(t, m, " ")
	if !m.dirty {
		t.Error("toggle should mark dirty")
	}
	if m.rules[0].Enabled {
		t.Error("rules[0] should be disabled after toggle")
	}
	m = sendKey(t, m, "down")
	m = sendKey(t, m, " ")
	if !m.rules[1].Enabled {
		t.Error("rules[1] should be enabled after toggle")
	}
	updated, dirty := m.Result()
	if !dirty {
		t.Error("Result should report dirty")
	}
	if updated[0].Enabled || !updated[1].Enabled {
		t.Errorf("Result state inconsistent: %+v", updated)
	}
}

func TestEnterEquivalentToSpace(t *testing.T) {
	m := makeModel()
	m = sendKey(t, m, "enter")
	if !m.dirty {
		t.Error("enter should toggle")
	}
}

func TestQuitMarksQuitting(t *testing.T) {
	m := makeModel()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	rm := updated.(RulesModel)
	if !rm.quitting {
		t.Error("q should mark quitting")
	}
	if cmd == nil {
		t.Error("q should emit tea.Quit cmd")
	}
}

func TestCtrlCAlsoQuits(t *testing.T) {
	m := makeModel()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	rm := updated.(RulesModel)
	if !rm.quitting {
		t.Error("ctrl+c should mark quitting")
	}
	if cmd == nil {
		t.Error("ctrl+c should emit tea.Quit cmd")
	}
}

func TestViewShowsScopeSections(t *testing.T) {
	m := makeModel()
	view := m.View()
	for _, want := range []string{"project:", "local:", "a", "b", "c", "com.example.app"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\nfull view:\n%s", want, view)
		}
	}
}

func TestViewShowsDirtyMarker(t *testing.T) {
	m := makeModel()
	m = sendKey(t, m, " ")
	view := m.View()
	if !strings.Contains(view, "unsynced toggles") {
		t.Errorf("dirty marker missing\nview:\n%s", view)
	}
}

func TestViewWhenEmpty(t *testing.T) {
	m := NewRulesModel("com.foo.bar", nil, DeviceStatus{})
	view := m.View()
	if !strings.Contains(view, "no rules") {
		t.Errorf("empty view should hint to add: %s", view)
	}
}

func TestToggleNoopOnEmpty(t *testing.T) {
	m := NewRulesModel("com.foo.bar", nil, DeviceStatus{})
	m = sendKey(t, m, " ")
	if m.dirty {
		t.Error("empty rules cannot become dirty")
	}
}

func TestViewShowsDeviceStatusMessage(t *testing.T) {
	rules := []config.EffectiveRule{
		{Rule: config.Rule{Name: "a", Enabled: true, Host: "x.com", Status: 500},
			Scope: config.ScopeProject},
	}
	m := NewRulesModel("com.foo", rules, DeviceStatus{
		Message: "app restarted since last attach (was pid 100, now 200)",
		Live:    false,
	})
	view := m.View()
	if !strings.Contains(view, "app restarted") {
		t.Errorf("device status message missing from view\n%s", view)
	}
}

func TestViewLiveMarkerWhenAttached(t *testing.T) {
	rules := []config.EffectiveRule{
		{Rule: config.Rule{Name: "a", Enabled: true, Host: "x.com", Status: 500},
			Scope: config.ScopeLocal},
	}
	m := NewRulesModel("com.foo", rules, DeviceStatus{
		Message: "agent live (pid 595)",
		Live:    true,
	})
	view := m.View()
	if !strings.Contains(view, "agent live") {
		t.Errorf("live message missing from view: %s", view)
	}
}
