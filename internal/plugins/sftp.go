package plugins

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"vps-fleet-tui/internal/theme"
	"vps-fleet-tui/internal/vps"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const viewLimit = 64 * 1024 // bytes shown when viewing a file

const (
	paneLocal = iota
	paneRemote
)

// SFTPPlugin is a two-pane file manager: local files on the left, the server on
// the right. It runs shell commands over SSH (find, head, cat), so it works on
// servers without an SFTP subsystem.
type SFTPPlugin struct {
	client        *vps.Client
	panes         [2]*pane
	active        int
	showHidden    bool
	width         int
	height        int
	statusMessage string

	viewing   bool
	viewer    viewport.Model
	viewTitle string

	confirm func() tea.Cmd // action waiting for a y/n answer (overwrite, delete)
	current *transfer      // running transfer, shown as a progress bar
	prompt  *inputPrompt   // open text input (chmod, chown, chgrp)
}

// pane is one side of the file manager.
type pane struct {
	remote        bool
	cwd           string // "" until the first listing (starts in the home directory)
	entries       []fileEntry
	table         table.Model
	wide          bool   // show the MODE column
	pendingSelect string // entry to select after the next listing
	filter        string // "/" filter on entry names, cleared when the folder changes
}

type fileEntry struct {
	name     string
	target   string // symlink target
	dir      bool   // directory, or symlink to one
	size     int64
	mode     string
	modified string
	owner    string // server side only
	group    string // server side only
	octal    string // permission bits, e.g. "644" (server side only)
}

// transfer copies one file from one pane's directory to the other's.
type transfer struct {
	upload bool
	name   string
	src    string
	dst    string
	total  int64        // expected size from the listing (0 if unknown)
	done   atomic.Int64 // bytes so far, written by the transfer goroutine
	begin  time.Time
}

func NewSFTPPlugin(client *vps.Client) *SFTPPlugin {
	p := &SFTPPlugin{
		client:     client,
		showHidden: true,
		viewer:     viewport.New(0, 0), // New sets the default scroll keys
	}
	for i := range p.panes {
		p.panes[i] = &pane{remote: i == paneRemote, table: newStyledTable(fileColumns(false, false))}
	}
	p.focus(paneLocal)
	return p
}

// fileColumns returns the pane columns; MODE (and OWNER on the server side) only when wide.
func fileColumns(wide, remote bool) []table.Column {
	cols := []table.Column{{Title: "NAME", Width: 20}, {Title: "SIZE", Width: 9}}
	if wide {
		cols = append(cols, table.Column{Title: "MODE", Width: 10})
		if remote {
			cols = append(cols, table.Column{Title: "OWNER", Width: 13})
		}
	}
	return append(cols, table.Column{Title: "MODIFIED", Width: 16})
}

func (p *SFTPPlugin) Name() string {
	return "SFTP"
}

func (p *SFTPPlugin) Init() tea.Cmd {
	return tea.Batch(p.list(paneLocal, ""), p.list(paneRemote, ""))
}

// Modal reports whether the plugin wants esc for itself: to close the viewer, a
// confirmation or a prompt, or to clear the active pane's filter.
func (p *SFTPPlugin) Modal() bool {
	return p.viewing || p.confirm != nil || p.prompt != nil || p.panes[p.active].filter != ""
}

// inputPrompt is a one-line text input shown in the status area.
type inputPrompt struct {
	label    string
	input    textinput.Model
	submit   func(value string) tea.Cmd
	onChange func(value string) tea.Cmd // optional: live updates while typing
	onCancel func() tea.Cmd             // optional: undo live updates on esc
	complete func(value string) tea.Cmd // optional: tab completion
	info     string                     // e.g. completion candidates, shown after the input
	invalid  bool                       // typed folder doesn't exist: shown in red
}

func newPromptInput(value string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.SetValue(value)
	ti.CursorEnd()
	ti.Focus()
	return ti
}

type attrMsg struct {
	pane int
	name string
	what string // e.g. "Changed mode of x to 755"
	err  error
}

// askAttr opens a prompt to change the mode, owner or group of the selected server entry.
func (p *SFTPPlugin) askAttr(command string) {
	pn := p.panes[p.active]
	if !pn.remote {
		p.statusMessage = "Error: " + command + " only works on the server side (press tab)"
		return
	}
	e, ok := pn.selected(p.showHidden)
	if !ok || e.name == ".." {
		return
	}
	var label, current, noun string
	switch command {
	case "chmod":
		label, current, noun = "Mode", e.octal, "mode" // octal like 644, or symbolic like u+x
	case "chown":
		label, current, noun = "Owner", e.owner, "owner" // user, or user:group
	case "chgrp":
		label, current, noun = "Group", e.group, "group"
	}
	ti := textinput.New()
	ti.Prompt = ""
	ti.SetValue(current)
	ti.CursorEnd()
	ti.Focus()
	full := pn.join(e.name)
	which := p.active
	p.prompt = &inputPrompt{
		label: fmt.Sprintf("%s %s: %s", command, e.name, label),
		input: ti,
		submit: func(value string) tea.Cmd {
			value = strings.TrimSpace(value)
			if value == "" || value == current {
				return nil
			}
			return func() tea.Msg {
				// "--" so values like "-x" aren't taken as options; everything is quoted
				out, err := p.client.Run(fmt.Sprintf("%s -- %s %s", command, vps.ShellQuote(value), vps.ShellQuote(full)))
				if err != nil {
					err = fmt.Errorf("%s", strings.TrimSpace(out+" "+err.Error()))
				}
				return attrMsg{pane: which, name: e.name, what: fmt.Sprintf("Changed %s of %s to %s", noun, e.name, value), err: err}
			}
		},
	}
}

func (p *SFTPPlugin) focus(i int) {
	p.active = i
	for j, pn := range p.panes {
		setTableFocus(&pn.table, j == i)
	}
}

// --- listing -------------------------------------------------------------------

type listingMsg struct {
	pane       int
	cwd        string
	entries    []fileEntry
	selectName string // set when "go to path" pointed at a file: select it
}

// --- go to path / completion --------------------------------------------------------

// remoteShellPath turns user input into a shell word for the server: "~" expands to
// $HOME, relative paths are relative to the pane's folder, everything else is quoted.
func (pn *pane) remoteShellPath(input string) string {
	switch {
	case input == "":
		return vps.ShellQuote(pn.cwd)
	case input == "~":
		return `"$HOME"`
	case strings.HasPrefix(input, "~/"):
		return `"$HOME"` + vps.ShellQuote(input[1:])
	case strings.HasPrefix(input, "/"):
		return vps.ShellQuote(input)
	}
	return vps.ShellQuote(path.Join(pn.cwd, input))
}

var windowsEnvVar = regexp.MustCompile(`%([A-Za-z0-9_()]+)%`)

// expandLocal expands ~, $VAR / %VAR% and makes relative paths relative to the pane's folder.
func (pn *pane) expandLocal(input string) string {
	s := windowsEnvVar.ReplaceAllStringFunc(input, func(m string) string {
		if v, ok := os.LookupEnv(m[1 : len(m)-1]); ok {
			return v
		}
		return m
	})
	s = os.ExpandEnv(s)
	if s == "~" || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			s = home + s[1:]
		}
	}
	if s == "" {
		return pn.cwd
	}
	if !filepath.IsAbs(s) {
		if strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\`) {
			// Rooted without a drive ("\Windows", "/tmp"): the current drive's root, as on Windows
			s = filepath.VolumeName(pn.cwd) + s
		} else {
			s = filepath.Join(pn.cwd, s)
		}
	}
	return filepath.Clean(s)
}

// gotoPath lists the folder the user typed. If it names a file, its folder is
// listed with the file selected.
func (p *SFTPPlugin) gotoPath(which int, input string) tea.Cmd {
	pn := p.panes[which]
	input = strings.TrimSpace(input)
	if which == paneLocal {
		target := pn.expandLocal(input)
		return func() tea.Msg {
			sel := ""
			if st, err := os.Stat(target); err == nil && !st.IsDir() {
				target, sel = filepath.Dir(target), filepath.Base(target)
			}
			msg := listLocal(target)
			if l, ok := msg.(listingMsg); ok {
				l.selectName = sel
				return l
			}
			return msg
		}
	}
	script := "t=" + pn.remoteShellPath(input) + `; if [ -e "$t" ] && [ ! -d "$t" ]; then ` +
		`printf 'FILE:%s\n' "${t##*/}"; t=$(dirname -- "$t"); fi; cd -- "$t" && pwd && ` + listCmd
	return func() tea.Msg {
		out, err := p.client.Run(script)
		if msg, ok := parseListing(out); ok {
			return msg
		}
		if err == nil {
			err = fmt.Errorf("unexpected output")
		}
		return fmt.Errorf("error opening %s: %s", input, strings.TrimSpace(out+" "+err.Error()))
	}
}

type completionMsg struct {
	which      int    // pane the completion is for
	input      string // prompt value the completion was computed for
	value      string // new value ("" = unchanged)
	info       string // candidates or a hint
	dir        string // folder the pane should show now ("" = stay)
	selectName string // entry to select there (the first candidate)
}

// completePath completes the last path segment of input to a folder name on the pane's side.
func (p *SFTPPlugin) completePath(which int, input string) tea.Cmd {
	pn := p.panes[which]
	cut := strings.LastIndex(input, "/")
	sep := "/"
	if which == paneLocal {
		if i := strings.LastIndex(input, `\`); i > cut {
			cut, sep = i, `\`
		} else if cut < 0 {
			sep = string(filepath.Separator)
		}
	}
	dirPart, prefix := input[:cut+1], input[cut+1:]

	return func() tea.Msg {
		var names []string
		if which == paneLocal {
			dir := pn.expandLocal(dirPart)
			if dirPart == "" {
				dir = pn.cwd
			}
			items, _ := os.ReadDir(dir)
			for _, it := range items {
				if st, err := os.Stat(filepath.Join(dir, it.Name())); err == nil && st.IsDir() {
					names = append(names, it.Name())
				}
			}
		} else {
			out, _ := p.client.Run("cd -- " + pn.remoteShellPath(dirPart) + ` 2>/dev/null || exit 0; ` +
				`for f in .* *; do [ "$f" != . ] && [ "$f" != .. ] && [ -d "$f" ] && printf '%s\n' "$f"; done`)
			for _, l := range strings.Split(out, "\n") {
				if l != "" {
					names = append(names, l)
				}
			}
		}

		// Match like a shell: case-insensitive on Windows, hidden folders only for a "." prefix
		var matches []string
		for _, n := range names {
			if strings.HasPrefix(n, ".") && !strings.HasPrefix(prefix, ".") {
				continue
			}
			if strings.HasPrefix(n, prefix) || (which == paneLocal && strings.HasPrefix(strings.ToLower(n), strings.ToLower(prefix))) {
				matches = append(matches, n)
			}
		}
		sortNames(matches)
		// The pane follows the completion: it shows the folder being completed in
		msg := completionMsg{which: which, input: input, dir: dirPart}
		switch len(matches) {
		case 0:
			msg.info = "no matching folder"
			return msg
		case 1:
			msg.value = dirPart + matches[0] + sep
			msg.dir = msg.value // unique match: step into it
			return msg
		}
		msg.selectName = matches[0]
		common := matches[0]
		for _, m := range matches[1:] {
			for !strings.HasPrefix(strings.ToLower(m), strings.ToLower(common)) {
				common = common[:len(common)-1]
			}
		}
		if len(common) > len(prefix) {
			msg.value = dirPart + common
		} else {
			msg.info = strings.Join(matches, "  ")
		}
		return msg
	}
}

type pathCheckMsg struct {
	promptValue string  // prompt text the check was made for
	listing     tea.Msg // listingMsg when ok
	ok          bool
}

// checkPath shows dir in the pane if it exists; otherwise the prompt turns red.
func (p *SFTPPlugin) checkPath(which int, dir, promptValue, selectName string) tea.Cmd {
	list := p.gotoPath(which, dir)
	return func() tea.Msg {
		l, ok := list().(listingMsg)
		if ok && selectName != "" {
			l.selectName = selectName
		}
		return pathCheckMsg{promptValue: promptValue, listing: l, ok: ok}
	}
}

func sortNames(names []string) {
	slices.SortFunc(names, func(a, b string) int { return cmp.Compare(strings.ToLower(a), strings.ToLower(b)) })
}

// askPath opens the "go to" prompt with the current folder, ready for tab completion.
func (p *SFTPPlugin) askPath() {
	which := p.active
	pn := p.panes[which]
	value := pn.cwd
	sep := "/"
	if !pn.remote {
		sep = string(filepath.Separator)
	}
	if value != "" && !strings.HasSuffix(value, sep) {
		value += sep
	}
	side := "local"
	if pn.remote {
		side = "server"
	}
	original := pn.cwd
	p.prompt = &inputPrompt{
		label:    "Go to (" + side + ")",
		input:    newPromptInput(value),
		submit:   func(v string) tea.Cmd { return p.gotoPath(which, v) },
		complete: func(v string) tea.Cmd { return p.completePath(which, v) },
		// Typing a separator after a folder name opens that folder right away
		onChange: func(v string) tea.Cmd {
			if strings.HasSuffix(v, "/") || (!pn.remote && strings.HasSuffix(v, `\`)) {
				return p.checkPath(which, v, v, "")
			}
			return nil
		},
		// tab navigates the pane, so esc goes back to where it was
		onCancel: func() tea.Cmd {
			if pn.cwd == original {
				return nil
			}
			return p.list(which, original)
		},
	}
}

// askFilter opens the "/" filter for the active pane; the list updates while typing.
func (p *SFTPPlugin) askFilter() {
	pn := p.panes[p.active]
	apply := func(v string) {
		pn.filter = v
		// select the first match rather than ".."
		first := ""
		for _, e := range pn.visibleEntries(p.showHidden) {
			if e.name != ".." {
				first = e.name
				break
			}
		}
		pn.refreshRows(p.showHidden, first)
	}
	p.prompt = &inputPrompt{
		label:    "Filter",
		input:    newPromptInput(pn.filter),
		submit:   func(string) tea.Cmd { return nil }, // keep the filter
		onChange: func(v string) tea.Cmd { apply(v); return nil },
		onCancel: func() tea.Cmd { apply(""); return nil },
	}
}

// list fetches the entries of dir ("" = home directory) for a pane.
func (p *SFTPPlugin) list(which int, dir string) tea.Cmd {
	if which == paneLocal {
		return func() tea.Msg { return listLocal(dir) }
	}
	cd := "cd"
	if dir != "" {
		cd = "cd -- " + vps.ShellQuote(dir)
	}
	return func() tea.Msg {
		out, err := p.client.Run(cd + " && pwd && " + listCmd)
		if msg, ok := parseListing(out); ok {
			return msg
		}
		if dir == "" {
			dir = "home directory"
		}
		if err == nil {
			err = fmt.Errorf("unexpected output")
		}
		return fmt.Errorf("error listing %s: %s", dir, strings.TrimSpace(out+" "+err.Error()))
	}
}

// listCmd prints one line per entry, tab separated:
// type, target type, size, mode, mtime, name, link target, owner, group, octal mode
const listCmd = `find . -mindepth 1 -maxdepth 1 -printf '%y\t%Y\t%s\t%M\t%TY-%Tm-%Td %TH:%TM\t%f\t%l\t%u\t%g\t%m\n'`

// parseListing parses the output of "pwd && listCmd". ok is false if the directory wasn't reached.
func parseListing(out string) (listingMsg, bool) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	selectName := ""
	if name, ok := strings.CutPrefix(lines[0], "FILE:"); ok { // see gotoPath
		selectName, lines = name, lines[1:]
	}
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "/") {
		return listingMsg{}, false
	}
	msg := listingMsg{pane: paneRemote, cwd: lines[0], selectName: selectName}
	for _, l := range lines[1:] {
		f := strings.Split(l, "\t")
		if len(f) < 7 {
			continue // e.g. a "permission denied" line
		}
		size, _ := strconv.ParseInt(f[2], 10, 64)
		e := fileEntry{
			name:     f[5],
			target:   f[6],
			dir:      f[0] == "d" || (f[0] == "l" && f[1] == "d"),
			size:     size,
			mode:     f[3],
			modified: f[4],
		}
		if len(f) >= 10 {
			e.owner, e.group, e.octal = f[7], f[8], f[9]
		}
		msg.entries = append(msg.entries, e)
	}
	sortEntries(msg.entries)
	return msg, true
}

func listLocal(dir string) tea.Msg {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("error finding local home directory: %v", err)
		}
		dir = home
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("error listing %s: %v", dir, err)
	}
	items, err := os.ReadDir(abs)
	if err != nil {
		return fmt.Errorf("error listing %s: %v", abs, err)
	}
	msg := listingMsg{pane: paneLocal, cwd: abs}
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			continue
		}
		e := fileEntry{
			name:     it.Name(),
			dir:      info.IsDir(),
			size:     info.Size(),
			mode:     info.Mode().String(),
			modified: info.ModTime().Format("2006-01-02 15:04"),
		}
		if info.Mode()&os.ModeSymlink != 0 {
			full := filepath.Join(abs, it.Name())
			e.target, _ = os.Readlink(full)
			if st, err := os.Stat(full); err == nil && st.IsDir() {
				e.dir = true
			}
		}
		msg.entries = append(msg.entries, e)
	}
	sortEntries(msg.entries)
	return msg
}

// sortEntries puts folders first, then sorts by name.
func sortEntries(entries []fileEntry) {
	slices.SortFunc(entries, func(a, b fileEntry) int {
		if a.dir != b.dir {
			if a.dir {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.ToLower(a.name), strings.ToLower(b.name))
	})
}

// --- pane helpers -----------------------------------------------------------------

func (pn *pane) join(name string) string {
	if pn.remote {
		return path.Join(pn.cwd, name)
	}
	return filepath.Join(pn.cwd, name)
}

func (pn *pane) parent() (dir, base string, ok bool) {
	if pn.remote {
		if pn.cwd == "/" || pn.cwd == "" {
			return "", "", false
		}
		return path.Dir(pn.cwd), path.Base(pn.cwd), true
	}
	parent := filepath.Dir(pn.cwd)
	if parent == pn.cwd || pn.cwd == "" {
		return "", "", false // drive root
	}
	return parent, filepath.Base(pn.cwd), true
}

// visibleEntries returns the entries shown in the table, with ".." first.
func (pn *pane) visibleEntries(showHidden bool) []fileEntry {
	var list []fileEntry
	if _, _, ok := pn.parent(); ok {
		list = append(list, fileEntry{name: "..", dir: true})
	}
	filter := strings.ToLower(pn.filter)
	for _, e := range pn.entries {
		if !showHidden && strings.HasPrefix(e.name, ".") {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(e.name), filter) {
			continue
		}
		list = append(list, e)
	}
	return list
}

func (pn *pane) refreshRows(showHidden bool, selectName string) {
	var rows []table.Row
	cursor := 0
	for i, e := range pn.visibleEntries(showHidden) {
		name, size := e.name, humanSize(e.size)
		if e.dir {
			name += "/"
			size = "-"
		}
		if e.target != "" {
			name += " → " + e.target
		}
		if e.name == selectName {
			cursor = i
		}
		row := table.Row{name, size}
		if pn.wide {
			row = append(row, e.mode)
			if pn.remote {
				owner := ""
				if e.owner != "" {
					owner = e.owner + ":" + e.group
				}
				row = append(row, owner)
			}
		}
		rows = append(rows, append(row, e.modified))
	}
	pn.table.SetRows(rows)
	pn.table.SetCursor(cursor)
}

func (pn *pane) selected(showHidden bool) (fileEntry, bool) {
	list := pn.visibleEntries(showHidden)
	i := pn.table.Cursor()
	if i < 0 || i >= len(list) {
		return fileEntry{}, false
	}
	return list[i], true
}

func (pn *pane) has(name string) bool {
	for _, e := range pn.entries {
		if e.name == name {
			return true
		}
	}
	return false
}

func (pn *pane) setSize(width, height int, showHidden bool) {
	tableWidth := width - 2 // room for the scrollbar
	wide := width >= 72
	if wide != pn.wide || len(pn.table.Columns()) == 0 {
		// Rows must match the columns, so clear them before switching the column set
		pn.wide = wide
		sel, _ := pn.selected(showHidden)
		pn.table.SetRows(nil)
		pn.table.SetColumns(fileColumns(wide, pn.remote))
		pn.refreshRows(showHidden, sel.name)
	}
	pn.table.SetWidth(tableWidth)
	pn.table.SetHeight(height)

	// NAME takes the width the other columns leave (1 char padding per side)
	cols := pn.table.Columns()
	used := 0
	for _, c := range cols[1:] {
		used += c.Width + 2
	}
	cols[0].Width = max(8, tableWidth-used-2)
	pn.table.SetColumns(cols)
}

// --- actions ------------------------------------------------------------------------

func (p *SFTPPlugin) up() tea.Cmd {
	pn := p.panes[p.active]
	dir, base, ok := pn.parent()
	if !ok {
		return nil
	}
	pn.pendingSelect = base
	return p.list(p.active, dir)
}

func (p *SFTPPlugin) open() tea.Cmd {
	pn := p.panes[p.active]
	e, ok := pn.selected(p.showHidden)
	if !ok {
		return nil
	}
	if e.name == ".." {
		return p.up()
	}
	if e.dir {
		return p.list(p.active, pn.join(e.name))
	}
	return p.viewFile(pn, e)
}

type fileContentMsg struct {
	title   string
	content string
}

func (p *SFTPPlugin) viewFile(pn *pane, e fileEntry) tea.Cmd {
	full := pn.join(e.name)
	title := fmt.Sprintf("%s  (%s)", full, humanSize(e.size))
	if e.size > viewLimit {
		title += fmt.Sprintf(", showing the first %s", humanSize(viewLimit))
	}
	p.statusMessage = "Loading " + e.name + "..."
	return func() tea.Msg {
		var content string
		if pn.remote {
			out, err := p.client.Run(fmt.Sprintf("head -c %d -- %s", viewLimit, vps.ShellQuote(full)))
			if err != nil {
				return fmt.Errorf("error reading %s: %s", full, strings.TrimSpace(out))
			}
			content = out
		} else {
			f, err := os.Open(full)
			if err != nil {
				return fmt.Errorf("error reading %s: %v", full, err)
			}
			defer f.Close()
			b, err := io.ReadAll(io.LimitReader(f, viewLimit))
			if err != nil {
				return fmt.Errorf("error reading %s: %v", full, err)
			}
			content = string(b)
		}
		if strings.IndexByte(content, 0) >= 0 {
			action := "upload"
			if pn.remote {
				action = "download"
			}
			return fileContentMsg{title: title, content: "Binary file, close this view and press t to " + action + " it."}
		}
		return fileContentMsg{title: title, content: strings.ReplaceAll(content, "\t", "    ")}
	}
}

// copySelected copies the selected file to the other pane's directory, asking first
// if that would overwrite something.
func (p *SFTPPlugin) copySelected() tea.Cmd {
	src, dst := p.panes[p.active], p.panes[1-p.active]
	e, ok := src.selected(p.showHidden)
	if !ok || e.name == ".." {
		return nil
	}
	if e.dir {
		p.statusMessage = "Error: copying folders is not supported yet, only files"
		return nil
	}
	if dst.cwd == "" {
		p.statusMessage = "Error: the other side has not been listed yet"
		return nil
	}
	t := &transfer{upload: !src.remote, name: e.name, src: src.join(e.name), dst: dst.join(e.name), total: e.size}
	if dst.has(e.name) {
		p.confirm = func() tea.Cmd { return p.start(t) }
		where := "locally"
		if t.upload {
			where = "on the server"
		}
		p.statusMessage = fmt.Sprintf("%s already exists %s. Overwrite? (y/n)", e.name, where)
		return nil
	}
	return p.start(t)
}

// askDelete asks for confirmation before deleting the selected file or empty folder.
func (p *SFTPPlugin) askDelete() {
	pn := p.panes[p.active]
	e, ok := pn.selected(p.showHidden)
	if !ok || e.name == ".." {
		return
	}
	where := "locally"
	if pn.remote {
		where = "on the server"
	}
	what := e.name
	if e.dir && e.target == "" {
		what += "/ (only if empty)"
	}
	p.statusMessage = fmt.Sprintf("Delete %s %s? (y/n)", what, where)
	p.confirm = func() tea.Cmd { return p.delete(p.active, e) }
}

type deleteMsg struct {
	pane int
	name string
	next string // entry to select afterwards
	err  error
}

// delete removes a file, a symlink, or an empty folder. Folders with content are
// refused on purpose, so a single key press can't wipe a whole tree.
func (p *SFTPPlugin) delete(which int, e fileEntry) tea.Cmd {
	pn := p.panes[which]
	full := pn.join(e.name)

	// Select the neighbour after deleting, so the cursor stays in place
	list := pn.visibleEntries(p.showHidden)
	next := ""
	if i := pn.table.Cursor(); i+1 < len(list) {
		next = list[i+1].name
	} else if i > 0 {
		next = list[i-1].name
	}

	p.statusMessage = "Deleting " + e.name + "..."
	isDir := e.dir && e.target == "" // a symlink to a folder is removed like a file
	return func() tea.Msg {
		var err error
		if pn.remote {
			cmd := "rm -f -- " + vps.ShellQuote(full)
			if isDir {
				cmd = "rmdir -- " + vps.ShellQuote(full)
			}
			if out, rerr := p.client.Run(cmd); rerr != nil {
				err = fmt.Errorf("%s", strings.TrimSpace(out+" "+rerr.Error()))
			}
		} else {
			err = os.Remove(full) // refuses non-empty folders
		}
		return deleteMsg{pane: which, name: e.name, next: next, err: err}
	}
}

type transferMsg struct {
	t       *transfer
	size    int64
	elapsed time.Duration
	err     error
}

type progressTickMsg struct{}

// progressTick redraws the progress bar while a transfer runs.
func progressTick() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return progressTickMsg{} })
}

func (p *SFTPPlugin) start(t *transfer) tea.Cmd {
	if p.current != nil {
		p.statusMessage = "Error: wait for the current transfer to finish"
		return nil
	}
	t.begin = time.Now()
	p.current = t
	p.statusMessage = ""
	run := func() tea.Msg {
		var err error
		if t.upload {
			err = p.upload(t)
		} else {
			err = p.download(t)
		}
		return transferMsg{t: t, size: t.done.Load(), elapsed: time.Since(t.begin), err: err}
	}
	return tea.Batch(run, progressTick())
}

func (p *SFTPPlugin) upload(t *transfer) error {
	f, err := os.Open(t.src)
	if err != nil {
		return err
	}
	defer f.Close()
	return p.client.Upload(t.dst, &countingReader{r: f, n: &t.done})
}

// download writes to a ".part" file first and renames it when complete,
// so an aborted download never leaves a truncated file under the real name.
func (p *SFTPPlugin) download(t *transfer) error {
	part := t.dst + ".vps-fleet-part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	err = p.client.Download(t.src, &countingWriter{w: f, n: &t.done})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(part, t.dst)
	}
	if err != nil {
		os.Remove(part)
	}
	return err
}

// progressLine renders e.g.
// "Downloading x.bin  ██████░░░░  58%  9.9 MB / 17.1 MB  2.8 MB/s  3s left".
func (p *SFTPPlugin) progressLine(t *transfer) string {
	verb := "Downloading"
	if t.upload {
		verb = "Uploading"
	}
	done := t.done.Load()
	elapsed := time.Since(t.begin).Seconds()
	rate := 0.0
	if elapsed > 0 {
		rate = float64(done) / elapsed
	}
	info := fmt.Sprintf("%s / %s  %s/s", humanSize(done), humanSize(t.total), humanSize(int64(rate)))
	pct := 0.0
	if t.total > 0 {
		pct = min(1, float64(done)/float64(t.total))
		if rate > 0 && done < t.total {
			left := time.Duration(float64(t.total-done)/rate) * time.Second
			info += "  " + left.Round(time.Second).String() + " left"
		}
	}
	name := ansi.Truncate(t.name, 30, "…")
	barWidth := max(10, min(40, p.width-lipgloss.Width(verb+name+info)-12))
	bar := progress.New(progress.WithGradient(theme.Gradient()), progress.WithoutPercentage(), progress.WithWidth(barWidth))
	return fmt.Sprintf("%s %s  %s %3.0f%%  %s", verb, name, bar.ViewAs(pct), pct*100, info)
}

// statusArea shows the running transfer's progress, or the last status message.
func (p *SFTPPlugin) statusArea() string {
	if p.prompt != nil {
		keys := "   (enter) apply  (esc) cancel"
		if p.prompt.complete != nil {
			keys = "   (tab) complete" + keys
		}
		in := p.prompt.input
		if p.prompt.invalid {
			in.TextStyle = lipgloss.NewStyle().Foreground(red)
		}
		line := lipgloss.NewStyle().Foreground(cyan).Render(p.prompt.label+": ") + in.View()
		if p.prompt.info != "" {
			keys = "   " + p.prompt.info // completion candidates replace the key hints
		}
		return line + hint(ansi.Truncate(keys, max(0, p.width-lipgloss.Width(line)), "…"))
	}
	if p.current != nil {
		return p.progressLine(p.current)
	}
	return statusLine(p.statusMessage)
}

// countingWriter/countingReader add the bytes they pass through to n, which the
// UI reads concurrently for the progress bar.
type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n.Add(int64(n))
	return n, err
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n.Add(int64(n))
	return n, err
}

// --- update / view ------------------------------------------------------------------

func (p *SFTPPlugin) Update(msg tea.Msg) (Plugin, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case listingMsg:
		pn := p.panes[msg.pane]
		if msg.cwd != pn.cwd {
			pn.filter = "" // a filter belongs to one folder
		}
		pn.cwd, pn.entries = msg.cwd, msg.entries
		if msg.selectName != "" {
			pn.pendingSelect = msg.selectName
		}
		pn.refreshRows(p.showHidden, pn.pendingSelect)
		pn.pendingSelect = ""
		return p, nil

	case fileContentMsg:
		p.viewing = true
		p.viewTitle = msg.title
		p.viewer.SetContent(msg.content)
		p.viewer.GotoTop()
		p.statusMessage = ""
		return p, nil

	case progressTickMsg:
		if p.current != nil {
			return p, progressTick()
		}
		return p, nil

	case transferMsg:
		p.current = nil
		if msg.err != nil {
			p.statusMessage = fmt.Sprintf("Error transferring %s: %v", msg.t.name, msg.err)
			return p, nil
		}
		verb, target := "Downloaded", paneLocal
		if msg.t.upload {
			verb, target = "Uploaded", paneRemote
		}
		p.statusMessage = fmt.Sprintf("%s %s (%s in %s)",
			verb, msg.t.name, humanSize(msg.size), msg.elapsed.Round(100*time.Millisecond))
		p.panes[target].pendingSelect = msg.t.name
		return p, p.list(target, p.panes[target].cwd)

	case completionMsg:
		pr := p.prompt
		if pr == nil || pr.input.Value() != msg.input {
			return p, nil // user kept typing or closed the prompt
		}
		if msg.value != "" {
			pr.input.SetValue(msg.value)
			pr.input.CursorEnd()
		}
		pr.info = msg.info
		if msg.dir == "" {
			return p, nil
		}
		return p, p.checkPath(msg.which, msg.dir, pr.input.Value(), msg.selectName)

	case pathCheckMsg:
		if msg.ok {
			_, cmd = p.Update(msg.listing)
		}
		if pr := p.prompt; pr != nil && pr.input.Value() == msg.promptValue {
			pr.invalid = !msg.ok
		}
		return p, cmd

	case attrMsg:
		if msg.err != nil {
			p.statusMessage = "Error: " + msg.err.Error()
			return p, nil
		}
		p.statusMessage = msg.what
		p.panes[msg.pane].pendingSelect = msg.name
		return p, p.list(msg.pane, p.panes[msg.pane].cwd)

	case deleteMsg:
		if msg.err != nil {
			p.statusMessage = fmt.Sprintf("Error deleting %s: %v", msg.name, msg.err)
			return p, nil
		}
		p.statusMessage = "Deleted " + msg.name
		p.panes[msg.pane].pendingSelect = msg.next
		return p, p.list(msg.pane, p.panes[msg.pane].cwd)

	case error:
		p.statusMessage = msg.Error()
		return p, nil

	case tea.KeyMsg:
		if p.prompt != nil {
			pr := p.prompt
			switch msg.Type {
			case tea.KeyEnter:
				p.prompt = nil
				return p, pr.submit(pr.input.Value())
			case tea.KeyEsc:
				p.prompt = nil
				if pr.onCancel != nil {
					return p, pr.onCancel()
				}
				p.statusMessage = "Cancelled"
				return p, nil
			case tea.KeyTab:
				if pr.complete != nil {
					return p, pr.complete(pr.input.Value())
				}
				return p, nil
			}
			before := pr.input.Value()
			pr.input, cmd = pr.input.Update(msg)
			if v := pr.input.Value(); v != before {
				pr.info = ""
				pr.invalid = false // re-checked at the next separator or tab
				if pr.onChange != nil {
					cmd = tea.Batch(cmd, pr.onChange(v))
				}
			}
			return p, cmd
		}
		if p.confirm != nil {
			action := p.confirm
			p.confirm = nil
			if msg.String() == "y" {
				return p, action()
			}
			p.statusMessage = "Cancelled"
			return p, nil
		}
		if p.viewing {
			switch msg.String() {
			case "esc", "q", "backspace", "left":
				p.viewing = false
				return p, nil
			}
			p.viewer, cmd = p.viewer.Update(msg)
			return p, cmd
		}

		p.statusMessage = ""
		switch msg.String() {
		case "tab":
			p.focus(1 - p.active)
			return p, nil
		case "enter", "right", "l":
			return p, p.open()
		case "backspace", "left", "h":
			return p, p.up()
		case "t", "f5": // transfer to the other side
			return p, p.copySelected()
		case "x", "delete":
			p.askDelete()
			return p, nil
		case "/":
			p.askFilter()
			return p, textinput.Blink
		case "p", ".":
			p.askPath()
			return p, textinput.Blink
		case "esc": // only reaches us while a filter is active (see Modal)
			pn := p.panes[p.active]
			sel, _ := pn.selected(p.showHidden)
			pn.filter = ""
			pn.refreshRows(p.showHidden, sel.name)
			return p, nil
		case "m":
			p.askAttr("chmod")
			return p, textinput.Blink
		case "o":
			p.askAttr("chown")
			return p, textinput.Blink
		case "g":
			p.askAttr("chgrp")
			return p, textinput.Blink
		case "a": // all files: show or hide hidden ones
			p.showHidden = !p.showHidden
			for _, pn := range p.panes {
				sel, _ := pn.selected(!p.showHidden)
				pn.refreshRows(p.showHidden, sel.name)
			}
			return p, nil
		case "~":
			return p, p.list(p.active, "")
		case "r":
			return p, p.list(p.active, p.panes[p.active].cwd)
		}
	}

	if !p.viewing {
		pn := p.panes[p.active]
		pn.table, cmd = pn.table.Update(msg)
	}
	return p, cmd
}

func (p *SFTPPlugin) paneView(i, width int) string {
	pn := p.panes[i]
	label := "Local"
	if pn.remote {
		label = "Server"
	}
	title := label + "  " + pn.cwd
	if pn.filter != "" {
		title += "  · filter: " + pn.filter // not "[/var]", which reads like a path
	}
	style := lipgloss.NewStyle().Foreground(darkGray)
	marker := "  "
	if i == p.active {
		style = lipgloss.NewStyle().Foreground(lime).Bold(true)
		marker = "▌ "
	}
	// Keep the end of long paths visible: "…/var/www/site"
	title = marker + truncateLeft(title, width-2)
	return lipgloss.JoinVertical(lipgloss.Left,
		style.Render(title),
		lipgloss.NewStyle().Width(width).Render(tableWithScrollbar(pn.table)),
	)
}

func (p *SFTPPlugin) View() string {
	if p.viewing {
		title := lipgloss.NewStyle().Foreground(cyan).Bold(true)
		return lipgloss.JoinVertical(lipgloss.Left,
			title.Render(truncateLeft(p.viewTitle, p.width)),
			p.viewer.View(),
			"\n"+p.statusArea(),
			hint(fmt.Sprintf("%3.0f%%   (↑/↓/pgup/pgdn) scroll  (esc) close", p.viewer.ScrollPercent()*100)),
		)
	}
	paneWidth := (p.width - 3) / 2
	sep := lipgloss.NewStyle().Foreground(darkGray).
		Render(strings.TrimSuffix(strings.Repeat("│\n", p.panes[0].table.Height()+3), "\n"))
	hidden := "(a) hide hidden"
	if !p.showHidden {
		hidden = "(a) show hidden"
	}
	keys := []string{"(tab) switch side", "(enter) open", "(←) parent", "(t) upload", "(x) delete", "(/) filter", "(.) go to path"}
	if p.active == paneRemote {
		keys[3] = "(t) download"
		keys = append(keys, "(m) chmod", "(o) chown", "(g) chgrp")
	}
	if p.panes[p.active].filter != "" {
		keys = append([]string{"(esc) clear filter"}, keys...)
	}
	keys = append(keys, hidden, "(~) home", "(r)eload")

	line := bottomLine(positionText(p.panes[p.active].table, "entry"), keys, p.width)
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Top, p.paneView(paneLocal, paneWidth), " ", sep, " ", p.paneView(paneRemote, paneWidth)),
		"\n"+p.statusArea(),
		"\n"+line,
	)
}

func (p *SFTPPlugin) SetSize(width, height int) {
	p.width = width
	p.height = height
	paneWidth := (width - 3) / 2 // " │ " between the panes
	for _, pn := range p.panes {
		pn.setSize(paneWidth, max(1, height-6), p.showHidden)
	}
	p.viewer.Width = width
	p.viewer.Height = max(1, height-4)
}

// truncateLeft shortens s to width cells, keeping the end ("…/long/path").
func truncateLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && ansi.StringWidth("…"+string(r)) > width {
		r = r[1:]
	}
	return "…" + string(r)
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
