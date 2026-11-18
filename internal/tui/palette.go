package tui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// palette is the command list opened with : or ctrl+p: every action that
// works on the current screen, found by typing part of its name.
type palette struct {
	query  string
	cursor int
}

// command is one action in the palette.
type command struct {
	title string
	// key is the shortcut shown beside the title, and the key pressed to run
	// the command when it has no run of its own. "" means palette only.
	key string
	// on are the screens the command is listed on.
	on []appState
	// why says why the command can't run right now; "" means it can.
	why func(m Model) string
	// run carries out the command instead of pressing key.
	run func(m *Model) tea.Cmd
}

var (
	onList   = []appState{stateList}
	onThread = []appState{stateThread}
	onBoth   = []appState{stateList, stateThread}
)

func needRow(m Model) string {
	if len(m.filteredGroups) == 0 {
		return "no threads"
	}
	return ""
}

// commands are listed in this order when the query is empty.
var commands = []command{
	{title: "Open thread", key: "↵", on: onList, why: needRow},
	{title: "Search threads", key: "/", on: onList},
	{title: "Show the next tool's threads", key: "tab", on: onList},
	{title: "Select or unselect thread for merge", key: "space", on: onList, why: needRow},
	{title: "Merge selected threads", key: "m", on: onList, why: func(m Model) string {
		if len(m.mergeSelected) < 2 {
			return "select 2 or more"
		}
		return ""
	}},
	{title: "Clear selection", key: "esc", on: onList, why: func(m Model) string {
		if len(m.mergeSelected) == 0 {
			return "nothing selected"
		}
		return ""
	}},
	{title: "Rename thread", key: "n", on: onList, why: needRow},
	{title: "Archive or unarchive thread", key: "a", on: onList, why: needRow},
	{title: "Show or hide archived threads", key: "A", on: onList},
	{title: "Delete thread", key: "d", on: onList, why: needRow},
	{title: "Sync all tools now", key: "s", on: onList},

	{title: "Hand off to another tool", key: "h", on: onThread},
	{title: "Resume in its tool", key: "r", on: onThread},
	{title: "Next prompt", key: "]", on: onThread},
	{title: "Previous prompt", key: "[", on: onThread},
	{title: "Go to top", key: "g", on: onThread},
	{title: "Go to bottom", key: "G", on: onThread},
	{title: "Back to thread list", key: "esc", on: onThread},

	{title: "Set up LLM for summaries", key: "L", on: onBoth, run: func(m *Model) tea.Cmd {
		m.openLLMSetup()
		return nil
	}},
	{title: "Check LLM connection", on: onBoth, run: func(m *Model) tea.Cmd {
		m.llmChecking = true
		m.llmAsked = true
		m.status = "checking the LLM…"
		return m.checkLLM()
	}},
	{title: "Set up live-update hooks", key: "H", on: onList},
	{title: "Show all keys", key: "?", on: onBoth},
	{title: "Quit", key: "q", on: onList},
}

// paletteCommands are the commands for the current screen that match the
// query: titles with a word starting with it first, then titles containing
// it, then titles with its letters in order.
func (m Model) paletteCommands() []command {
	q := ""
	if m.palette != nil {
		q = strings.ToLower(strings.TrimSpace(m.palette.query))
	}
	var word, contains, letters []command
	for _, c := range commands {
		if !slices.Contains(c.on, m.state) {
			continue
		}
		t := strings.ToLower(c.title)
		switch {
		case strings.Contains(" "+t, " "+q):
			word = append(word, c)
		case strings.Contains(t, q):
			contains = append(contains, c)
		case inOrder(t, q):
			letters = append(letters, c)
		}
	}
	return append(append(word, contains...), letters...)
}

// inOrder reports whether every letter of q appears in s, in order.
func inOrder(s, q string) bool {
	for _, r := range q {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}

func (m *Model) openPalette() {
	m.palette = &palette{}
}

func (m *Model) handlePaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.palette
	list := m.paletteCommands()
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.palette = nil
	case "up", "ctrl+p":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "ctrl+n":
		if p.cursor < len(list)-1 {
			p.cursor++
		}
	case "enter":
		if p.cursor >= len(list) {
			return m, nil
		}
		c := list[p.cursor]
		if c.why != nil && c.why(*m) != "" {
			return m, nil
		}
		m.palette = nil
		if c.run != nil {
			return m, c.run(m)
		}
		return m.handleKey(keyFor(c.key))
	case "backspace":
		if r := []rune(p.query); len(r) > 0 {
			p.query = string(r[:len(r)-1])
			p.cursor = 0
		}
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			p.query += string(msg.Runes)
			p.cursor = 0
		}
	}
	return m, nil
}

// keyFor is the key press a command's shortcut stands for.
func keyFor(k string) tea.KeyMsg {
	switch k {
	case "↵":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func (m Model) viewPalette(base string) string {
	pw := dialogWidth(64, m.width)
	iw := pw - 4
	list := m.paletteCommands()
	lines := []string{
		styleFaint.Render("> ") + styleFg.Render(fit(m.palette.query, iw-4)) + styleCursor.Render("▏"),
		"",
	}

	// Show a window of the list that keeps the cursor in view.
	rows := max(3, min(len(list), m.height-10))
	top := 0
	if m.palette.cursor >= rows {
		top = m.palette.cursor - rows + 1
	}
	for i := top; i < len(list) && i < top+rows; i++ {
		c := list[i]
		why := ""
		if c.why != nil {
			why = c.why(m)
		}
		on := i == m.palette.cursor
		r := choiceRow(on)
		right := r.sub()
		switch {
		case why != "":
			right.add(styleFaint, why)
		case c.key != "":
			right.add(styleKey, " "+c.key+" ")
		}
		right.space(1)
		titleSt := styleFg
		if on {
			titleSt = styleBold
		}
		r.add(usableStyle(why == "", titleSt), fit(c.title, iw-r.width()-right.width()-1))
		lines = append(lines, spread(r, right, iw))
	}
	if len(list) == 0 {
		lines = append(lines, styleFaint.Render("  No command matches."))
	}
	lines = append(lines, "", renderKeys([]keyHint{{"type", "filter"}, {"↑↓", "choose"}, {"↵", "run"}, {"esc", "close"}}).String())
	return overlay(base, panel("Commands", styleCursor, colorFaint, lines, pw), m.width, m.height)
}
