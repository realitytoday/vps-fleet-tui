package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// renderTopHeader renders the one-line summary above the full-screen process list, e.g.
// "web-1  CPU ████░░ 89%  MEM ████░░ 80%  Load 2.51 2.18 2.17  Up 6d 12h".
func (m Model) renderTopHeader(width int) string {
	v := m.selected()
	if v == nil {
		return ""
	}
	name := lipgloss.NewStyle().Foreground(Lime).Bold(true).Render(v.Name)
	mt := v.Metrics
	if mt == nil || m.healthOf(v) == healthOffline {
		return name + "  " + m.statusText(v)
	}
	label := lipgloss.NewStyle().Foreground(Cyan).Bold(true)
	rest := fmt.Sprintf("  %s %.2f %.2f %.2f  %s %s",
		label.Render("Load"), mt.Load1, mt.Load5, mt.Load15, label.Render("Up"), compactUptime(mt.Uptime))
	// Two bars share the width that's left; each "CPU " + bar + " 100%"
	barWidth := max(5, min(25, (width-lipgloss.Width(name)-lipgloss.Width(rest)-2*(4+5+2))/2))
	gauge := func(title string, pct float64) string {
		return "  " + label.Render(title) + " " + bar(pct, barWidth) + fmt.Sprintf(" %3.0f%%", pct)
	}
	return ansi.Truncate(name+gauge("CPU", mt.CPUUsage)+gauge("MEM", memPct(mt))+rest, width, "…")
}

// renderProcesses renders a top-like list of the selected server's busiest processes
// into width x height.
func (m Model) renderProcesses(width, height int) string {
	title := lipgloss.NewStyle().Foreground(Cyan).Bold(true)
	dim := lipgloss.NewStyle().Foreground(DarkGray)

	v := m.selected()
	if v == nil {
		return ""
	}
	sortName := "CPU"
	if m.procsByMem {
		sortName = "memory"
	}
	ps := m.procs[v.ID]
	switch {
	case m.healthOf(v) == healthOffline:
		return title.Render("Processes") + "\n\n" + dim.Render("server is offline")
	case ps == nil || (ps.list == nil && ps.err == nil):
		return title.Render("Processes") + "\n\n" + dim.Render("loading…")
	case ps.list == nil:
		return title.Render("Processes") + "\n\n" + ErrorStyle.Render(ansi.Truncate(ps.err.Error(), width, "…"))
	}

	// Title line: "Processes · 123 total, 1 running" ... "sorted by CPU (o)"
	left := title.Render("Processes")
	if ps.list.Summary != "" {
		left += dim.Render(" · " + ps.list.Summary)
	}
	right := dim.Render("sorted by " + sortName + " (o)")
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		left = ansi.Truncate(left, max(0, width-lipgloss.Width(right)-2), "…")
		gap = max(2, width-lipgloss.Width(left)-lipgloss.Width(right))
	}
	lines := []string{left + strings.Repeat(" ", gap) + right, ""}

	// Columns; TIME+ and USER are dropped on narrow screens
	showTime, showUser := width >= 70, width >= 55
	header := fmt.Sprintf("%7s", "PID")
	if showUser {
		header += fmt.Sprintf(" %-9s", "USER")
	}
	header += fmt.Sprintf(" %5s %5s %7s", "CPU%", "MEM%", "RES")
	if showTime {
		header += fmt.Sprintf(" %9s", "TIME+")
	}
	header += "  COMMAND"
	lines = append(lines,
		lipgloss.NewStyle().Foreground(Cyan).Bold(true).Render(fit(header, width)),
		dim.Render(strings.Repeat("─", max(0, width))))

	sortStyle := lipgloss.NewStyle().Bold(true) // the column the list is sorted by
	for _, p := range ps.list.Procs {
		if len(lines) >= height {
			break
		}
		row := fmt.Sprintf("%7d", p.PID)
		if showUser {
			row += " " + fit(p.User, 9)
		}
		cpu, mem := fmt.Sprintf("%5.1f", p.CPU), fmt.Sprintf("%5.1f", p.Mem)
		if m.procsByMem {
			mem = sortStyle.Render(mem)
		} else {
			cpu = sortStyle.Render(cpu)
		}
		row += " " + cpu + " " + mem + fmt.Sprintf(" %7s", p.RES)
		if showTime {
			row += fmt.Sprintf(" %9s", p.Time)
		}
		cmdWidth := width - lipgloss.Width(row) - 2
		row += "  " + ansi.Truncate(p.Command, max(0, cmdWidth), "…")
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}
