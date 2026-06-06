// pkg_picker.go is the bubbletea model for selecting a target package before
// the rules view. It shows the device-detected debuggable package list and
// returns the chosen one (or a cancellation) on exit.
package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// PkgPickerModel is a single-select list over debuggable packages discovered
// on the device. Aliases (when supplied) are appended to each row's label
// for orientation; the selection result is always the literal package name.
type PkgPickerModel struct {
	pkgs      []string
	aliases   map[string][]string // pkg → its registered aliases (display only)
	cursor    int
	selected  string
	cancelled bool
	quitting  bool
}

// NewPkgPickerModel constructs a picker over the given package list. Callers
// should query adb.ListDebuggablePackages first; an empty list still renders
// (with a hint message) so the user gets a clear error rather than a blank
// session.
//
// aliasesByPkg is optional. When supplied, each entry's value is rendered as
// a "(name1, name2)" hint after the package name. Pass nil to render the
// raw package list with no annotations.
func NewPkgPickerModel(pkgs []string, aliasesByPkg map[string][]string) PkgPickerModel {
	return PkgPickerModel{
		pkgs:    append([]string(nil), pkgs...),
		aliases: aliasesByPkg,
	}
}

// Result returns the chosen pkg and whether a real selection was made. False
// means the user pressed q/esc/ctrl+c to cancel.
func (m PkgPickerModel) Result() (string, bool) {
	return m.selected, !m.cancelled && m.selected != ""
}

func (m PkgPickerModel) Init() tea.Cmd { return nil }

func (m PkgPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.cancelled = true
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if len(m.pkgs) > 0 {
				m.cursor = (m.cursor - 1 + len(m.pkgs)) % len(m.pkgs)
			}
		case "down", "j":
			if len(m.pkgs) > 0 {
				m.cursor = (m.cursor + 1) % len(m.pkgs)
			}
		case "home":
			m.cursor = 0
		case "end":
			if len(m.pkgs) > 0 {
				m.cursor = len(m.pkgs) - 1
			}
		case "enter":
			if len(m.pkgs) > 0 {
				m.selected = m.pkgs[m.cursor]
				m.quitting = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m PkgPickerModel) View() string {
	if m.quitting {
		return ""
	}
	header := titleStyle.Render("forja rules — select target package")
	help := dimStyle.Render("↑↓ select   enter confirm   q cancel")
	if len(m.pkgs) == 0 {
		empty := dimStyle.Render(
			"(no debuggable packages running on device — launch a debug-built app first)")
		return header + "\n\n" + empty + "\n\n" + help + "\n"
	}
	body := ""
	for i, pkg := range m.pkgs {
		line := "  " + pkg
		if alts := m.aliases[pkg]; len(alts) > 0 {
			line += dimStyle.Render(fmt.Sprintf("  (%s)", joinAliases(alts)))
		}
		if i == m.cursor {
			line = selectedStyle.Render(line)
		}
		body += line + "\n"
	}
	return header + "\n\n" + body + "\n" + help + "\n"
}

func joinAliases(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
