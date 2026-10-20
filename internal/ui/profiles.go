package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"vps-fleet-tui/internal/config"
	"vps-fleet-tui/internal/vps"
)

// profilePicker is the state of the profile list (P).
type profilePicker struct {
	names  []string
	counts []int // servers per profile, -1 if the file can't be read
	errs   []string
	cursor int
}

// WithProfile records which profile the model was started with.
func (m Model) WithProfile(name string) Model {
	if name == "" {
		name = config.DefaultProfile
	}
	m.profile = name
	return m
}

func (m *Model) openProfiles() {
	pp := &profilePicker{names: config.ListProfiles()}
	for i, name := range pp.names {
		cfg, err := config.LoadProfile(name)
		if err != nil {
			pp.counts = append(pp.counts, -1)
			pp.errs = append(pp.errs, err.Error())
			continue
		}
		pp.counts = append(pp.counts, len(cfg.Servers))
		pp.errs = append(pp.errs, "")
		if name == m.profile {
			pp.cursor = i
		}
	}
	m.profiles = pp
	m.state = stateProfiles
}

func (m Model) updateProfiles(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	pp := m.profiles
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.state = stateFleet
		m.profiles = nil
	case "up", "k":
		pp.cursor = max(0, pp.cursor-1)
	case "down", "j":
		pp.cursor = min(len(pp.names)-1, pp.cursor+1)
	case "enter":
		name := pp.names[pp.cursor]
		m.state = stateFleet
		m.profiles = nil
		return m.switchProfile(name)
	}
	return m, nil
}

// switchProfile loads another server list and reconnects.
func (m Model) switchProfile(name string) (tea.Model, tea.Cmd) {
	cfg, err := config.LoadProfile(name)
	if err != nil {
		m.statusMessage = "Error: " + err.Error()
		return m, nil
	}
	m.loadServers(cfg.Servers)
	m.profile = name
	m.statusMessage = fmt.Sprintf("Profile %s: %d servers", name, len(cfg.Servers))
	return m, m.fetchMetrics()
}

// loadServers replaces the server list, dropping connections and cached state.
func (m *Model) loadServers(servers []vps.VPS) {
	for _, c := range m.clients {
		c.Close()
	}
	m.clients = map[string]*vps.Client{}
	m.status = map[string]*serverStatus{}
	m.procs = map[string]*procState{}
	m.manager.Servers = servers
	m.filter = ""
	m.selectedID = ""
	m.keepSelectionVisible()
}

func (m Model) renderProfiles(width int) string {
	pp := m.profiles
	title := lipgloss.NewStyle().Foreground(Cyan).Bold(true)
	dim := lipgloss.NewStyle().Foreground(DarkGray)
	lime := lipgloss.NewStyle().Foreground(Lime).Bold(true)

	lines := []string{title.Render("Profiles") + dim.Render("   default = config.yaml, others: "+config.ProfilesDir()), ""}
	for i, name := range pp.names {
		marker, label := "  ", name
		if i == pp.cursor {
			marker, label = lime.Render("▌ "), lime.Render(name)
		}
		info := ""
		switch {
		case pp.counts[i] < 0:
			info = ErrorStyle.Render(pp.errs[i])
		case pp.counts[i] == 1:
			info = dim.Render("1 server")
		default:
			info = dim.Render(fmt.Sprintf("%d servers", pp.counts[i]))
		}
		if name == m.profile {
			info += "  " + SuccessStyle.Render("● active")
		}
		lines = append(lines, marker+lipgloss.NewStyle().Width(24).Render(label)+info)
	}
	if len(pp.names) == 1 {
		lines = append(lines, "",
			dim.Render("No other profiles yet. Create "+filepath.Join(config.ProfilesDir(), "<name>.yaml")),
			dim.Render("with a servers: list in the same format as config.yaml."))
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}
