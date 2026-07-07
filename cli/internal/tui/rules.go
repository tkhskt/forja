// Package tui hosts the bubbletea models that drive forja's interactive
// surfaces.
//
// rules.go is the `forja rules` model: shows the merged effective rules
// (project + user), lets the user toggle enabled state, and on quit reports
// the new state so the caller can persist to status.json and push to device.
// The yml files themselves are never touched by the TUI.
package tui

import (
	"encoding/json"
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

	projectCount, localCount := 0, 0
	for _, r := range m.rules {
		if r.Scope == config.ScopeProject {
			projectCount++
		} else {
			localCount++
		}
	}
	header := titleStyle.Render(fmt.Sprintf(
		"forja rules — %s  (%d rules: %d project + %d local)",
		m.app, len(m.rules), projectCount, localCount,
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

	// Each rule renders as a small block so long names are never truncated and
	// everything stays legible: the checkbox + handle on the first line, then
	// (when set) the description, then the match → response rewrite. A section
	// header precedes the first row of each scope.
	const detailIndent = "      " // aligns under the handle: 2 (cursor) + 4 ("[x] ")
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

		cursor := "  "
		if i == m.cursor {
			cursor = "▸ "
		}
		mark := "[ ]"
		if r.Enabled {
			mark = "[x]"
		}
		selected := i == m.cursor
		stale := !m.device.Live && r.Enabled

		// Line 1: cursor + checkbox + full handle. Highlighted for the cursor
		// row, dimmed when the rule is enabled but not live on the device.
		nameLine := fmt.Sprintf("%s%s %s", cursor, mark, r.DisplayHandle())
		switch {
		case selected:
			nameLine = selectedStyle.Render(nameLine)
		case stale:
			nameLine = staleRuleStyle.Render(nameLine)
		}
		body += nameLine + "\n"

		// Line 2 (optional): the description, dimmed.
		if r.Description != "" {
			body += dimStyle.Render(detailIndent+r.Description) + "\n"
		}

		// Line 3: the rewrite — match on the left, response on the right.
		host := stringOrStar(r.Match.Host)
		path := stringOrStar(r.Match.Path)
		status := "-"
		if r.Response.Status != 0 {
			status = fmt.Sprintf("%d", r.Response.Status)
		}
		detail := fmt.Sprintf("host=%s  path=%s  → %s%s", host, path, status, responseExtras(r.Rule))
		detailStyle := dimStyle
		if stale {
			detailStyle = staleRuleStyle
		}
		body += detailStyle.Render(detailIndent+detail) + "\n"
	}
	return header + "\n" + statusLine + "\n" + body + "\n" + help + "\n"
}

// responseExtras renders the body / bodyFile / headers fragment appended
// to a rule row in the TUI. Each component is optional and skipped when
// not set on the rule, so a status-only rewrite stays compact:
//
//	[x] mock-failure  host=example.com  path=/foo  → 500  body='{"k":"v"}'
//	[x] empty-204     host=example.com  path=/foo  → 204  body=''
//	[x] big-response  host=example.com  path=/foo  → 200  bodyFile=responses/x.json
//	[x] html-mock     host=example.com  path=/foo  → 200  body='<h1>hi</h1>' headers=1
//
// `rules list` and the TUI use the same FormatBodyPreview underneath so
// the same truncation / escaping rules apply to both surfaces.
func responseExtras(r config.Rule) string {
	out := ""
	if r.Response.Body != nil {
		switch {
		case r.Response.Body.Object != nil:
			if b, err := json.Marshal(r.Response.Body.Object); err == nil {
				out += "  body=" + FormatBodyPreview(string(b))
			} else {
				out += "  body=object"
			}
		default:
			out += "  body=" + FormatBodyPreview(r.Response.Body.String)
		}
	}
	if r.Response.BodyFile != "" {
		out += "  bodyFile=" + r.Response.BodyFile
	}
	if n := len(r.Response.Headers); n > 0 {
		out += fmt.Sprintf("  headers=%d", n)
	}
	return out
}

func stringOrStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}
