package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zuhailkhan/threadman/internal/domain"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
)

type appState int

const (
	stateList appState = iota
	stateThread
	stateSearch
)

type syncDoneMsg struct{ results []syncsvc.SyncResult }
type threadLoadedMsg struct{ thread domain.Thread }
type errMsg struct{ err error }

type Model struct {
	svc      *syncsvc.Service
	threads  []domain.Thread
	filtered []domain.Thread
	cursor   int
	state    appState
	query    string
	viewport viewport.Model
	thread   domain.Thread
	status   string
	width    int
	height   int
}

func New(svc *syncsvc.Service) Model {
	return Model{svc: svc}
}

func (m Model) Init() tea.Cmd {
	return m.loadThreads()
}

func (m Model) loadThreads() tea.Cmd {
	return func() tea.Msg {
		threads, err := m.svc.ListThreads(context.Background(), "")
		if err != nil {
			return errMsg{err}
		}
		return threads
	}
}

func (m Model) syncAll() tea.Cmd {
	return func() tea.Msg {
		results := m.svc.SyncAll(context.Background())
		return syncDoneMsg{results}
	}
}

func (m Model) openThread(t domain.Thread) tea.Cmd {
	return func() tea.Msg {
		full, err := m.svc.GetThread(context.Background(), t.ID)
		if err != nil {
			return errMsg{err}
		}
		return threadLoadedMsg{full}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport = viewport.New(msg.Width, msg.Height-4)

	case []domain.Thread:
		m.threads = msg
		m.applyFilter()
		m.status = ""

	case syncDoneMsg:
		var parts []string
		for _, r := range msg.results {
			parts = append(parts, fmt.Sprintf("[%s] %d synced", r.Provider, r.ThreadsSaved))
		}
		m.status = strings.Join(parts, "  ")
		return m, m.loadThreads()

	case threadLoadedMsg:
		m.thread = msg.thread
		m.state = stateThread
		m.viewport.SetContent(renderThread(msg.thread))
		m.viewport.GotoTop()

	case errMsg:
		m.status = "error: " + msg.err.Error()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.state == stateThread {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case stateList:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.filtered) > 0 {
				return m, m.openThread(m.filtered[m.cursor])
			}
		case "s":
			m.status = "syncing..."
			return m, m.syncAll()
		case "/":
			m.state = stateSearch
			m.query = ""
		}

	case stateSearch:
		switch msg.String() {
		case "esc":
			m.state = stateList
			m.query = ""
			m.applyFilter()
			m.cursor = 0
		case "enter":
			m.state = stateList
		case "backspace":
			if len(m.query) > 0 {
				m.query = m.query[:len(m.query)-1]
				m.applyFilter()
				m.cursor = 0
			}
		default:
			if len(msg.Runes) == 1 {
				m.query += string(msg.Runes)
				m.applyFilter()
				m.cursor = 0
			}
		}

	case stateThread:
		switch msg.String() {
		case "q", "esc":
			m.state = stateList
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}

	return m, nil
}

func (m *Model) applyFilter() {
	if m.query == "" {
		m.filtered = m.threads
		return
	}
	q := strings.ToLower(m.query)
	m.filtered = make([]domain.Thread, 0, len(m.threads))
	for _, t := range m.threads {
		if strings.Contains(strings.ToLower(t.Title), q) ||
			strings.Contains(strings.ToLower(t.Provider), q) {
			m.filtered = append(m.filtered, t)
		}
	}
}

func (m Model) View() string {
	if m.width == 0 {
		return "loading..."
	}
	switch m.state {
	case stateThread:
		return m.viewThread()
	default:
		return m.viewList()
	}
}

func (m Model) viewList() string {
	var b strings.Builder

	// header bar
	title := styleTitle.Render("threadman")
	hints := styleHint.Render("[s] sync  [/] search  [q] quit")
	gap := m.width - lipgloss.Width(title) - lipgloss.Width(hints)
	if gap < 1 {
		gap = 1
	}
	b.WriteString(title + strings.Repeat(" ", gap) + hints + "\n")
	b.WriteString(styleSep.Render(strings.Repeat("─", m.width)) + "\n")

	// column headers
	if m.state == stateSearch {
		b.WriteString(styleHeader.Render(fmt.Sprintf("  search: %s_", m.query)) + "\n")
	} else {
		titleWidth := m.width - 50
		if titleWidth < 10 {
			titleWidth = 10
		}
		b.WriteString(styleHeader.Render(fmt.Sprintf("  %-3s  %-10s  %-*s  %-18s  %s", "#", "PROVIDER", titleWidth, "TITLE", "WORKSPACE", "DATE")) + "\n")
	}
	b.WriteString(styleSep.Render(strings.Repeat("─", m.width)) + "\n")

	// rows
	listHeight := m.height - 6
	start := 0
	if m.cursor >= listHeight {
		start = m.cursor - listHeight + 1
	}
	for i := start; i < len(m.filtered) && i < start+listHeight; i++ {
		t := m.filtered[i]
		cursor := "  "
		row := renderListRow(i+1, t, m.width)
		if i == m.cursor {
			cursor = styleCursor.Render("▶ ")
			row = styleSelected.Width(m.width - 2).Render(row)
		}
		b.WriteString(cursor + row + "\n")
	}

	// fill remaining space
	rendered := start + min(listHeight, len(m.filtered)-start)
	for i := rendered; i < listHeight; i++ {
		b.WriteString("\n")
	}

	// status bar
	var statusLine string
	if m.status != "" {
		statusLine = m.status
	} else {
		count := fmt.Sprintf("%d threads", len(m.filtered))
		nav := "↑↓ navigate  Enter open"
		statusGap := m.width - len(count) - len(nav) - 2
		if statusGap < 1 {
			statusGap = 1
		}
		statusLine = count + strings.Repeat(" ", statusGap) + nav
	}
	b.WriteString(styleStatus.Width(m.width).Render(statusLine))

	return b.String()
}

func (m Model) viewThread() string {
	var b strings.Builder

	title := m.thread.Title
	if title == "" {
		title = "(untitled)"
	}
	tag := providerStyle(m.thread.Provider).Render("[" + m.thread.Provider + "]")
	hint := styleHint.Render("[q] back")
	ws := styleHint.Render(m.thread.WorkspacePath)
	left := tag + " " + styleTitle.Render(title) + "  " + ws
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(hint)
	if gap < 1 {
		gap = 1
	}
	b.WriteString(left + strings.Repeat(" ", gap) + hint + "\n")
	b.WriteString(styleSep.Render(strings.Repeat("─", m.width)) + "\n")

	b.WriteString(m.viewport.View())

	scroll := fmt.Sprintf("  %d%%", int(m.viewport.ScrollPercent()*100))
	b.WriteString("\n" + styleStatus.Width(m.width).Render(scroll))

	return b.String()
}

func renderListRow(n int, t domain.Thread, width int) string {
	// fixed columns: num(3) + gap(2) + provider(10) + gap(2) + gap(2) + ws(18) + gap(2) + date(9) + cursor(2) = 50
	titleWidth := width - 50
	if titleWidth < 10 {
		titleWidth = 10
	}

	tag := providerStyle(t.Provider).Render(fmt.Sprintf("%-10s", "["+t.Provider+"]"))
	title := t.Title
	if title == "" {
		title = "(untitled)"
	}
	if len(title) > titleWidth {
		title = title[:titleWidth-3] + "..."
	}
	ws := filepath.Base(t.WorkspacePath)
	if len(ws) > 18 {
		ws = ws[:15] + "..."
	}
	date := lastMessageDate(t)
	return fmt.Sprintf("%-3d  %s  %-*s  %-18s  %s", n, tag, titleWidth, title, ws, date)
}

func renderThread(t domain.Thread) string {
	var b strings.Builder
	sep := styleSep.Render(strings.Repeat("─", 80))
	for _, m := range t.Messages {
		switch m.Role {
		case domain.RoleUser:
			b.WriteString(styleYou.Render("You:") + "\n")
		case domain.RoleAssistant:
			b.WriteString(styleAssistant.Render("Assistant:") + "\n")
		default:
			b.WriteString(string(m.Role) + ":\n")
		}
		b.WriteString(m.Content + "\n")
		b.WriteString(sep + "\n")
	}
	return b.String()
}

func lastMessageDate(t domain.Thread) string {
	var last time.Time
	for _, m := range t.Messages {
		if m.Timestamp.After(last) {
			last = m.Timestamp
		}
	}
	if last.IsZero() {
		return t.CreatedAt.Format("02 Jan 06")
	}
	return last.Format("02 Jan 06")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
