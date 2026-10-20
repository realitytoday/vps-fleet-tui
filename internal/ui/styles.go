package ui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	Lime     = lipgloss.Color("#d7ff00")
	Cyan     = lipgloss.Color("#00d7ff")
	Green    = lipgloss.Color("#00ff00")
	Yellow   = lipgloss.Color("#ffff00")
	Red      = lipgloss.Color("#ff0000")
	DarkGray = lipgloss.Color("#5f5f5f")
	White    = lipgloss.Color("#ffffff")
	Black    = lipgloss.Color("#000000")

	// Styles
	MainStyle = lipgloss.NewStyle()

	HeaderStyle = lipgloss.NewStyle().
			Foreground(Cyan).
			Bold(true).
			Padding(0, 1)

	// Default terminal foreground, same as the CPU/MEM line in the header
	FooterStyle = lipgloss.NewStyle().
			Padding(0, 1)

	TitleStyle = lipgloss.NewStyle().
			Background(Lime).
			Foreground(Black).
			Bold(true).
			Padding(0, 1)

	StatusStyle = lipgloss.NewStyle().
			Foreground(White).
			Padding(0, 1)

	// Table Styles
	TableHeaderStyle = lipgloss.NewStyle().
				Foreground(Cyan).
				Bold(true).
				Padding(0, 1).
				Border(lipgloss.NormalBorder(), false, false, true, false).
				BorderForeground(DarkGray)

	// Selected fleet row: marker + name in lime (no background bar, which clashed with the coloured cells)
	selectedNameStyle = lipgloss.NewStyle().Foreground(Lime).Bold(true)

	TableNormalStyle = lipgloss.NewStyle().
				Padding(0, 1)

	SuccessStyle = lipgloss.NewStyle().Foreground(Green)
	WarningStyle = lipgloss.NewStyle().Foreground(Yellow)
	ErrorStyle   = lipgloss.NewStyle().Foreground(Red)
)
