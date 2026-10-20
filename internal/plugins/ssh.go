package plugins

import (
	"fmt"
	"strings"
	"vps-fleet-tui/internal/vps"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Markers separating command output from the session state reported after it.
const (
	cwdMarker   = "\x1f__VPSTUI_CWD__"
	aliasMarker = "\x1f__VPSTUI_ALIASES__"
)

type SSHPlugin struct {
	client  *vps.Client
	input   textinput.Model
	output  viewport.Model
	history []string // output lines shown in the viewport
	width   int
	height  int
	err     error

	cwd     string // remote working directory; "" = login directory
	aliases string // `alias -p` output, replayed before each command

	// Command history (up/down)
	cmdHistory []string
	histIdx    int    // == len(cmdHistory) when not browsing
	draft      string // input saved when browsing starts

	// Reverse search (ctrl+r)
	searching   bool
	searchQuery string
	searchIdx   int // index into cmdHistory of the current match, -1 = none
}

func NewSSHPlugin(client *vps.Client) *SSHPlugin {
	ti := textinput.New()
	ti.Placeholder = "Enter command..."
	ti.Prompt = "$ "
	ti.Focus()

	vp := viewport.New(0, 0)
	vp.SetContent("Connected to " + client.VPS.Name + "\nType a command and press Enter.")
	// Only page keys scroll the output; everything else is typed into the input.
	vp.KeyMap = viewport.KeyMap{
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
	}

	return &SSHPlugin{
		client: client,
		input:  ti,
		output: vp,
	}
}

func (p *SSHPlugin) Name() string {
	return "SSH"
}

type cwdResult string

func (p *SSHPlugin) Init() tea.Cmd {
	// Fetch the login directory so the prompt shows it from the start
	fetchCwd := func() tea.Msg {
		out, err := p.client.Run("printf '%s' \"$PWD\"")
		if err != nil {
			return nil
		}
		return cwdResult(strings.TrimSpace(out))
	}
	return tea.Batch(textinput.Blink, fetchCwd)
}

// Modal reports whether the plugin wants esc for itself (to cancel a reverse search).
func (p *SSHPlugin) Modal() bool {
	return p.searching
}

func (p *SSHPlugin) Update(msg tea.Msg) (Plugin, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if p.searching {
			return p, p.updateSearch(msg)
		}
		switch msg.String() {
		case "enter":
			cmdStr := p.input.Value()
			if cmdStr != "" {
				return p, p.submit(cmdStr)
			}
		case "up":
			p.browseHistory(-1)
			return p, nil
		case "down":
			p.browseHistory(1)
			return p, nil
		case "ctrl+r":
			p.searching = true
			p.searchQuery = ""
			p.searchIdx = -1
			return p, nil
		case "tab":
			return p, p.complete(p.input.Value())
		case "pgup", "pgdown":
			p.output, cmd = p.output.Update(msg)
			return p, cmd
		}
	case commandResult:
		if msg.output != "" {
			p.history = append(p.history, msg.output)
		}
		if msg.cwd != "" {
			p.setCwd(msg.cwd)
		}
		if msg.stateOK {
			p.aliases = msg.aliases
		}
		p.updateOutput()
		return p, nil
	case completionResult:
		p.applyCompletion(msg)
		return p, nil
	case cwdResult:
		if p.cwd == "" && msg != "" {
			p.setCwd(string(msg))
		}
		return p, nil
	}

	p.input, cmd = p.input.Update(msg)
	cmds = append(cmds, cmd)

	if _, isKey := msg.(tea.KeyMsg); !isKey {
		p.output, cmd = p.output.Update(msg)
		cmds = append(cmds, cmd)
	}

	return p, tea.Batch(cmds...)
}

func (p *SSHPlugin) submit(cmdStr string) tea.Cmd {
	p.history = append(p.history, p.input.Prompt+cmdStr)
	p.updateOutput()
	p.input.SetValue("")
	if n := len(p.cmdHistory); n == 0 || p.cmdHistory[n-1] != cmdStr {
		p.cmdHistory = append(p.cmdHistory, cmdStr)
	}
	p.histIdx = len(p.cmdHistory)
	p.draft = ""
	return p.runCommand(cmdStr)
}

// browseHistory moves through previous commands like a shell's up/down arrows.
func (p *SSHPlugin) browseHistory(delta int) {
	if len(p.cmdHistory) == 0 {
		return
	}
	if p.histIdx == len(p.cmdHistory) {
		p.draft = p.input.Value()
	}
	p.histIdx = max(0, min(len(p.cmdHistory), p.histIdx+delta))
	if p.histIdx == len(p.cmdHistory) {
		p.input.SetValue(p.draft)
	} else {
		p.input.SetValue(p.cmdHistory[p.histIdx])
	}
	p.input.CursorEnd()
}

// updateSearch handles keys during a bash-style reverse-i-search.
func (p *SSHPlugin) updateSearch(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyCtrlR: // next older match
		if p.searchIdx > 0 {
			p.findMatch(p.searchIdx - 1)
		}
	case tea.KeyBackspace:
		if r := []rune(p.searchQuery); len(r) > 0 {
			p.searchQuery = string(r[:len(r)-1])
			p.findMatch(len(p.cmdHistory) - 1)
		}
	case tea.KeyEsc, tea.KeyCtrlG: // cancel
		p.searching = false
	case tea.KeyEnter: // accept and run
		p.searching = false
		if p.searchIdx >= 0 {
			return p.submit(p.cmdHistory[p.searchIdx])
		}
	case tea.KeyRunes, tea.KeySpace:
		p.searchQuery += string(msg.Runes)
		if msg.Type == tea.KeySpace {
			p.searchQuery += " "
		}
		p.findMatch(len(p.cmdHistory) - 1)
	default: // any other key (arrows, tab, …) accepts the match for editing
		p.searching = false
		if p.searchIdx >= 0 {
			p.input.SetValue(p.cmdHistory[p.searchIdx])
			p.input.CursorEnd()
		}
	}
	return nil
}

// findMatch searches backwards from index `from` for a command containing the query.
func (p *SSHPlugin) findMatch(from int) {
	for i := min(from, len(p.cmdHistory)-1); i >= 0; i-- {
		if strings.Contains(p.cmdHistory[i], p.searchQuery) {
			p.searchIdx = i
			return
		}
	}
	if p.searchQuery == "" {
		p.searchIdx = -1
	}
	// no match: keep the previous one, like bash
}

type commandResult struct {
	output  string
	cwd     string
	aliases string
	stateOK bool // cwd/aliases were reported (false if the command called exit)
	err     error
}

func (p *SSHPlugin) runCommand(cmd string) tea.Cmd {
	script := buildScript(p.cwd, p.aliases, cmd)
	return func() tea.Msg {
		out, err := p.client.Run(script)
		res := parseScriptOutput(out)
		res.err = err
		if err != nil {
			res.output = strings.TrimLeft(res.output+"\nError: "+err.Error(), "\n")
		}
		return res
	}
}

// buildScript wraps a command so the shell state survives across sessions: each
// command runs in a new SSH session, so the working directory and aliases are
// restored first and reported back afterwards.
func buildScript(cwd, aliases, cmd string) string {
	return cdPrefix(cwd) + "\n" +
		// aliases are off in non-interactive bash; they must be defined on
		// lines before the one that uses them
		"shopt -s expand_aliases 2>/dev/null\n" +
		aliases + "\n" +
		// merge stderr into stdout so error messages stay in order with the output
		"{\n" + cmd + "\n} 2>&1\n" +
		"__s=$?; printf '\\n" + cwdMarker + "%s\\n" + aliasMarker + "\\n' \"$PWD\"; alias -p 2>/dev/null; exit $__s"
}

// parseScriptOutput splits the command output from the state reported by buildScript.
func parseScriptOutput(out string) commandResult {
	i := strings.LastIndex(out, "\n"+cwdMarker)
	if i < 0 {
		return commandResult{output: strings.TrimRight(out, "\n")}
	}
	res := commandResult{output: strings.TrimRight(out[:i], "\n")}
	state := out[i+1+len(cwdMarker):]
	j := strings.Index(state, "\n"+aliasMarker+"\n")
	if j < 0 {
		return res
	}
	res.cwd = state[:j]
	res.aliases = strings.TrimRight(state[j+len(aliasMarker)+2:], "\n")
	res.stateOK = true
	return res
}

type completionResult struct {
	input   string // input the completion was requested for
	dir     string // directory part of the word, as typed
	matches []string
}

// complete lists remote paths that start with the last word of the input.
func (p *SSHPlugin) complete(input string) tea.Cmd {
	word := input[wordStart(input):]
	dir, partial := "", word
	if i := strings.LastIndex(word, "/"); i >= 0 {
		dir, partial = word[:i+1], word[i+1:]
	}
	// The typed word may contain backslash escapes ("My\ Files/"); query with the literal name
	dir, partial = shellUnescape(dir), shellUnescape(partial)
	cwd := p.cwd
	return func() tea.Msg {
		cd := ""
		if dir != "" {
			cd = "cd " + shellPath(dir) + " || exit 0\n"
		}
		// Plain sh (no compgen): directories get a trailing "/"
		script := fmt.Sprintf("%s\n%sfor f in %s*; do [ -e \"$f\" ] || continue; "+
			"if [ -d \"$f\" ]; then printf '%%s/\\n' \"$f\"; else printf '%%s\\n' \"$f\"; fi; done",
			cdPrefix(cwd), cd, shQuote(partial))
		out, _ := p.client.Run(script)
		var matches []string
		for _, l := range strings.Split(out, "\n") {
			if l != "" {
				matches = append(matches, shellEscape(l))
			}
		}
		return completionResult{input: input, dir: dir, matches: matches}
	}
}

func (p *SSHPlugin) applyCompletion(r completionResult) {
	if p.input.Value() != r.input || len(r.matches) == 0 {
		return // user kept typing, or nothing to complete
	}
	prefix := r.input[:wordStart(r.input)] + shellEscape(r.dir)
	if len(r.matches) == 1 {
		m := r.matches[0]
		if !strings.HasSuffix(m, "/") {
			m += " "
		}
		p.input.SetValue(prefix + m)
		p.input.CursorEnd()
		return
	}
	common := r.matches[0]
	for _, m := range r.matches[1:] {
		for !strings.HasPrefix(m, common) {
			common = common[:len(common)-1]
		}
	}
	if completed := prefix + common; len(completed) > len(r.input) {
		p.input.SetValue(completed)
		p.input.CursorEnd()
		return
	}
	// Ambiguous and nothing more to add: list the candidates, like a second tab in bash
	p.history = append(p.history, p.input.Prompt+r.input, strings.Join(r.matches, "  "))
	p.updateOutput()
}

func (p *SSHPlugin) setCwd(cwd string) {
	p.cwd = cwd
	p.input.Prompt = cwd + " $ "
}

// cdPrefix returns a shell line that changes into cwd (no-op when unknown).
func cdPrefix(cwd string) string {
	if cwd == "" {
		return ":"
	}
	return "cd " + shQuote(cwd) + " 2>/dev/null"
}

// wordStart returns the index where the last shell word of s begins (escaped spaces don't split).
func wordStart(s string) int {
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case ' ':
			start = i + 1
		}
	}
	return start
}

// shellEscape backslash-escapes characters the shell would otherwise interpret, as bash completion does.
func shellEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(" \t'\"\\$`&;|()<>*?[]#!{}", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func shellUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellPath quotes a path but keeps a leading ~ expandable.
func shellPath(s string) string {
	if s == "~" || strings.HasPrefix(s, "~/") {
		return "\"$HOME\"" + shQuote(s[1:])
	}
	return shQuote(s)
}

func (p *SSHPlugin) updateOutput() {
	p.output.SetContent(strings.Join(p.history, "\n"))
	p.output.GotoBottom()
}

func (p *SSHPlugin) View() string {
	prompt := p.input.View()
	if p.searching {
		match := ""
		if p.searchIdx >= 0 {
			match = p.cmdHistory[p.searchIdx]
		}
		prompt = fmt.Sprintf("(reverse-i-search)`%s': %s", p.searchQuery, match)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		p.output.View(),
		"\n",
		prompt,
	)
}

func (p *SSHPlugin) SetSize(width, height int) {
	p.width = width
	p.height = height
	p.output.Width = width
	p.output.Height = height - 3
	p.input.Width = width
}
