package plugins

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Plugin defines the interface for TUI plugins
type Plugin interface {
	Name() string
	Init() tea.Cmd
	Update(tea.Msg) (Plugin, tea.Cmd)
	View() string
	SetSize(width, height int)
}
