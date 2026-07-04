// device_picker.go is the bubbletea model for selecting a target device before
// the package picker, shown only when several devices are connected. It returns
// the chosen device serial (or a cancellation) on exit. Kept independent of the
// adb package (it takes plain DeviceChoice values) so it stays trivially
// testable and the tui layer carries no device-discovery dependency.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// DeviceChoice is one selectable device: Serial is the value returned, Label is
// what's shown (e.g. "Pixel_7  (RZ8N70ABCDE)" or a bare "emulator-5554").
type DeviceChoice struct {
	Serial string
	Label  string
}

// DevicePickerModel is a single-select list over connected devices.
type DevicePickerModel struct {
	devices   []DeviceChoice
	cursor    int
	selected  string // chosen serial
	cancelled bool
	quitting  bool
}

// NewDevicePickerModel constructs a picker over the given devices.
func NewDevicePickerModel(devices []DeviceChoice) DevicePickerModel {
	return DevicePickerModel{devices: append([]DeviceChoice(nil), devices...)}
}

// Result returns the chosen serial and whether a real selection was made. False
// means the user pressed q/esc/ctrl+c to cancel.
func (m DevicePickerModel) Result() (string, bool) {
	return m.selected, !m.cancelled && m.selected != ""
}

func (m DevicePickerModel) Init() tea.Cmd { return nil }

func (m DevicePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.cancelled = true
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if len(m.devices) > 0 {
				m.cursor = (m.cursor - 1 + len(m.devices)) % len(m.devices)
			}
		case "down", "j":
			if len(m.devices) > 0 {
				m.cursor = (m.cursor + 1) % len(m.devices)
			}
		case "home":
			m.cursor = 0
		case "end":
			if len(m.devices) > 0 {
				m.cursor = len(m.devices) - 1
			}
		case "enter":
			if len(m.devices) > 0 {
				m.selected = m.devices[m.cursor].Serial
				m.quitting = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m DevicePickerModel) View() string {
	if m.quitting {
		return ""
	}
	header := titleStyle.Render("forja rules — select target device")
	help := dimStyle.Render("↑↓ select   enter confirm   q cancel")
	if len(m.devices) == 0 {
		empty := dimStyle.Render("(no devices connected)")
		return header + "\n\n" + empty + "\n\n" + help + "\n"
	}
	body := ""
	for i, d := range m.devices {
		line := "  " + d.Label
		if i == m.cursor {
			line = selectedStyle.Render(line)
		}
		body += line + "\n"
	}
	return header + "\n\n" + body + "\n" + help + "\n"
}
