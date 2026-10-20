package ui

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"vps-fleet-tui/internal/config"
	"vps-fleet-tui/internal/theme"
)

// Rows of the settings screen
const (
	setBars = iota
	setStart
	setEnd
	setKill
	setSort
	setCount
)

var barSchemeOrder = []string{"current", "greyscale", "custom"}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// settingsForm is the state of the settings screen.
type settingsForm struct {
	cursor int
	start  textinput.Model
	end    textinput.Model
	err    string // last save error
}

func newSettingsForm(s config.Settings) *settingsForm {
	newInput := func(v string) textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.CharLimit = 7
		ti.Width = 8
		ti.SetValue(v)
		return ti
	}
	return &settingsForm{start: newInput(s.CustomStart), end: newInput(s.CustomEnd)}
}

// WithSettings applies loaded settings to a new model.
func (m Model) WithSettings(s config.Settings) Model {
	m.settings = s
	m.applyGradient()
	if k, ok := parseSortKey(s.SortOrder); ok {
		m.sortBy = k
	}
	return m
}

// applyGradient sets the bar colours from the settings; invalid custom colours are ignored.
func (m *Model) applyGradient() {
	start, end := m.settings.Gradient()
	if hexColor.MatchString(start) && hexColor.MatchString(end) {
		theme.SetGradient(start, end)
	}
}

func parseSortKey(s string) (sortKey, bool) {
	for k := sortKey(0); k < sortCount; k++ {
		if k.String() == s {
			return k, true
		}
	}
	return 0, false
}

// defaultKillSignal returns the configured signal, falling back to TERM.
func (m Model) defaultKillSignal() string {
	if s, ok := normalizeSignal(m.settings.KillSignal); ok {
		return s
	}
	return "TERM"
}

// cycle returns the entry delta steps from current in options (wrapping).
func cycle(options []string, current string, delta int) string {
	i := slices.Index(options, current)
	if i < 0 {
		i = 0
	}
	return options[(i+delta+len(options))%len(options)]
}

func (m Model) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.settingsForm
	focusInputs := func() tea.Cmd {
		f.start.Blur()
		f.end.Blur()
		switch f.cursor {
		case setStart:
			f.start.CursorEnd()
			return f.start.Focus()
		case setEnd:
			f.end.CursorEnd()
			return f.end.Focus()
		}
		return nil
	}

	switch msg.String() {
	case "esc", "ctrl+c":
		m.state = stateFleet
		m.settingsForm = nil
		return m, nil
	case "up":
		f.cursor = (f.cursor + setCount - 1) % setCount
		return m, focusInputs()
	case "down", "tab", "enter":
		f.cursor = (f.cursor + 1) % setCount
		return m, focusInputs()
	case "left", "right":
		delta := 1
		if msg.String() == "left" {
			delta = -1
		}
		switch f.cursor {
		case setBars:
			m.settings.BarColors = cycle(barSchemeOrder, m.settings.BarColors, delta)
		case setKill:
			m.settings.KillSignal = cycle(killSignals, m.defaultKillSignal(), delta)
		case setSort:
			var names []string
			for k := sortKey(0); k < sortCount; k++ {
				names = append(names, k.String())
			}
			m.settings.SortOrder = cycle(names, m.settings.SortOrder, delta)
			if k, ok := parseSortKey(m.settings.SortOrder); ok {
				m.sortBy = k // takes effect right away, not only at the next start
			}
		default:
			return m.updateColorInput(msg) // move the cursor inside the colour field
		}
		m.saveSettings()
		return m, nil
	}

	if f.cursor == setStart || f.cursor == setEnd {
		return m.updateColorInput(msg)
	}
	return m, nil
}

// updateColorInput edits a custom colour; valid values are applied and saved at once.
func (m Model) updateColorInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.settingsForm
	in := &f.start
	if f.cursor == setEnd {
		in = &f.end
	}
	var cmd tea.Cmd
	*in, cmd = in.Update(msg)
	v := strings.TrimSpace(in.Value())
	if v != "" && !strings.HasPrefix(v, "#") {
		v = "#" + v // "5A56E0" is fine too
	}
	if hexColor.MatchString(v) {
		if f.cursor == setStart {
			m.settings.CustomStart = strings.ToUpper(v)
		} else {
			m.settings.CustomEnd = strings.ToUpper(v)
		}
		m.saveSettings()
	}
	return m, cmd
}

func (m *Model) saveSettings() {
	m.applyGradient()
	m.settingsForm.err = ""
	if err := config.SaveSettings(m.settings); err != nil {
		m.settingsForm.err = "Could not save: " + err.Error()
	}
}

func (m Model) renderSettings(width int) string {
	f := m.settingsForm
	title := lipgloss.NewStyle().Foreground(Cyan).Bold(true)
	dim := lipgloss.NewStyle().Foreground(DarkGray)
	lime := lipgloss.NewStyle().Foreground(Lime).Bold(true)
	labelStyle := lipgloss.NewStyle().Width(22)

	custom := m.settings.BarColors == "custom"
	choice := func(v string) string { return "◀ " + v + " ▶" }
	swatch := func(in textinput.Model) string {
		v := strings.TrimSpace(in.Value())
		if !strings.HasPrefix(v, "#") {
			v = "#" + v
		}
		if !hexColor.MatchString(v) {
			return ErrorStyle.Render("  not a colour, use #RRGGBB")
		}
		return "  " + lipgloss.NewStyle().Foreground(lipgloss.Color(v)).Render("████")
	}
	colorField := func(in textinput.Model) string {
		text := in.View()
		if !custom {
			text = dim.Render(in.Value()) + dim.Render("  (used with \"custom\")")
			return text
		}
		return text + swatch(in)
	}

	killValue := m.defaultKillSignal()
	if n, ok := signalNumbers[killValue]; ok {
		killValue = fmt.Sprintf("%s %d", killValue, n)
	}
	rows := []struct{ label, value, note string }{
		{"Bar colours", choice(m.settings.BarColors), strings.Join(barSchemeOrder, " · ")},
		{"Custom start colour", colorField(f.start), ""},
		{"Custom end colour", colorField(f.end), ""},
		{"Default kill signal", choice(killValue), "preselected in the kill dialog (k)"},
		{"Default sort order", choice(m.settings.SortOrder), "fleet list order at start (o changes it)"},
	}

	lines := []string{
		title.Render("Settings") + dim.Render("   saved to "+config.SettingsPath()),
		"",
	}
	for i, r := range rows {
		marker, label := "  ", r.label
		if i == f.cursor {
			marker, label = lime.Render("▌ "), lime.Render(r.label)
		}
		line := marker + labelStyle.Render(label) + r.value
		if r.note != "" {
			line += "   " + dim.Render(r.note)
		}
		lines = append(lines, line)
	}

	// Preview of the current colours
	barWidth := min(40, max(10, width-20))
	sample := make([]float64, barWidth)
	for i := range sample {
		sample[i] = 50 + 40*math.Sin(float64(i)/3) // a wave covering the full width
	}
	lines = append(lines, "",
		"  "+labelStyle.Render("Preview")+bar(75, barWidth)+"  75%",
		"  "+labelStyle.Render("")+sparkline(sample, barWidth))
	if f.err != "" {
		lines = append(lines, "", ErrorStyle.Render(f.err))
	}
	return strings.Join(lines, "\n")
}
