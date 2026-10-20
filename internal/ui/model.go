package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/x/ansi"

	"vps-fleet-tui/internal/config"
	"vps-fleet-tui/internal/plugins"
	"vps-fleet-tui/internal/vps"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type state int

const (
	stateFleet state = iota
	stateDashboard
	statePlugin
	stateTop      // full-screen process list
	stateSettings // settings screen (c)
	stateProfiles // profile list (P)
)

type MetricsMsg struct {
	VPSID   string
	Metrics *vps.Metrics
	Err     error
}

type UpdatesMsg struct {
	VPSID string
	Count int
	Err   error
}

type Model struct {
	manager       *vps.Manager
	clients       map[string]*vps.Client   // one reusable SSH connection per server (by ID)
	status        map[string]*serverStatus // runtime state per server (by ID)
	state         state
	activePlugin  plugins.Plugin
	selectedID    string
	mode          viewMode
	sortBy        sortKey
	filter        string
	filtering     bool
	statusMessage string
	width         int
	height        int

	procs      map[string]*procState // process lists for the dashboard (by server ID)
	procsByMem bool                  // sort the process list by memory instead of CPU
	kill       *killDialog           // open kill dialog (top and dashboard views)

	settings     config.Settings // user preferences (settings.yaml)
	settingsForm *settingsForm   // state of the settings screen while it's open

	profile  string         // active server profile ("default" = config.yaml)
	profiles *profilePicker // profile list while it's open (P)
}

// procState is the latest process list of one server.
type procState struct {
	list     *vps.ProcessList
	err      error
	fetched  time.Time
	inFlight bool
	byMem    bool // sort order the list was fetched with
}

type ProcsMsg struct {
	VPSID string
	List  *vps.ProcessList
	ByMem bool
	Err   error
}

// procCmd fetches the selected server's process list if the dashboard shows it
// and the current one is missing, older than a tick, or in the wrong order.
func (m Model) procCmd() tea.Cmd {
	v := m.selected()
	if (m.state != stateDashboard && m.state != stateTop) || v == nil || m.healthOf(v) == healthOffline {
		return nil
	}
	ps := m.procs[v.ID]
	if ps == nil {
		ps = &procState{}
		m.procs[v.ID] = ps
	}
	if ps.inFlight || (ps.byMem == m.procsByMem && time.Since(ps.fetched) < 4*time.Second) {
		return nil
	}
	client := m.clients[v.ID]
	if client == nil {
		return nil
	}
	ps.inFlight = true
	id, byMem := v.ID, m.procsByMem
	return func() tea.Msg {
		list, err := client.FetchProcesses(byMem)
		return ProcsMsg{VPSID: id, List: list, ByMem: byMem, Err: err}
	}
}

func NewModel(manager *vps.Manager) Model {
	m := Model{
		manager: manager,
		clients: map[string]*vps.Client{},
		status:  map[string]*serverStatus{},
		procs:   map[string]*procState{},

		settings: config.DefaultSettings(),
		state:    stateFleet,
	}
	if list := m.visible(); len(list) > 0 {
		m.selectedID = list[0].ID
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.tick(),
		m.fetchMetrics(),
	)
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

type tickMsg struct{}

// fetchMetrics starts a metrics fetch for every server that has none running,
// and an update check for servers whose last one is older than updatesEvery.
func (m Model) fetchMetrics() tea.Cmd {
	var cmds []tea.Cmd
	for _, v := range m.manager.Servers {
		st := m.status[v.ID]
		if st == nil {
			st = newServerStatus()
			m.status[v.ID] = st
		}
		client := m.client(v)
		if client == nil {
			continue
		}
		if !st.inFlight {
			st.inFlight = true
			cmds = append(cmds, fetchServerMetrics(v.ID, client))
		}
		if !st.updatesFlight && time.Since(st.updatesChecked) > updatesEvery && st.err == nil {
			st.updatesFlight = true
			cmds = append(cmds, fetchServerUpdates(v.ID, client))
		}
	}
	return tea.Batch(cmds...)
}

// client returns the cached SSH client for v, creating it on first use.
func (m Model) client(v vps.VPS) *vps.Client {
	if c, ok := m.clients[v.ID]; ok {
		return c
	}
	c, err := vps.NewClient(v)
	if err != nil {
		m.status[v.ID].err = err
		return nil
	}
	m.clients[v.ID] = c
	return c
}

func fetchServerMetrics(id string, client *vps.Client) tea.Cmd {
	return func() tea.Msg {
		metrics, err := client.FetchMetrics()
		return MetricsMsg{VPSID: id, Metrics: metrics, Err: err}
	}
}

func fetchServerUpdates(id string, client *vps.Client) tea.Cmd {
	return func() tea.Msg {
		n, err := client.FetchUpdates()
		return UpdatesMsg{VPSID: id, Count: n, Err: err}
	}
}

func (m Model) server(id string) *vps.VPS {
	for i := range m.manager.Servers {
		if m.manager.Servers[i].ID == id {
			return &m.manager.Servers[i]
		}
	}
	return nil
}

// selected returns the selected server, or nil.
func (m Model) selected() *vps.VPS {
	return m.server(m.selectedID)
}

func (m Model) contentHeight() int {
	return max(0, m.height-lipgloss.Height(m.renderHeader())-lipgloss.Height(m.renderFooter()))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.activePlugin != nil {
			m.activePlugin.SetSize(m.width-4, m.contentHeight()-2)
		}

	case MetricsMsg:
		st := m.status[msg.VPSID]
		v := m.server(msg.VPSID)
		if st == nil || v == nil {
			return m, nil // server removed by a config reload
		}
		st.inFlight = false
		if msg.Err != nil {
			st.err = msg.Err
			v.Metrics = nil
			return m, nil
		}
		st.err = nil
		st.lastUpdate = time.Now()
		st.cpuHist = pushHist(st.cpuHist, msg.Metrics.CPUUsage)
		st.memHist = pushHist(st.memHist, memPct(msg.Metrics))
		v.Metrics = msg.Metrics
		return m, nil

	case UpdatesMsg:
		if st := m.status[msg.VPSID]; st != nil {
			st.updatesFlight = false
			st.updatesChecked = time.Now()
			if msg.Err == nil {
				st.updates = msg.Count
			}
		}
		return m, nil

	case ProcsMsg:
		if ps := m.procs[msg.VPSID]; ps != nil {
			ps.inFlight = false
			ps.err = msg.Err
			ps.byMem, ps.fetched = msg.ByMem, time.Now() // also on errors, so they aren't retried in a loop
			if msg.Err == nil {
				ps.list = msg.List
			}
		}
		return m, m.procCmd() // the sort order may have changed meanwhile

	case KillMsg:
		if msg.Err != nil {
			m.statusMessage = fmt.Sprintf("Error: kill -%s %d: %v", msg.Signal, msg.PID, msg.Err)
		} else {
			m.statusMessage = fmt.Sprintf("Sent %s to %d", msg.Signal, msg.PID)
			if msg.Command != "" {
				m.statusMessage += " (" + ansi.Truncate(msg.Command, 40, "…") + ")"
			}
		}
		if ps := m.procs[msg.VPSID]; ps != nil {
			ps.fetched = time.Time{} // refresh the list now to show the effect
		}
		return m, m.procCmd()

	case tickMsg:
		return m, tea.Batch(m.tick(), m.fetchMetrics(), m.procCmd())

	case tea.KeyMsg:
		// Inside a plugin all keys belong to the plugin (e.g. typing "ls" in SSH);
		// only esc / ctrl+c leave it.
		if m.state == statePlugin {
			// A plugin in a modal state (e.g. SSH reverse search) uses esc itself
			modal, _ := m.activePlugin.(interface{ Modal() bool })
			if k := msg.String(); k == "ctrl+c" || (k == "esc" && (modal == nil || !modal.Modal())) {
				m.state = stateFleet
				return m, nil
			}
			break
		}
		if m.state == stateSettings {
			return m.updateSettings(msg)
		}
		if m.state == stateProfiles {
			return m.updateProfiles(msg)
		}
		if m.filtering {
			return m.updateFilter(msg)
		}
		if m.kill != nil {
			return m.updateKill(msg)
		}
		m.statusMessage = "" // a message is shown until the next key press
		next, cmd := m.updateKeys(msg)
		// Opening the dashboard or switching server there loads that server's processes
		return next, tea.Batch(cmd, next.(Model).procCmd())
	}

	if m.state == statePlugin && m.activePlugin != nil {
		var cmd tea.Cmd
		m.activePlugin, cmd = m.activePlugin.Update(msg)
		return m, cmd
	}
	return m, nil
}

// updateFilter handles typing into the "/" filter.
func (m Model) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.filtering = false
	case tea.KeyEsc:
		m.filtering = false
		m.filter = ""
	case tea.KeyBackspace:
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		m.filter += string(msg.Runes)
	case tea.KeyUp, tea.KeyDown:
		return m.updateKeys(msg)
	case tea.KeyCtrlC:
		return m, tea.Quit
	}
	m.keepSelectionVisible()
	return m, nil
}

// keepSelectionVisible moves the selection to the first visible server if it was filtered out.
func (m *Model) keepSelectionVisible() {
	list := m.visible()
	for _, v := range list {
		if v.ID == m.selectedID {
			return
		}
	}
	if len(list) > 0 {
		m.selectedID = list[0].ID
	}
}

func (m *Model) moveSelection(delta int, vertical bool) {
	list := m.visible()
	if len(list) == 0 {
		return
	}
	cur := m.cursor(list)
	next := cur + delta
	if m.mode == modeCards && m.state == stateFleet && vertical {
		next = m.cardNeighbour(list, cur, delta, cardColumns(m.width))
	}
	m.selectedID = list[max(0, min(len(list)-1, next))].ID
}

func (m Model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		if m.state == stateFleet {
			return m, tea.Quit
		}
		m.state = stateFleet

	case "esc":
		if m.state == stateDashboard || m.state == stateTop {
			m.state = stateFleet
		} else {
			m.filter = ""
		}

	case "k":
		if m.state == stateTop || m.state == stateDashboard {
			return m, m.openKill()
		}
		m.moveSelection(-1, true) // vim-style up elsewhere
	case "up":
		m.moveSelection(-1, true)
	case "down", "j":
		m.moveSelection(1, true)
	case "left", "h":
		if m.mode == modeCards {
			m.moveSelection(-1, false)
		}
	case "right", "l":
		if m.mode == modeCards {
			m.moveSelection(1, false)
		}
	case "home":
		if list := m.visible(); len(list) > 0 {
			m.selectedID = list[0].ID
		}
	case "end":
		if list := m.visible(); len(list) > 0 {
			m.selectedID = list[len(list)-1].ID
		}

	case "/":
		m.filtering = true
		m.state = stateFleet
	case "o":
		if m.state == stateDashboard || m.state == stateTop {
			m.procsByMem = !m.procsByMem // process list: CPU <-> memory
		} else {
			m.sortBy = (m.sortBy + 1) % sortCount
		}
	case "d": // display: table or cards
		if m.mode == modeTable {
			m.mode = modeCards
		} else {
			m.mode = modeTable
		}
		m.state = stateFleet

	case "ctrl+r":
		cfg, err := config.LoadProfile(m.profile) // the active profile, not always config.yaml
		if err != nil {
			m.statusMessage = "Error reloading config: " + err.Error()
			return m, nil
		}
		for _, c := range m.clients {
			c.Close()
		}
		m.clients = map[string]*vps.Client{}
		m.manager.Servers = cfg.Servers
		m.keepSelectionVisible()
		m.statusMessage = "Config reloaded"
		return m, m.fetchMetrics()

	case "enter", "2", "tab":
		if m.state == stateDashboard && msg.String() == "tab" {
			m.state = stateFleet
		} else if m.selected() != nil {
			m.state = stateDashboard
		}
	case "1":
		m.state = stateFleet
	case "P": // profiles
		m.openProfiles()
	case "c": // settings
		m.state = stateSettings
		m.settingsForm = newSettingsForm(m.settings)
	case "t": // top: full-screen process list of the selected server
		if m.selected() != nil {
			m.state = stateTop
		}

	case "s":
		return m.openPlugin(func(c *vps.Client) plugins.Plugin { return plugins.NewSSHPlugin(c) })
	case "v":
		return m.openPlugin(func(c *vps.Client) plugins.Plugin { return plugins.NewSystemdPlugin(c) })
	case "f":
		return m.openPlugin(func(c *vps.Client) plugins.Plugin { return plugins.NewSFTPPlugin(c) })
	}
	return m, nil
}

func (m Model) openPlugin(newPlugin func(*vps.Client) plugins.Plugin) (tea.Model, tea.Cmd) {
	v := m.selected()
	if v == nil {
		return m, nil
	}
	// Plugins get their own connection so they don't interfere with the metrics polling
	client, err := vps.NewClient(*v)
	if err != nil {
		m.statusMessage = "Error: " + err.Error()
		return m, nil
	}
	m.activePlugin = newPlugin(client)
	m.state = statePlugin
	m.activePlugin.SetSize(m.width-4, m.contentHeight()-2)
	return m, m.activePlugin.Init()
}

func (m Model) View() string {
	// The first frame is drawn before the terminal size is known
	if m.width <= 0 || m.height <= 0 {
		return "Loading..."
	}
	header := m.renderHeader()
	footer := m.renderFooter()
	contentHeight := max(0, m.height-lipgloss.Height(header)-lipgloss.Height(footer))

	var content string
	switch {
	case m.state == stateFleet && m.mode == modeCards:
		content = lipgloss.NewStyle().Padding(1, 1, 0, 1).
			Render(m.renderFleet(m.width-2, contentHeight-1))

	case m.state == stateFleet:
		// Split view: table on the left, details of the selected server on the right
		const minDashWidth = 30
		dashWidth := min(48, max(minDashWidth, m.width/3))
		if m.width-dashWidth < 64 {
			dashWidth = 0 // not enough room for both
		}
		tableWidth := m.width - dashWidth - 1

		left := lipgloss.NewStyle().Padding(1, 0, 0, 1).
			Render(m.renderFleet(tableWidth, contentHeight-1))
		content = left
		if dashWidth > 0 {
			left = lipgloss.NewStyle().Width(tableWidth + 1).Render(left)
			right := lipgloss.NewStyle().Padding(1, 1, 0, 2).Width(dashWidth).
				Render(m.renderDashboard(dashWidth - 3))
			content = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
		}

	case m.state == stateProfiles:
		content = lipgloss.NewStyle().Padding(1, 2, 0, 2).Render(m.renderProfiles(m.width - 4))

	case m.state == stateSettings:
		content = lipgloss.NewStyle().Padding(1, 2, 0, 2).Render(m.renderSettings(m.width - 4))

	case m.state == stateTop:
		content = lipgloss.NewStyle().Padding(1, 1, 0, 1).Render(
			m.renderTopHeader(m.width-2) + "\n\n" + m.renderProcesses(m.width-2, contentHeight-3))

	case m.state == stateDashboard && m.width >= 100:
		// Details on the left half, a top-like process list on the right
		leftWidth := m.width / 2
		left := lipgloss.NewStyle().Padding(1, 1, 0, 1).Width(leftWidth).
			Render(m.renderDashboard(leftWidth - 2))
		right := lipgloss.NewStyle().Padding(1, 1, 0, 1).
			Render(m.renderProcesses(m.width-leftWidth-2, contentHeight-1))
		content = lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	case m.state == stateDashboard:
		content = lipgloss.NewStyle().Padding(1, 1, 0, 1).Render(m.renderDashboard(m.width - 2))
	case m.activePlugin != nil:
		content = MainStyle.Render(m.activePlugin.View())
	default:
		content = MainStyle.Render("No active plugin")
	}

	// Fill (or clip) the content area so the footer always sits on the last line
	if m.kill != nil && (m.state == stateTop || m.state == stateDashboard) {
		// The dialog sits on top; the view below is cut at the bottom
		content = lipgloss.NewStyle().Padding(1, 1, 0, 1).Render(m.renderKill(m.width-2)) + "\n" + content
	}

	content = lipgloss.NewStyle().Height(contentHeight).MaxHeight(contentHeight).MaxWidth(m.width).Render(content)

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		content,
		footer,
	)
}
