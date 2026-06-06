package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newKey(s string) tea.KeyMsg {
	// Simulate a key press by constructing the typed-letter case directly.
	// (bubbletea exposes KeyMsg as a plain struct alias for tea.Key.)
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestPkgPickerNavigatesAndSelects(t *testing.T) {
	m := NewPkgPickerModel([]string{"com.a", "com.b", "com.c"}, nil)
	if m.cursor != 0 {
		t.Errorf("initial cursor should be 0, got %d", m.cursor)
	}
	mm, _ := m.Update(newKey("down"))
	m = mm.(PkgPickerModel)
	mm, _ = m.Update(newKey("down"))
	m = mm.(PkgPickerModel)
	if m.cursor != 2 {
		t.Errorf("after 2x down cursor should be 2, got %d", m.cursor)
	}
	mm, _ = m.Update(newKey("enter"))
	m = mm.(PkgPickerModel)
	sel, ok := m.Result()
	if !ok || sel != "com.c" {
		t.Errorf("Result: ok=%v sel=%q", ok, sel)
	}
}

func TestPkgPickerCancelReturnsFalse(t *testing.T) {
	m := NewPkgPickerModel([]string{"com.a"}, nil)
	mm, _ := m.Update(newKey("q"))
	m = mm.(PkgPickerModel)
	_, ok := m.Result()
	if ok {
		t.Errorf("q should cancel — Result.ok should be false")
	}
}

func TestPkgPickerEscapeAlsoCancels(t *testing.T) {
	m := NewPkgPickerModel([]string{"com.a"}, nil)
	mm, _ := m.Update(newKey("esc"))
	m = mm.(PkgPickerModel)
	if _, ok := m.Result(); ok {
		t.Errorf("esc should cancel")
	}
}

func TestPkgPickerEmptyListRendersHint(t *testing.T) {
	m := NewPkgPickerModel(nil, nil)
	view := m.View()
	if !strings.Contains(view, "no debuggable packages") {
		t.Errorf("empty-list view should hint: %s", view)
	}
}

func TestPkgPickerWrapAround(t *testing.T) {
	m := NewPkgPickerModel([]string{"com.a", "com.b"}, nil)
	// Up from index 0 wraps to last.
	mm, _ := m.Update(newKey("up"))
	m = mm.(PkgPickerModel)
	if m.cursor != 1 {
		t.Errorf("up from 0 should wrap to last (1), got %d", m.cursor)
	}
	// Down from last wraps to 0.
	mm, _ = m.Update(newKey("down"))
	m = mm.(PkgPickerModel)
	if m.cursor != 0 {
		t.Errorf("down from last should wrap to 0, got %d", m.cursor)
	}
}
