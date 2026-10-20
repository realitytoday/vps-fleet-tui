package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	labelStyle = lipgloss.NewStyle().Foreground(Cyan).Bold(true).Width(10).Padding(0, 1) // fits the longest inline label ("Disk IO:")
	valueStyle = lipgloss.NewStyle().Foreground(White)
)

func shortUptime(long string) string {
	if long == "-" || long == "Unknown" {
		return "  0d  0h  0m  0s"
	}
	// Example: "up 5 days, 23 hours, 33 minutes"
	s := strings.TrimPrefix(long, "up ")
	parts := strings.Split(s, ",")

	var d, h, m, sec int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.Contains(p, "day") {
			fmt.Sscanf(p, "%d", &d)
		} else if strings.Contains(p, "hour") {
			fmt.Sscanf(p, "%d", &h)
		} else if strings.Contains(p, "minute") {
			fmt.Sscanf(p, "%d", &m)
		} else if strings.Contains(p, "second") {
			fmt.Sscanf(p, "%d", &sec)
		}
	}

	f := func(val int, unit string, w int) string {
		format := fmt.Sprintf("%%%dd%%s", w-1)
		return fmt.Sprintf(format, val, unit)
	}

	// Format:   5d 23h 17m 12s (16 chars)
	return fmt.Sprintf("%s %2dh %2dm %2ds", f(d, "d", 4), h, m, sec)
}

func (m Model) renderDashboard(width int) string {
	v := m.selected()
	if v == nil {
		return "No VPS selected. Press Enter on a server in the fleet view."
	}
	st := m.statusOf(v)

	// width is the usable content width; every line below must fit within it
	contentWidth := max(16, width)
	valueWidth := contentWidth - labelStyle.GetWidth() - 1
	dim := lipgloss.NewStyle().Foreground(DarkGray)

	s := strings.Builder{}
	s.WriteString(TitleStyle.MaxWidth(contentWidth).Render("Dashboard: "+v.Name) + "\n\n")

	// Fixed width labels; values wrap within their own column so continuation lines stay aligned
	renderLine := func(label, value string, style lipgloss.Style) string {
		return lipgloss.JoinHorizontal(lipgloss.Top,
			labelStyle.Render(label), " ", style.Width(valueWidth).Render(value)) + "\n"
	}

	s.WriteString(renderLine("Host:", v.Host, valueStyle))
	if v.Group != "" {
		s.WriteString(renderLine("Group:", v.Group, valueStyle))
	}

	switch m.healthOf(v) {
	case healthOffline:
		s.WriteString(renderLine("Status:", "Offline", ErrorStyle))
		s.WriteString(renderLine("Error:", st.err.Error(), dim))
		return s.String()
	case healthConnecting:
		s.WriteString(renderLine("Status:", "Connecting...", WarningStyle))
		return s.String()
	case healthStale:
		s.WriteString(renderLine("Status:", "Stale ("+ago(st.lastUpdate)+")", WarningStyle))
	default:
		s.WriteString(renderLine("Status:", "Online", SuccessStyle))
	}

	metrics := v.Metrics
	s.WriteString(renderLine("OS:", metrics.OS, valueStyle))
	uptime := metrics.Uptime
	if lipgloss.Width(uptime) > valueWidth {
		uptime = strings.Join(strings.Fields(shortUptime(uptime)), " ") // e.g. "6d 4h 20m 0s"
	}
	s.WriteString(renderLine("Uptime:", uptime, valueStyle))

	load := fmt.Sprintf("%.2f %.2f %.2f", metrics.Load1, metrics.Load5, metrics.Load15)
	loadStyle := valueStyle
	if metrics.Cores > 0 {
		unit := "cores"
		if metrics.Cores == 1 {
			unit = "core"
		}
		load += fmt.Sprintf(" (%d %s)", metrics.Cores, unit)
		if metrics.Load5 > float64(metrics.Cores) {
			loadStyle = WarningStyle
		}
	}
	s.WriteString(renderLine("Load:", load, loadStyle))

	// Health: failed units, pending updates, reboot
	var health []string
	if metrics.FailedUnits > 0 {
		health = append(health, ErrorStyle.Render(fmt.Sprintf("%d failed units", metrics.FailedUnits)))
	}
	switch {
	case st.updates > 0:
		health = append(health, WarningStyle.Render(fmt.Sprintf("%d updates", st.updates)))
	case st.updates < 0 && st.updatesChecked.IsZero():
		health = append(health, dim.Render("checking updates…"))
	}
	if metrics.RebootRequired {
		health = append(health, WarningStyle.Render("reboot required"))
	}
	if len(health) == 0 {
		health = append(health, SuccessStyle.Render("✓ healthy"))
	}
	s.WriteString(renderLine("Health:", strings.Join(health, ", "), lipgloss.NewStyle()))
	s.WriteString("\n")

	// Gauge line: " " + bar + " 100.0%" (7 chars) must fit in contentWidth
	gaugeWidth := max(5, contentWidth-8)
	gaugeLine := func(pct float64) string {
		return " " + bar(pct, gaugeWidth) + fmt.Sprintf(" %5.1f%%", pct) + "\n"
	}
	trendLine := func(hist []float64) string {
		if len(hist) < 2 {
			return ""
		}
		return " " + sparkline(hist, gaugeWidth) + "\n"
	}

	s.WriteString(renderLine("CPU:", "", valueStyle))
	s.WriteString(gaugeLine(metrics.CPUUsage))
	s.WriteString(trendLine(st.cpuHist) + "\n")

	s.WriteString(renderLine("Memory:", formatBytes(metrics.MemUsed)+" / "+formatBytes(metrics.MemTotal), valueStyle))
	s.WriteString(gaugeLine(memPct(metrics)))
	s.WriteString(trendLine(st.memHist) + "\n")

	s.WriteString(renderLine("Disk:", formatBytes(metrics.DiskUsed)+" / "+formatBytes(metrics.DiskTotal), valueStyle))
	s.WriteString(gaugeLine(diskPct(metrics)))

	if metrics.HasIO {
		s.WriteString("\n")
		s.WriteString(renderLine("Disk IO:",
			"R "+formatRate(metrics.DiskReadBps)+"  W "+formatRate(metrics.DiskWriteBps), valueStyle))
		s.WriteString(renderLine("Net IO:",
			"↓ "+formatRate(metrics.NetRxBps)+"  ↑ "+formatRate(metrics.NetTxBps), valueStyle))
	}

	s.WriteString("\n" + dim.Render(" updated "+ago(st.lastUpdate)))
	return s.String()
}

// ago formats the time since t, e.g. "3s ago".
func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm ago", int(d.Minutes()))
}

// formatRate formats bytes per second, e.g. "1.2 MB/s".
func formatRate(bps float64) string {
	return formatBytes(uint64(bps+0.5)) + "/s"
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
