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
	"github.com/fsnotify/fsnotify"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/hooks"
	"github.com/zuhailkhan/threadman/internal/ports"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
)

type appState int

const (
	stateOnboarding appState = iota
	stateHookSetup
	stateList
	stateThread
	stateSearch
	stateHandoffPicker
	stateHandoffConfirm
)

type onboardingProvider struct {
	name     string
	label    string
	selected bool
}

type syncDoneMsg struct{ results []syncsvc.SyncResult }
type threadLoadedMsg struct{ thread domain.Thread }
type threadRefreshedMsg struct{ thread domain.Thread }
type backgroundThreadsMsg []domain.Thread
type hookSetupDoneMsg struct{ errs []error }
type dbChangedMsg struct{}
type errMsg struct{ err error }
type countMsg int
type handoffWrittenMsg struct {
	sessionID string
	writer    ports.ThreadWriter
}
type handoffErrMsg struct{ err error }

type Model struct {
	svc                 *syncsvc.Service
	dbChanges           <-chan struct{}
	threads             []domain.Thread
	filtered            []domain.Thread
	cursor              int
	state               appState
	query               string
	viewport            viewport.Model
	thread              domain.Thread
	status              string
	width               int
	height              int
	onboardingProviders []onboardingProvider
	onboardingCursor    int
	writers             []ports.ThreadWriter
	handoffCursor       int
	handoffTarget       ports.ThreadWriter
	handoffSessionID    string
}

var providerLabels = map[string]string{
	"claude":   "Claude Code",
	"gemini":   "Gemini CLI",
	"opencode": "OpenCode",
}

func New(svc *syncsvc.Service, dbPath string, writers []ports.ThreadWriter) Model {
	providers := make([]onboardingProvider, 0)
	for _, name := range svc.ProviderNames() {
		label := providerLabels[name]
		if label == "" {
			label = name
		}
		providers = append(providers, onboardingProvider{name: name, label: label, selected: true})
	}
	ch := make(chan struct{}, 1)
	go watchDB(dbPath, ch)
	return Model{svc: svc, dbChanges: ch, onboardingProviders: providers, writers: writers}
}

func watchDB(path string, ch chan<- struct{}) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	defer watcher.Close()

	if err := watcher.Add(filepath.Dir(path)); err != nil {
		return
	}
	base := filepath.Base(path)
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(event.Name) == base &&
				(event.Has(fsnotify.Write) || event.Has(fsnotify.Create)) {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.checkFirstRun(), waitForDBChange(m.dbChanges))
}

func waitForDBChange(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return dbChangedMsg{}
	}
}

func (m Model) checkFirstRun() tea.Cmd {
	return func() tea.Msg {
		count, err := m.svc.CountThreads(context.Background())
		if err != nil {
			return errMsg{err}
		}
		return countMsg(count)
	}
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

func (m Model) backgroundLoadThreads() tea.Cmd {
	return func() tea.Msg {
		threads, err := m.svc.ListThreads(context.Background(), "")
		if err != nil {
			return nil
		}
		return backgroundThreadsMsg(threads)
	}
}

func (m Model) refreshCurrentThread() tea.Cmd {
	id := m.thread.ID
	return func() tea.Msg {
		thread, err := m.svc.GetThread(context.Background(), id)
		if err != nil {
			return nil
		}
		return threadRefreshedMsg{thread}
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

func (m Model) writeHandoff(w ports.ThreadWriter) tea.Cmd {
	t := m.thread
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		id, err := w.WriteThread(ctx, t)
		if err != nil {
			return handoffErrMsg{err}
		}
		return handoffWrittenMsg{sessionID: id, writer: w}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport = viewport.New(msg.Width, msg.Height-4)

	case countMsg:
		if msg == 0 {
			m.state = stateOnboarding
		} else {
			m.state = stateList
			return m, m.loadThreads()
		}

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
		if m.state == stateOnboarding {
			m.state = stateHookSetup
			return m, nil
		}
		m.state = stateList
		return m, m.loadThreads()

	case hookSetupDoneMsg:
		if len(msg.errs) == 0 {
			m.status = "hooks configured for all providers"
		} else {
			msgs := make([]string, len(msg.errs))
			for i, e := range msg.errs {
				msgs[i] = e.Error()
			}
			m.status = "hook setup errors: " + strings.Join(msgs, "; ")
		}
		m.state = stateList
		return m, m.loadThreads()

	case dbChangedMsg:
		cmds := []tea.Cmd{waitForDBChange(m.dbChanges)}
		switch m.state {
		case stateList, stateSearch:
			cmds = append(cmds, m.backgroundLoadThreads())
		case stateThread:
			cmds = append(cmds, m.refreshCurrentThread())
		}
		return m, tea.Batch(cmds...)

	case backgroundThreadsMsg:
		m.threads = []domain.Thread(msg)
		m.applyFilter()

	case threadLoadedMsg:
		m.thread = msg.thread
		m.state = stateThread
		m.viewport.SetContent(renderThread(msg.thread))
		m.viewport.GotoBottom()

	case threadRefreshedMsg:
		if m.state == stateThread {
			prevCount := len(m.thread.Messages)
			m.thread = msg.thread
			m.viewport.SetContent(renderThread(msg.thread))
			if len(msg.thread.Messages) > prevCount {
				m.viewport.GotoBottom()
			}
		}
		return m, nil

	case handoffWrittenMsg:
		m.handoffTarget = msg.writer
		m.handoffSessionID = msg.sessionID
		m.status = ""
		m.state = stateHandoffConfirm

	case handoffErrMsg:
		m.status = "handoff error: " + msg.err.Error()
		m.state = stateThread

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

func (m Model) syncSelected() tea.Cmd {
	selected := make([]string, 0)
	for _, p := range m.onboardingProviders {
		if p.selected {
			selected = append(selected, p.name)
		}
	}
	return func() tea.Msg {
		results := m.svc.SyncAll(context.Background())
		filtered := results[:0]
		for _, r := range results {
			for _, name := range selected {
				if r.Provider == name {
					filtered = append(filtered, r)
					break
				}
			}
		}
		return syncDoneMsg{filtered}
	}
}

func (m Model) setupHooksCmd() tea.Cmd {
	return func() tea.Msg {
		return hookSetupDoneMsg{errs: hooks.SetupAll()}
	}
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case stateHookSetup:
		switch msg.String() {
		case "enter", "y":
			m.status = "configuring hooks..."
			return m, m.setupHooksCmd()
		case "esc", "n", "q":
			m.state = stateList
			return m, m.loadThreads()
		}

	case stateOnboarding:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.onboardingCursor > 0 {
				m.onboardingCursor--
			}
		case "down", "j":
			if m.onboardingCursor < len(m.onboardingProviders)-1 {
				m.onboardingCursor++
			}
		case " ":
			m.onboardingProviders[m.onboardingCursor].selected = !m.onboardingProviders[m.onboardingCursor].selected
		case "enter":
			m.status = "importing..."
			return m, m.syncSelected()
		}

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
		case "H":
			m.status = "configuring hooks..."
			return m, m.setupHooksCmd()
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
		case "h":
			if len(m.thread.Messages) == 0 {
				m.status = "no messages to hand off"
				return m, nil
			}
			m.handoffCursor = 0
			m.state = stateHandoffPicker
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}

	case stateHandoffPicker:
		switch msg.String() {
		case "esc", "q":
			m.state = stateThread
		case "up", "k":
			if m.handoffCursor > 0 {
				m.handoffCursor--
			}
		case "down", "j":
			if m.handoffCursor < len(m.writers)-1 {
				m.handoffCursor++
			}
		case "enter":
			if len(m.writers) == 0 {
				return m, nil
			}
			w := m.writers[m.handoffCursor]
			m.handoffTarget = w
			if w.Name() == m.thread.Provider {
				m.handoffSessionID = m.thread.OriginalID
				m.state = stateHandoffConfirm
				return m, nil
			}
			m.status = "writing session..."
			return m, m.writeHandoff(w)
		}

	case stateHandoffConfirm:
		switch msg.String() {
		case "y", "Y":
			m.state = stateThread
			m.status = ""
			cmd := m.handoffTarget.OpenCommand(m.handoffSessionID, m.thread.WorkspacePath)
			return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
				return nil
			})
		case "n", "N", "esc":
			openCmd := m.handoffTarget.OpenCommand(m.handoffSessionID, m.thread.WorkspacePath)
			m.status = "run: " + strings.Join(openCmd.Args, " ")
			m.state = stateThread
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
	case stateOnboarding:
		return m.viewOnboarding()
	case stateHookSetup:
		return m.viewHookSetup()
	case stateThread:
		return m.viewThread()
	case stateHandoffPicker:
		return m.viewHandoffPicker()
	case stateHandoffConfirm:
		return m.viewHandoffConfirm()
	default:
		return m.viewList()
	}
}

func (m Model) viewOnboarding() string {
	dialogWidth := 44

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString("  Welcome to threadman\n")
	inner.WriteString("  Select providers to import from:\n")
	inner.WriteString("\n")
	for i, p := range m.onboardingProviders {
		check := "✓"
		if !p.selected {
			check = " "
		}
		line := fmt.Sprintf("  [%s] %s", check, p.label)
		if i == m.onboardingCursor {
			line = styleCursor.Render("▶") + fmt.Sprintf(" [%s] %s", check, p.label)
		}
		inner.WriteString(line + "\n")
	}
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  ↑↓ navigate  Space toggle") + "\n")
	inner.WriteString(styleHint.Render("  Enter import  Q quit") + "\n")
	inner.WriteString("\n")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Width(dialogWidth).
		Render(inner.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) viewHookSetup() string {
	dialogWidth := 46

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString("  Set up auto-ingestion?\n")
	inner.WriteString("\n")
	inner.WriteString("  Ingest after every AI response:\n")
	inner.WriteString("\n")
	for _, p := range m.onboardingProviders {
		inner.WriteString(fmt.Sprintf("    %s\n", p.label))
	}
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  Enter / y  configure") + "\n")
	inner.WriteString(styleHint.Render("  Esc / n    skip") + "\n")
	inner.WriteString("\n")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Width(dialogWidth).
		Render(inner.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) viewHandoffPicker() string {
	dialogWidth := 40

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString("  Hand off to:\n")
	inner.WriteString("\n")
	for i, w := range m.writers {
		label := providerLabels[w.Name()]
		if label == "" {
			label = w.Name()
		}
		line := fmt.Sprintf("  %s", label)
		if i == m.handoffCursor {
			line = styleCursor.Render("▶") + fmt.Sprintf(" %s", label)
		}
		inner.WriteString(line + "\n")
	}
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  ↑↓ navigate  Enter select  Esc back") + "\n")
	inner.WriteString("\n")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Width(dialogWidth).
		Render(inner.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) viewHandoffConfirm() string {
	dialogWidth := 50

	label := providerLabels[m.handoffTarget.Name()]
	if label == "" {
		label = m.handoffTarget.Name()
	}

	var prompt string
	if m.handoffTarget.Name() == m.thread.Provider {
		prompt = fmt.Sprintf("  Resume existing session in %s?", label)
	} else {
		prompt = fmt.Sprintf("  Open in %s?", label)
	}

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString(prompt + "\n")
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  y  open    n / Esc  cancel") + "\n")
	inner.WriteString("\n")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Width(dialogWidth).
		Render(inner.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) viewList() string {
	var b strings.Builder

	// header bar
	title := styleTitle.Render("threadman")
	hints := styleHint.Render("[s] sync  [H] hooks  [/] search  [q] quit")
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
	hint := styleHint.Render("[h] handoff  [q] back")
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
