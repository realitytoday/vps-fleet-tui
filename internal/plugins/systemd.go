package plugins

import (
	"fmt"
	"strings"
	"vps-fleet-tui/internal/vps"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type SystemdPlugin struct {
	client        *vps.Client
	table         table.Model
	width         int
	height        int
	statusMessage string
}

func NewSystemdPlugin(client *vps.Client) *SystemdPlugin {
	columns := []table.Column{
		{Title: "UNIT", Width: 40},
		{Title: "LOAD", Width: 10},
		{Title: "ACTIVE", Width: 10},
		{Title: "SUB", Width: 10},
		{Title: "DESCRIPTION", Width: 40},
	}

	return &SystemdPlugin{
		client: client,
		table:  newStyledTable(columns),
	}
}

func (p *SystemdPlugin) Name() string {
	return "Systemd"
}

func (p *SystemdPlugin) Init() tea.Cmd {
	return p.fetchUnits
}

func (p *SystemdPlugin) fetchUnits() tea.Msg {
	// --plain drops the "●" marker in front of failed units, which would shift the columns
	output, err := p.client.Run("systemctl list-units --all --no-pager --no-legend --plain")
	if err != nil {
		return fmt.Errorf("error fetching units: %v", err)
	}

	var rows []table.Row
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 5 {
			rows = append(rows, table.Row{
				fields[0],
				fields[1],
				fields[2],
				fields[3],
				strings.Join(fields[4:], " "),
			})
		}
	}

	return unitsMsg(rows)
}

type unitsMsg []table.Row

func (p *SystemdPlugin) Update(msg tea.Msg) (Plugin, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case unitsMsg:
		p.table.SetRows(msg)
		return p, nil

	case error:
		p.statusMessage = msg.Error()
		return p, nil

	case tea.KeyMsg:
		p.statusMessage = ""
		switch msg.String() {
		case "r":
			return p, p.fetchUnits
		case "s": // Start
			return p, p.unitAction("start")
		case "x": // Stop
			return p, p.unitAction("stop")
		case "t": // Restart
			return p, p.unitAction("restart")
		}
	}

	p.table, cmd = p.table.Update(msg)
	return p, cmd
}

func (p *SystemdPlugin) unitAction(action string) tea.Cmd {
	row := p.table.SelectedRow()
	if len(row) == 0 {
		return nil
	}
	unit := row[0]
	p.statusMessage = fmt.Sprintf("%s %s...", action, unit)
	return func() tea.Msg {
		_, err := p.client.Run(fmt.Sprintf("sudo systemctl %s -- %s", action, vps.ShellQuote(unit)))
		if err != nil {
			return fmt.Errorf("error %s unit %s: %v", action, unit, err)
		}
		return p.fetchUnits()
	}
}

func (p *SystemdPlugin) View() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		tableWithScrollbar(p.table),
		"\n"+statusLine(p.statusMessage),
		"\n"+bottomLine(positionText(p.table, "unit"), []string{"(r)eload", "(s)tart", "(x) stop", "(t) restart"}, p.width),
	)
}

func (p *SystemdPlugin) SetSize(width, height int) {
	p.width = width
	p.height = height
	tableWidth := width - 2 // room for the scrollbar
	p.table.SetWidth(tableWidth)
	p.table.SetHeight(height - 4)
	fitLastColumn(&p.table, tableWidth) // DESCRIPTION takes the remaining width
}
