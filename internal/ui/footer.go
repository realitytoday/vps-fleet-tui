package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m Model) renderFooter() string {
	// Most important keys first; the ones that don't fit are dropped
	view := "<d>:Cards"
	if m.mode == modeCards {
		view = "<d>:Table"
	}
	keys := []string{
		"<↑/↓>:Move",
		"<enter>:Details",
		"</>:Filter",
		"<o>:Sort(" + m.sortBy.String() + ")",
		view,
		"<s>:SSH",
		"<t>:Top",
		"<v>:Systemd",
		"<f>:SFTP",
		"<ctrl+r>:Reload",
		"<q>:Quit",
	}
	if m.mode == modeCards {
		keys[0] = "<←↑↓→>:Move"
	}
	switch {
	case m.kill != nil:
		keys = []string{"<tab>:Switch field", "<←/→>:Signal", "<enter>:Send", "<esc>:Cancel"}
	case m.filtering:
		keys = []string{"<type>:Filter", "<enter>:Apply", "<esc>:Clear", "<↑/↓>:Move"}
	case m.state == stateDashboard || m.state == stateTop:
		procSort := "cpu"
		if m.procsByMem {
			procSort = "mem"
		}
		other := "<t>:Top"
		if m.state == stateTop {
			other = "<enter>:Details"
		}
		keys = []string{"<esc>:Back", "<↑/↓>:Server", "<o>:Sort(" + procSort + ")", "<k>:Kill", "<s>:SSH", other, "<v>:Systemd", "<f>:SFTP"}
	case m.state == statePlugin:
		// all other keys go to the plugin (see Update)
		keys = []string{"<esc>:Back"}
		if m.activePlugin != nil && m.activePlugin.Name() == "SSH" {
			keys = append(keys, "<↑/↓>:History", "<ctrl+r>:Search", "<tab>:Complete", "<pgup/pgdn>:Scroll")
		}
	case m.state == stateProfiles:
		keys = []string{"<↑/↓>:Move", "<enter>:Switch", "<esc>:Back"}
	case m.state == stateSettings:
		keys = []string{"<↑/↓>:Setting", "<←/→>:Change", "<type>:Colour", "<esc>:Back"}
	}

	// Right side: the last status message (e.g. a kill result) if any, else the size;
	// the fleet view also puts the config key there
	sizeInfo := FooterStyle.Render(fmt.Sprintf("%dx%d", m.width, m.height))
	if m.state == stateFleet && !m.filtering {
		sizeInfo = FooterStyle.Render("<P>:Profile") + FooterStyle.Render("<c>:Config") + sizeInfo
	}
	if m.statusMessage != "" {
		style := FooterStyle.Foreground(Green)
		if strings.HasPrefix(m.statusMessage, "Error") {
			style = FooterStyle.Foreground(Red)
		}
		sizeInfo = style.Render(ansi.Truncate(m.statusMessage, max(10, m.width/2), "…"))
	}

	// FooterStyle's padding already separates keys; drop keys that would
	// not fit so the bar never wraps onto a second line.
	keybar := ""
	for _, key := range keys {
		k := FooterStyle.Render(key)
		if lipgloss.Width(keybar)+lipgloss.Width(k)+lipgloss.Width(sizeInfo) > m.width {
			break
		}
		keybar += k
	}

	bar := lipgloss.JoinHorizontal(lipgloss.Top,
		keybar,
		lipgloss.NewStyle().Width(max(0, m.width-lipgloss.Width(keybar)-lipgloss.Width(sizeInfo))).Render(""),
		sizeInfo,
	)

	footer := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(DarkGray).
		Width(m.width).
		Render(bar)

	return footer
}
