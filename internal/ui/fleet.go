package ui

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"
	"vps-fleet-tui/internal/theme"
	"vps-fleet-tui/internal/vps"
)

const (
	historyLen     = 60 // samples kept per server (5 minutes at a 5s tick)
	staleAfter     = 30 * time.Second
	critPercent    = 90.0
	updatesEvery   = 10 * time.Minute
	cardInnerWidth = 28
)

// serverStatus is runtime state per server, keyed by VPS ID so it survives config reloads.
type serverStatus struct {
	err            error
	lastUpdate     time.Time
	inFlight       bool
	cpuHist        []float64
	memHist        []float64
	updates        int // pending package updates, -1 = unknown
	updatesChecked time.Time
	updatesFlight  bool
}

func newServerStatus() *serverStatus {
	return &serverStatus{updates: -1}
}

func pushHist(h []float64, v float64) []float64 {
	h = append(h, v)
	if len(h) > historyLen {
		h = h[len(h)-historyLen:]
	}
	return h
}

type viewMode int

const (
	modeTable viewMode = iota
	modeCards
)

type sortKey int

const (
	sortConfig sortKey = iota // order of the servers in config.yaml (the default)
	sortName
	sortID
	sortStatus
	sortCPU
	sortMem
	sortDisk
	sortCount
)

func (s sortKey) String() string {
	return [...]string{"config", "name", "id", "status", "cpu", "mem", "disk"}[s]
}

// health classifies a server for colouring and sorting.
type health int

const (
	healthOffline health = iota
	healthStale
	healthConnecting
	healthOnline
)

func (m Model) statusOf(v *vps.VPS) *serverStatus {
	if s, ok := m.status[v.ID]; ok {
		return s
	}
	return newServerStatus()
}

func (m Model) healthOf(v *vps.VPS) health {
	st := m.statusOf(v)
	switch {
	case st.err != nil:
		return healthOffline
	case v.Metrics == nil:
		return healthConnecting
	case time.Since(st.lastUpdate) > staleAfter:
		return healthStale
	}
	return healthOnline
}

func memPct(mt *vps.Metrics) float64 {
	if mt == nil || mt.MemTotal == 0 {
		return 0
	}
	return float64(mt.MemUsed) / float64(mt.MemTotal) * 100
}

func diskPct(mt *vps.Metrics) float64 {
	if mt == nil || mt.DiskTotal == 0 {
		return 0
	}
	return float64(mt.DiskUsed) / float64(mt.DiskTotal) * 100
}

// alerts lists problems worth attention; critical ones first.
func (m Model) alerts(v *vps.VPS) (crit, warn []string) {
	mt := v.Metrics
	if mt == nil {
		return nil, nil
	}
	if mt.FailedUnits > 0 {
		crit = append(crit, fmt.Sprintf("%d failed", mt.FailedUnits))
	}
	if diskPct(mt) >= critPercent {
		crit = append(crit, "disk full")
	}
	if memPct(mt) >= critPercent {
		crit = append(crit, "mem high")
	}
	if mt.Cores > 0 && mt.Load5 > float64(mt.Cores) {
		warn = append(warn, "load high")
	}
	if mt.RebootRequired {
		warn = append(warn, "reboot")
	}
	if u := m.statusOf(v).updates; u > 0 {
		warn = append(warn, fmt.Sprintf("%d upd", u))
	}
	return crit, warn
}

// visible returns the servers after filtering, grouped (if any groups are configured) and sorted.
func (m Model) visible() []*vps.VPS {
	q := strings.ToLower(m.filter)
	var list []*vps.VPS
	for i := range m.manager.Servers {
		v := &m.manager.Servers[i]
		if q == "" || strings.Contains(strings.ToLower(v.Name+" "+v.Host+" "+v.Group), q) {
			list = append(list, v)
		}
	}

	metric := func(v *vps.VPS) float64 {
		switch m.sortBy {
		case sortCPU:
			if v.Metrics != nil {
				return v.Metrics.CPUUsage
			}
		case sortMem:
			return memPct(v.Metrics)
		case sortDisk:
			return diskPct(v.Metrics)
		}
		return -1
	}
	slices.SortStableFunc(list, func(a, b *vps.VPS) int {
		// Groups first (alphabetical, ungrouped last), then the chosen key
		if a.Group != b.Group {
			if a.Group == "" || b.Group == "" {
				return cmp.Compare(b.Group, a.Group)
			}
			return cmp.Compare(a.Group, b.Group)
		}
		switch m.sortBy {
		case sortConfig:
			return 0 // stable sort keeps the order of the config file
		case sortID:
			if c := naturalCompare(a.ID, b.ID); c != 0 {
				return c
			}
		case sortStatus:
			if c := cmp.Compare(m.healthOf(a), m.healthOf(b)); c != 0 {
				return c // offline first
			}
		case sortCPU, sortMem, sortDisk:
			if c := cmp.Compare(metric(b), metric(a)); c != 0 {
				return c // highest first
			}
		}
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return list
}

// naturalCompare orders strings with embedded numbers numerically,
// so "crawler-2" < "crawler-10".
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := digitPrefix(a), digitPrefix(b)
		if da != "" && db != "" {
			na, nb := strings.TrimLeft(da, "0"), strings.TrimLeft(db, "0")
			if c := cmp.Compare(len(na), len(nb)); c != 0 {
				return c
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if c := cmp.Compare(strings.ToLower(a[:1]), strings.ToLower(b[:1])); c != 0 {
			return c
		}
		a, b = a[1:], b[1:]
	}
	return cmp.Compare(len(a), len(b))
}

func digitPrefix(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

func (m Model) hasGroups() bool {
	for _, v := range m.manager.Servers {
		if v.Group != "" {
			return true
		}
	}
	return false
}

// cursor returns the index of the selected server in list, or 0.
func (m Model) cursor(list []*vps.VPS) int {
	for i, v := range list {
		if v.ID == m.selectedID {
			return i
		}
	}
	return 0
}

// --- small render helpers ---------------------------------------------------

// bar renders a fixed-width usage bar with the configured gradient (see Settings).
// Thresholds are reported in the ALERTS column instead of by colour.
func bar(pct float64, width int) string {
	start, end := theme.Gradient()
	p := progress.New(progress.WithGradient(start, end), progress.WithoutPercentage(), progress.WithWidth(max(1, width)))
	return p.ViewAs(max(0, min(1, pct/100)))
}

// No full block "█": the tallest level is 7/8 high, so there is always a thin
// gap between a trend line and the bar drawn above it.
var sparkRunes = []rune("▁▂▃▄▅▆▇")

// sparkDarken is how far trend lines are blended towards black (0 = same as the bars)
const sparkDarken = 0.3

// sparkline renders the last `width` values (0–100) as a trend line. Each column
// gets the gradient colour of the bar cell directly above it.
func sparkline(values []float64, width int) string {
	if len(values) > width {
		values = values[len(values)-width:]
	}
	// Same colours as the bars, so each trend column matches the bar cell above it
	start, end := theme.Gradient()
	gradientFrom, _ := colorful.Hex(start)
	gradientTo, _ := colorful.Hex(end)
	var b strings.Builder
	for col, v := range values {
		// round (not truncate), otherwise only exactly 100% reaches the top level
		i := int(math.Round(v / 100 * float64(len(sparkRunes)-1)))
		p := 0.5
		if width > 1 {
			p = float64(col) / float64(width-1)
		}
		// a shade darker than the bar so the trend reads as secondary
		c := gradientFrom.BlendLuv(gradientTo, p)
		color := lipgloss.Color(c.BlendLab(colorful.Color{}, sparkDarken).Hex())
		b.WriteString(lipgloss.NewStyle().Foreground(color).Render(string(sparkRunes[max(0, min(len(sparkRunes)-1, i))])))
	}
	return b.String()
}

// fit pads or truncates s (which may contain ANSI codes) to exactly w cells.
func fit(s string, w int) string {
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}

// compactUptime turns "5 days, 23 hours, 33 minutes" into "5d 23h" / "23h 33m".
func compactUptime(long string) string {
	f := strings.Fields(shortUptime(long)) // "5d 23h 33m 0s"
	if len(f) < 3 {
		return "-"
	}
	if f[0] != "0d" {
		return f[0] + " " + f[1]
	}
	return f[1] + " " + f[2]
}

func (m Model) statusText(v *vps.VPS) string {
	switch m.healthOf(v) {
	case healthOffline:
		return ErrorStyle.Render("● offline")
	case healthStale:
		return WarningStyle.Render("● stale")
	case healthConnecting:
		return lipgloss.NewStyle().Foreground(DarkGray).Render("○ connect")
	}
	return SuccessStyle.Render("● online")
}

func (m Model) alertText(v *vps.VPS) string {
	crit, warn := m.alerts(v)
	var parts []string
	for _, c := range crit {
		parts = append(parts, ErrorStyle.Render(c))
	}
	for _, w := range warn {
		parts = append(parts, WarningStyle.Render(w))
	}
	return strings.Join(parts, " ")
}

// --- table view ---------------------------------------------------------------

type column struct {
	title string
	width int
	sort  sortKey // -1 = not sortable
	cell  func(v *vps.VPS) string
}

const colGap = 2

func (m Model) metricCell(pct func(*vps.Metrics) float64) func(*vps.VPS) string {
	return func(v *vps.VPS) string {
		if v.Metrics == nil || m.healthOf(v) == healthOffline {
			return "-"
		}
		p := pct(v.Metrics)
		return bar(p, 5) + fmt.Sprintf(" %3.0f%%", p)
	}
}

// tableColumns picks the columns that fit in width, dropping the least important first.
func (m Model) tableColumns(width int) []column {
	name := column{"NAME", 16, sortName, func(v *vps.VPS) string { return v.Name }}
	host := column{"HOST", 15, -1, func(v *vps.VPS) string { return v.Host }}
	status := column{"STATUS", 9, sortStatus, m.statusText}
	cpu := column{"CPU", 10, sortCPU, m.metricCell(func(mt *vps.Metrics) float64 { return mt.CPUUsage })}
	mem := column{"MEM", 10, sortMem, m.metricCell(memPct)}
	disk := column{"DISK", 10, sortDisk, m.metricCell(diskPct)}
	load := column{"LOAD", 5, -1, func(v *vps.VPS) string {
		if v.Metrics == nil {
			return "-"
		}
		return fmt.Sprintf("%.2f", v.Metrics.Load1)
	}}
	uptime := column{"UPTIME", 7, -1, func(v *vps.VPS) string {
		if v.Metrics == nil {
			return "-"
		}
		return compactUptime(v.Metrics.Uptime)
	}}
	alerts := column{"ALERTS", 8, -1, m.alertText}

	total := func(cols []column) int {
		w := 2 // left and right gutter (selection markers)
		for _, c := range cols {
			w += c.width + colGap
		}
		return w - colGap
	}

	cols := []column{name, host, status, cpu, mem, disk, load, uptime, alerts}
	for _, drop := range []string{"HOST", "LOAD", "UPTIME", "ALERTS"} {
		if total(cols) <= width {
			break
		}
		cols = slices.DeleteFunc(cols, func(c column) bool { return c.title == drop })
	}
	// The last column takes any spare width
	cols[len(cols)-1].width += max(0, width-total(cols))
	return cols
}

// renderTable renders the fleet table and returns its lines plus the line range of the selection.
func (m Model) renderTable(width int, list []*vps.VPS) (lines []string, selBottom int) {
	cols := m.tableColumns(width)
	headStyle := lipgloss.NewStyle().Foreground(Cyan).Bold(true)
	rule := lipgloss.NewStyle().Foreground(DarkGray)

	var head []string
	for _, c := range cols {
		t := c.title
		if c.sort == m.sortBy {
			t += map[bool]string{true: "▲", false: "▼"}[c.sort == sortName || c.sort == sortStatus]
		}
		head = append(head, fit(t, c.width))
	}
	lines = append(lines, " "+headStyle.Render(strings.Join(head, strings.Repeat(" ", colGap))))
	lines = append(lines, rule.Render(strings.Repeat("─", max(0, width))))

	cur := m.cursor(list)
	groups := m.hasGroups()
	for i, v := range list {
		if groups && (i == 0 || list[i-1].Group != v.Group) {
			lines = append(lines, m.groupHeading(v.Group, list))
		}
		sel := i == cur
		var cells []string
		for j, c := range cols {
			text := c.cell(v)
			if sel && j == 0 {
				text = selectedNameStyle.Render(text)
			}
			cells = append(cells, fit(text, c.width))
		}
		// Selected row: lime markers in the left and right gutters instead of a
		// background bar, so the coloured bars and status stay readable
		left, right := " ", " "
		if sel {
			left, right = selectedNameStyle.Render("▌"), selectedNameStyle.Render("▐")
			selBottom = len(lines)
		}
		row := left + fit(strings.Join(cells, strings.Repeat(" ", colGap)), max(0, width-2)) + right
		lines = append(lines, row)
	}
	return lines, selBottom
}

func (m Model) groupHeading(group string, list []*vps.VPS) string {
	n := 0
	for _, v := range list {
		if v.Group == group {
			n++
		}
	}
	if group == "" {
		group = "ungrouped"
	}
	return lipgloss.NewStyle().Foreground(Lime).Bold(true).Render("▸ "+group) +
		lipgloss.NewStyle().Foreground(DarkGray).Render(fmt.Sprintf(" (%d)", n))
}

// --- card view ----------------------------------------------------------------

func cardColumns(width int) int {
	return max(1, (width+1)/(cardInnerWidth+4+1)) // border+padding = 4, gap = 1
}

func (m Model) renderCard(v *vps.VPS, selected bool) string {
	w := cardInnerWidth
	dim := lipgloss.NewStyle().Foreground(DarkGray)
	status := m.statusText(v)
	name := lipgloss.NewStyle().Bold(true).Render(ansi.Truncate(v.Name, w-lipgloss.Width(status)-1, "…"))
	lines := []string{
		name + strings.Repeat(" ", max(1, w-lipgloss.Width(name)-lipgloss.Width(status))) + status,
		dim.Render(fit(strings.TrimSuffix(v.Host+" · "+v.Group, " · "), w)),
	}

	metric := func(label string, pct float64) string {
		if v.Metrics == nil || m.healthOf(v) == healthOffline {
			return dim.Render(label + " -")
		}
		return label + " " + bar(pct, w-9) + fmt.Sprintf(" %3.0f%%", pct)
	}
	var cpu float64
	if v.Metrics != nil {
		cpu = v.Metrics.CPUUsage
	}
	lines = append(lines,
		metric("CPU", cpu),
		metric("MEM", memPct(v.Metrics)),
		metric("DSK", diskPct(v.Metrics)),
	)

	if mt := v.Metrics; mt != nil && m.healthOf(v) != healthOffline {
		lines = append(lines, dim.Render(fmt.Sprintf("load %.2f · up %s", mt.Load1, compactUptime(mt.Uptime))))
		if a := m.alertText(v); a != "" {
			lines = append(lines, a)
		} else {
			lines = append(lines, SuccessStyle.Render("✓ healthy"))
		}
	} else if st := m.statusOf(v); st.err != nil {
		lines = append(lines, ErrorStyle.Render(ansi.Truncate(st.err.Error(), w, "…")), "")
	} else {
		lines = append(lines, "", "")
	}

	border := DarkGray
	switch {
	case selected:
		border = Lime
	case m.healthOf(v) == healthOffline:
		border = Red
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Width(w + 2).
		Render(strings.Join(lines, "\n"))
}

func (m Model) renderCards(width int, list []*vps.VPS) (lines []string, selBottom int) {
	cols := cardColumns(width)
	cur := m.cursor(list)
	groups := m.hasGroups()

	for start := 0; start < len(list); {
		// A grid row never spans two groups
		end := start
		for end < len(list) && end-start < cols && list[end].Group == list[start].Group {
			end++
		}
		if groups && (start == 0 || list[start-1].Group != list[start].Group) {
			lines = append(lines, m.groupHeading(list[start].Group, list))
		}
		var cards []string
		for i := start; i < end; i++ {
			if i > start {
				cards = append(cards, " ")
			}
			cards = append(cards, m.renderCard(list[i], i == cur))
		}
		row := strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, cards...), "\n")
		if cur >= start && cur < end {
			selBottom = len(lines) + len(row) - 1
		}
		lines = append(lines, row...)
		start = end
	}
	return lines, selBottom
}

// cardNeighbour returns the index reached by moving up/down one grid row in card view.
func (m Model) cardNeighbour(list []*vps.VPS, cur, dir, cols int) int {
	// Build the same row layout as renderCards
	type pos struct{ row, col int }
	positions := make([]pos, len(list))
	row := 0
	for start := 0; start < len(list); row++ {
		end := start
		for end < len(list) && end-start < cols && list[end].Group == list[start].Group {
			positions[end] = pos{row, end - start}
			end++
		}
		start = end
	}
	target := positions[cur].row + dir
	best := cur
	for i, p := range positions {
		if p.row == target {
			best = i
			if p.col >= positions[cur].col {
				break
			}
		}
	}
	return best
}

// renderFleet renders the fleet list (table or cards) into exactly height lines.
func (m Model) renderFleet(width, height int) string {
	list := m.visible()
	var lines []string

	if m.filtering || m.filter != "" {
		cursor := ""
		if m.filtering {
			cursor = "█"
		}
		lines = append(lines,
			lipgloss.NewStyle().Foreground(Cyan).Render("/ "+m.filter+cursor)+
				lipgloss.NewStyle().Foreground(DarkGray).Render(
					fmt.Sprintf("   %d of %d servers", len(list), len(m.manager.Servers))))
	}
	height -= len(lines)

	if len(list) == 0 {
		msg := "No servers configured. Add them to " + "~/.config/vps-fleet/config.yaml"
		if m.filter != "" {
			msg = "No servers match the filter"
		}
		return strings.Join(append(lines, "", " "+msg), "\n")
	}

	var body []string
	var selBottom int
	if m.mode == modeCards {
		body, selBottom = m.renderCards(width, list)
	} else {
		body, selBottom = m.renderTable(width, list)
	}

	// Scroll so the selection is visible (table header stays pinned)
	pinned := 0
	if m.mode == modeTable {
		pinned = 2
	}
	pinned = min(pinned, len(body))
	avail := max(1, height-pinned) // always show at least the selected line
	offset := 0
	if selBottom-pinned >= avail {
		offset = selBottom - pinned - avail + 1
	}
	rest := body[min(len(body), pinned+offset):]
	rest = rest[:min(len(rest), avail)]
	visible := append(append([]string{}, body[:pinned]...), rest...)
	return strings.Join(append(lines, visible...), "\n")
}
