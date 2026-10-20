package ui

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"vps-fleet-tui/internal/vps"
)

// shellNoise matches the "sh: line 1: kill: " prefix of shell error messages.
var shellNoise = regexp.MustCompile(`^(\S+: )?(line \d+: )?kill: `)

// Signals offered in the kill dialog, most common first (TERM is the default).
var killSignals = []string{"TERM", "HUP", "INT", "QUIT", "KILL", "USR1", "USR2", "STOP", "CONT"}

// signalNumbers are the Linux numbers (x86 and ARM) of killSignals.
var signalNumbers = map[string]int{
	"HUP": 1, "INT": 2, "QUIT": 3, "KILL": 9, "USR1": 10, "USR2": 12, "TERM": 15, "CONT": 18, "STOP": 19,
}

// signalMatches reports whether typed (a name like "sigkill" or a number like "9") means name.
func signalMatches(typed, name string) bool {
	s, ok := normalizeSignal(typed)
	if !ok {
		return false
	}
	if n, err := strconv.Atoi(s); err == nil {
		return signalNumbers[name] == n
	}
	return s == name
}

// killDialog asks for a PID and a signal to send to it.
type killDialog struct {
	serverID string
	pid      textinput.Model
	signal   textinput.Model
	onSignal bool // focus is on the signal field
	sigIndex int  // position in killSignals when picked with ←/→
	err      string
}

type KillMsg struct {
	VPSID   string
	PID     int
	Signal  string
	Command string
	Err     error
}

func newKillDialog(serverID string, defaultPID int, defaultSignal string) *killDialog {
	pid := textinput.New()
	pid.Prompt = ""
	pid.CharLimit = 9
	if defaultPID > 0 {
		pid.Placeholder = strconv.Itoa(defaultPID)
	}
	pid.Focus()
	sig := textinput.New()
	sig.Prompt = ""
	sig.CharLimit = 12
	sig.SetValue(defaultSignal)
	// start ←/→ from the default's position in the list
	return &killDialog{serverID: serverID, pid: pid, signal: sig, sigIndex: max(0, slices.Index(killSignals, defaultSignal))}
}

// openKill opens the dialog; the PID field suggests the busiest process.
func (m *Model) openKill() tea.Cmd {
	v := m.selected()
	if v == nil {
		return nil
	}
	defaultPID := 0
	if ps := m.procs[v.ID]; ps != nil && ps.list != nil && len(ps.list.Procs) > 0 {
		defaultPID = ps.list.Procs[0].PID
	}
	m.kill = newKillDialog(v.ID, defaultPID, m.defaultKillSignal())
	return textinput.Blink
}

// processByPID looks up a PID in the server's current process list.
func (m Model) processByPID(serverID string, pid int) (vps.Process, bool) {
	if ps := m.procs[serverID]; ps != nil && ps.list != nil {
		for _, p := range ps.list.Procs {
			if p.PID == pid {
				return p, true
			}
		}
	}
	return vps.Process{}, false
}

// normalizeSignal accepts "kill", "SIGKILL" or "9" and returns "KILL" / "9".
func normalizeSignal(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "SIG")
	if s == "" {
		return "", false
	}
	if n, err := strconv.Atoi(s); err == nil {
		return s, n >= 0 && n <= 64
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '+' && r != '-' {
			return "", false // RTMIN+1 is valid; anything else odd is rejected
		}
	}
	return s, true
}

func (m Model) updateKill(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := m.kill
	d.err = ""
	switch msg.String() {
	case "esc":
		m.kill = nil
		return m, nil
	case "tab", "shift+tab", "up", "down":
		d.onSignal = !d.onSignal
		if d.onSignal {
			d.pid.Blur()
			d.signal.Focus()
		} else {
			d.signal.Blur()
			d.pid.Focus()
		}
		return m, textinput.Blink
	case "left", "right":
		if d.onSignal {
			delta := 1
			if msg.String() == "left" {
				delta = len(killSignals) - 1
			}
			d.sigIndex = (d.sigIndex + delta) % len(killSignals)
			d.signal.SetValue(killSignals[d.sigIndex])
			d.signal.CursorEnd()
			return m, nil
		}
	case "enter":
		return m.sendKill()
	}

	var cmd tea.Cmd
	if d.onSignal {
		d.signal, cmd = d.signal.Update(msg)
	} else {
		// PID: digits only
		if msg.Type == tea.KeyRunes && strings.Trim(string(msg.Runes), "0123456789") != "" {
			return m, nil
		}
		d.pid, cmd = d.pid.Update(msg)
	}
	return m, cmd
}

func (m Model) sendKill() (tea.Model, tea.Cmd) {
	d := m.kill
	pidText := d.pid.Value()
	if pidText == "" {
		pidText = d.pid.Placeholder // enter on an empty field takes the suggestion
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 1 {
		d.err = "enter the PID of a process (not 0 or 1)"
		return m, nil
	}
	sig, ok := normalizeSignal(d.signal.Value())
	if !ok {
		d.err = "unknown signal, use a name like TERM or KILL, or a number"
		return m, nil
	}
	client := m.clients[d.serverID]
	if client == nil {
		d.err = "not connected"
		return m, nil
	}
	command := ""
	if p, ok := m.processByPID(d.serverID, pid); ok {
		command = p.Command
	}
	m.kill = nil
	id := d.serverID
	return m, func() tea.Msg {
		out, err := client.Run(fmt.Sprintf("kill -s %s %d", sig, pid))
		if err != nil {
			// "sh: line 1: kill: (123) - No such process" -> "(123) - No such process"
			if msg := shellNoise.ReplaceAllString(strings.TrimSpace(out), ""); msg != "" {
				err = fmt.Errorf("%s", msg)
			}
		}
		return KillMsg{VPSID: id, PID: pid, Signal: sig, Command: command, Err: err}
	}
}

// renderKill draws the dialog box, width wide.
func (m Model) renderKill(width int) string {
	d := m.kill
	label := lipgloss.NewStyle().Foreground(Cyan).Bold(true).Width(9)
	dim := lipgloss.NewStyle().Foreground(DarkGray)
	inner := max(20, width-4)

	pidLine := label.Render("PID:") + d.pid.View()
	pidText := d.pid.Value()
	if pidText == "" {
		pidText = d.pid.Placeholder
	}
	if pid, err := strconv.Atoi(pidText); err == nil {
		if p, ok := m.processByPID(d.serverID, pid); ok {
			pidLine += "   " + dim.Render(fmt.Sprintf("%s  %s", p.User, p.Command))
		}
	}

	var sigs []string
	for _, s := range killSignals {
		item := fmt.Sprintf("%s %d", s, signalNumbers[s]) // e.g. "KILL 9"
		if signalMatches(d.signal.Value(), s) {
			sigs = append(sigs, lipgloss.NewStyle().Foreground(Lime).Bold(true).Render(item))
		} else {
			sigs = append(sigs, dim.Render(item))
		}
	}
	sigLine := label.Render("Signal:") + lipgloss.NewStyle().Width(8).Render(d.signal.View()) + "  " + strings.Join(sigs, "  ")

	help := dim.Render("(tab) switch field  (←/→) pick signal  (enter) send  (esc) cancel")
	if d.err != "" {
		help = ErrorStyle.Render(d.err)
	}
	name := ""
	if v := m.server(d.serverID); v != nil {
		name = " on " + v.Name
	}
	lines := []string{pidLine, sigLine, help}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, inner, "…")
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(Lime).
		Padding(0, 1).Width(inner + 2).Render(strings.Join(lines, "\n"))
	// Title in the top border
	title := lipgloss.NewStyle().Foreground(Lime).Bold(true).Render(" Kill process" + name + " ")
	first, rest, _ := strings.Cut(box, "\n")
	if lipgloss.Width(first) > lipgloss.Width(title)+3 {
		first = ansi.Truncate(first, 2, "") + title + ansi.TruncateLeft(first, 2+lipgloss.Width(title), "")
	}
	return first + "\n" + rest
}
