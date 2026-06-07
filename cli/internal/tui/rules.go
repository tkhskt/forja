// Package tui hosts the bubbletea models that drive forja's interactive
// surfaces.
//
// rules.go is the `forja rules` model: shows the merged effective rules
// (project + user), lets the user toggle enabled state, and on quit reports
// the new state so the caller can persist to status.json and push to device.
// The yml files themselves are never touched by the TUI.
package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tkhskt/forja/internal/config"
)

// Styles. Kept in one place so the View can stay declarative.
var (
	titleStyle     = lipgloss.NewStyle().Bold(true)
	dimStyle       = lipgloss.NewStyle().Faint(true)
	selectedStyle  = lipgloss.NewStyle().Reverse(true)
	dirtyStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	warnStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3")) // yellow
	okStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2")) // green
	staleRuleStyle = lipgloss.NewStyle().Faint(true)                                // dim rules when not live
	scopeHeader    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4")) // blue
)

// DeviceStatus is the read-only snapshot of "what does forja think is happening
// on the device for this package". The cmd layer fills this in before opening
// the TUI; the TUI just renders it.
type DeviceStatus struct {
	Message string // one-line summary (e.g. "agent live (pid 595)")
	Live    bool   // true when rules below should be effective right now
}

// RulesModel is the bubbletea Model for `forja rules`. It owns a copy of the
// effective rule slice so toggle changes within the TUI don't bleed into the
// caller until .Result() is consulted.
type RulesModel struct {
	app      string
	rules    []config.EffectiveRule
	device   DeviceStatus
	cursor   int
	dirty    bool
	quitting bool
	width    int
}

// NewRulesModel constructs a model from a merged effective rule slice and a
// device status snapshot. app is shown in the header.
func NewRulesModel(app string, eff []config.EffectiveRule, device DeviceStatus) RulesModel {
	return RulesModel{app: app, rules: append([]config.EffectiveRule(nil), eff...), device: device}
}

// Result returns the (possibly mutated) effective rules and whether the user
// toggled anything during the session.
func (m RulesModel) Result() ([]config.EffectiveRule, bool) {
	return m.rules, m.dirty
}

func (m RulesModel) Init() tea.Cmd { return nil }

func (m RulesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if len(m.rules) > 0 {
				m.cursor = (m.cursor - 1 + len(m.rules)) % len(m.rules)
			}
		case "down", "j":
			if len(m.rules) > 0 {
				m.cursor = (m.cursor + 1) % len(m.rules)
			}
		case "home":
			m.cursor = 0
		case "end":
			if len(m.rules) > 0 {
				m.cursor = len(m.rules) - 1
			}
		case " ", "enter":
			if len(m.rules) > 0 {
				m.rules[m.cursor].Enabled = !m.rules[m.cursor].Enabled
				m.dirty = true
			}
		}
	}
	return m, nil
}

func (m RulesModel) View() string {
	if m.quitting {
		return ""
	}

	projectCount, userCount := 0, 0
	for _, r := range m.rules {
		if r.Scope == config.ScopeProject {
			projectCount++
		} else {
			userCount++
		}
	}
	header := titleStyle.Render(fmt.Sprintf(
		"forja rules — %s  (%d rules: %d project + %d user)",
		m.app, len(m.rules), projectCount, userCount,
	))
	if m.dirty {
		header += "  " + dirtyStyle.Render("(unsynced toggles)")
	}

	statusLine := ""
	if m.device.Message != "" {
		marker := "●"
		style := warnStyle
		if m.device.Live {
			style = okStyle
		}
		statusLine = style.Render(marker+" "+m.device.Message) + "\n"
	}

	help := dimStyle.Render("↑↓ select   space/enter toggle   q sync & exit")

	if len(m.rules) == 0 {
		empty := dimStyle.Render(
			"(no rules — `forja rules add NAME --host ... --status ...` to create one)")
		return header + "\n" + statusLine + "\n" + empty + "\n\n" + help + "\n"
	}

	// Render with a section header before the first row of each scope.
	body := ""
	lastScope := ""
	for i, r := range m.rules {
		if r.Scope != lastScope {
			if lastScope != "" {
				body += "\n"
			}
			body += scopeHeader.Render(r.Scope+":") + "\n"
			lastScope = r.Scope
		}
		mark := "[ ]"
		if r.Enabled {
			mark = "[x]"
		}
		host := stringOrStar(r.Match.Host)
		path := stringOrStar(r.Match.Path)
		status := "-"
		if r.Response.Status != 0 {
			status = fmt.Sprintf("%d", r.Response.Status)
		}
		line := fmt.Sprintf("  %s %-30s host=%-24s path=%-20s → %s",
			mark, truncate(r.Name, 30), truncate(host, 24), truncate(path, 20), status)
		switch {
		case i == m.cursor:
			line = selectedStyle.Render(line)
		case !m.device.Live && r.Enabled:
			line = staleRuleStyle.Render(line)
		}
		body += line + "\n"
	}
	return header + "\n" + statusLine + "\n" + body + "\n" + help + "\n"
}

func stringOrStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
