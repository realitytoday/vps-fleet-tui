package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) renderHeader() string {
	logo := HeaderStyle.Render("vps-fleet v0.1.0")
	if m.profile != "" {
		logo += lipgloss.NewStyle().Foreground(DarkGray).Render("· " + m.profile)
	}

	context := "no server selected"
	if v := m.selected(); v != nil {
		context = fmt.Sprintf("%s [%s]", v.Name, v.Host)
	}

	contextInfo := context // default foreground, same as "N servers" in the summary
	resources := m.fleetSummary()

	inner := m.width - 2 // outer horizontal padding
	logoW, ctxW := lipgloss.Width(logo), lipgloss.Width(contextInfo)

	// Single line: logo left, context centred, resources right.
	// If space is tight, shift the context left; drop the resources only if that is not enough.
	right := resources
	ctxStart := max(logoW+1, min((inner-ctxW)/2, inner-lipgloss.Width(right)-1-ctxW))
	if ctxStart+ctxW+1+lipgloss.Width(right) > inner {
		right = ""
		ctxStart = max(logoW+1, (inner-ctxW)/2)
	}

	header := logo +
		strings.Repeat(" ", ctxStart-logoW) +
		contextInfo +
		strings.Repeat(" ", max(0, inner-ctxStart-ctxW-lipgloss.Width(right))) +
		right

	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(DarkGray).
		Padding(0, 1).
		Width(m.width).
		Render(header)
}

// fleetSummary returns e.g. "3 servers · 2 online · 1 offline · 1 alert".
func (m Model) fleetSummary() string {
	var online, offline, alerting int
	for i := range m.manager.Servers {
		v := &m.manager.Servers[i]
		switch m.healthOf(v) {
		case healthOnline, healthStale:
			online++
		case healthOffline:
			offline++
		}
		if crit, _ := m.alerts(v); len(crit) > 0 {
			alerting++
		}
	}
	sep := lipgloss.NewStyle().Foreground(DarkGray).Render(" · ")
	count := fmt.Sprintf("%d servers", len(m.manager.Servers))
	if len(m.manager.Servers) == 1 {
		count = "1 server"
	}
	parts := []string{count}
	parts = append(parts, SuccessStyle.Render(fmt.Sprintf("%d online", online)))
	if offline > 0 {
		parts = append(parts, ErrorStyle.Render(fmt.Sprintf("%d offline", offline)))
	}
	if alerting > 0 {
		parts = append(parts, ErrorStyle.Render(fmt.Sprintf("%d alerting", alerting)))
	}
	return strings.Join(parts, sep)
}
