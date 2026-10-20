package plugins

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
)

// Colours match the main UI (internal/ui/styles.go)
var (
	lime     = lipgloss.Color("#d7ff00")
	cyan     = lipgloss.Color("#00d7ff")
	darkGray = lipgloss.Color("#5f5f5f")
	red      = lipgloss.Color("#ff0000")
)

// newStyledTable creates a table with the same look as the fleet view.
func newStyledTable(columns []table.Column) table.Model {
	t := table.New(table.WithColumns(columns))
	setTableFocus(&t, true)
	return t
}

// setTableFocus highlights the selected row in lime only when the table has focus.
func setTableFocus(t *table.Model, focused bool) {
	s := table.DefaultStyles()
	s.Header = lipgloss.NewStyle().Foreground(cyan).Bold(true).Padding(0, 1).
		Border(lipgloss.NormalBorder(), false, false, true, false).BorderForeground(darkGray)
	s.Cell = lipgloss.NewStyle().Padding(0, 1)
	s.Selected = lipgloss.NewStyle().Bold(true)
	if focused {
		s.Selected = s.Selected.Foreground(lime)
		t.Focus()
	} else {
		t.Blur()
	}
	t.SetStyles(s)
}

// fitLastColumn gives the last column whatever width is left (each column has 1 char padding per side).
func fitLastColumn(t *table.Model, width int) {
	cols := t.Columns()
	used := 0
	for _, c := range cols[:len(cols)-1] {
		used += c.Width + 2
	}
	cols[len(cols)-1].Width = max(10, width-used-2)
	t.SetColumns(cols)
}

// tableScrollbar renders a vertical track with a thumb showing which part of the list is visible.
func tableScrollbar(t table.Model) string {
	total, height := len(t.Rows()), t.Height()
	lines := []string{" ", " "} // next to the header and its underline
	if total <= height || height <= 0 {
		return strings.Join(lines, "\n")
	}
	thumb := max(1, height*height/total)
	pos := t.Cursor() * (height - thumb) / max(1, total-1)
	track := lipgloss.NewStyle().Foreground(darkGray).Render("│")
	bar := lipgloss.NewStyle().Foreground(lime).Render("┃")
	for i := 0; i < height; i++ {
		if i >= pos && i < pos+thumb {
			lines = append(lines, bar)
		} else {
			lines = append(lines, track)
		}
	}
	return strings.Join(lines, "\n")
}

// tableWithScrollbar renders the table with its scrollbar on the right.
func tableWithScrollbar(t table.Model) string {
	return lipgloss.JoinHorizontal(lipgloss.Top, t.View(), " ", tableScrollbar(t))
}

// positionText returns e.g. "unit 12 of 187".
func positionText(t table.Model, noun string) string {
	n := len(t.Rows())
	if n == 0 {
		return "no " + noun + "s"
	}
	return fmt.Sprintf("%s %d of %d", noun, t.Cursor()+1, n)
}

// statusLine colours error messages red.
func statusLine(msg string) string {
	// Server errors often span lines ("…: Directory not empty\nProcess exited with status 1")
	msg = strings.Join(strings.Fields(msg), " ")
	if strings.HasPrefix(strings.ToLower(msg), "error") {
		return lipgloss.NewStyle().Foreground(red).Render(msg)
	}
	return msg
}

// bottomLine puts left (e.g. "entry 3 of 14") on the left and the key hints
// right-aligned; hints that don't fit are dropped from the end.
func bottomLine(left string, keys []string, width int) string {
	right := ""
	for _, k := range keys {
		next := right
		if next != "" {
			next += "  "
		}
		next += k
		if lipgloss.Width(left)+2+lipgloss.Width(next) > width {
			break
		}
		right = next
	}
	gap := max(2, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + hint(right)
}

func hint(s string) string {
	return lipgloss.NewStyle().Foreground(darkGray).Render(s)
}
