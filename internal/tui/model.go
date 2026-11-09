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
	stateResumePicker
	stateRename
	stateDeletePicker
	stateDeleteConfirm
	stateDeleteTyping
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
type handoffLinkErrMsg struct{ err error }
type managementDoneMsg struct{}

type Model struct {
	svc                 *syncsvc.Service
	dbChanges           <-chan struct{}
	threads             []domain.Thread
	groups              []ThreadGroup
	filteredGroups      []ThreadGroup
	cursor              int
	state               appState
	query               string
	viewport            viewport.Model
	thread              domain.Thread
	groupThreads        []domain.Thread
	status              string
	width               int
	height              int
	onboardingProviders []onboardingProvider
	onboardingCursor    int
	writers             []ports.ThreadWriter
	handoffCursor       int
	handoffTargets      []ports.ThreadWriter
	handoffTarget       ports.ThreadWriter
	handoffSessionID    string
	resumeCursor        int
	showArchived        bool
	renameDraft         string
	deleteTargets       []domain.Thread
	deleteSelected      []bool
	deleteDraft         string
}

type ThreadGroup struct {
	Primary domain.Thread
	Members []domain.Thread
}

func buildGroups(threads []domain.Thread) []ThreadGroup {
	type entry struct {
		primary domain.Thread
		members []domain.Thread
		hasPrim bool
	}
	byKey := map[string]*entry{}
	order := []string{}

	for _, t := range threads {
		key := t.CanonicalID
		if key == "" {
			key = t.ID
		}
		if _, ok := byKey[key]; !ok {
			byKey[key] = &entry{}
			order = append(order, key)
		}
		e := byKey[key]
		e.members = append(e.members, t)
		if t.ID == t.CanonicalID && !e.hasPrim {
			e.primary = t
			e.hasPrim = true
		}
	}

	groups := make([]ThreadGroup, 0, len(order))
	for _, key := range order {
		e := byKey[key]
		if !e.hasPrim && len(e.members) > 0 {
			e.primary = e.members[0]
		}
		groups = append(groups, ThreadGroup{Primary: e.primary, Members: e.members})
	}
	return groups
}

func providerIcons(g ThreadGroup) string {
	seen := map[string]bool{g.Primary.Provider: true}
	providers := []string{g.Primary.Provider}
	for _, m := range g.Members {
		if !seen[m.Provider] {
			seen[m.Provider] = true
			providers = append(providers, m.Provider)
		}
	}
	parts := make([]string, len(providers))
	for i, p := range providers {
		parts[i] = providerStyle(p).Render(providerIcon(p))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

var providerLabels = map[string]string{
	"claude":   "Claude Code",
	"gemini":   "Gemini CLI",
	"opencode": "OpenCode",
	"pi":       "pi",
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
	showArchived := m.showArchived
	return func() tea.Msg {
		threads, err := m.svc.ListThreads(context.Background(), "", showArchived)
		if err != nil {
			return errMsg{err}
		}
		return threads
	}
}

func (m Model) backgroundLoadThreads() tea.Cmd {
	showArchived := m.showArchived
	return func() tea.Msg {
		threads, err := m.svc.ListThreads(context.Background(), "", showArchived)
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

func (m Model) recordHandoffLink(targetProvider, targetOriginalID string) tea.Cmd {
	source := m.thread
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.svc.RecordHandoff(ctx, source, targetProvider, targetOriginalID); err != nil {
			return handoffLinkErrMsg{err}
		}
		return nil
	}
}

func (m Model) availableHandoffTargets() []ports.ThreadWriter {
	inGroup := map[string]bool{}
	for _, t := range m.groupThreads {
		inGroup[t.Provider] = true
	}
	available := make([]ports.ThreadWriter, 0, len(m.writers))
	for _, w := range m.writers {
		if !inGroup[w.Name()] {
			available = append(available, w)
		}
	}
	return available
}

func (m Model) archiveThreadCmd(t domain.Thread) tea.Cmd {
	return func() tea.Msg {
		if err := m.svc.ArchiveThread(context.Background(), t); err != nil {
			return errMsg{err}
		}
		return managementDoneMsg{}
	}
}

func (m Model) unarchiveThreadCmd(t domain.Thread) tea.Cmd {
	return func() tea.Msg {
		if err := m.svc.UnarchiveThread(context.Background(), t); err != nil {
			return errMsg{err}
		}
		return managementDoneMsg{}
	}
}

func (m Model) renameThreadCmd(t domain.Thread, title string) tea.Cmd {
	return func() tea.Msg {
		if err := m.svc.RenameThread(context.Background(), t, title); err != nil {
			return errMsg{err}
		}
		return managementDoneMsg{}
	}
}

func (m Model) deleteThreadCopiesCmd(selected []domain.Thread) tea.Cmd {
	writers := m.writers
	return func() tea.Msg {
		if err := m.svc.DeleteThreadCopies(context.Background(), selected, writers); err != nil {
			return errMsg{err}
		}
		return managementDoneMsg{}
	}
}

func (m Model) selectedDeleteThreads() []domain.Thread {
	out := make([]domain.Thread, 0, len(m.deleteTargets))
	for i, t := range m.deleteTargets {
		if m.deleteSelected[i] {
			out = append(out, t)
		}
	}
	return out
}

func (m Model) requiresTypingConfirm(selected []domain.Thread) bool {
	if len(selected) == len(m.deleteTargets) {
		return true
	}
	for _, t := range selected {
		if t.CanonicalID != "" && t.ID == t.CanonicalID {
			return true
		}
		if t.CanonicalID == "" && len(m.deleteTargets) == 1 {
			return true
		}
	}
	return false
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
		m.groups = buildGroups(m.threads)
		m.applyFilter()
		m.status = ""

	case syncDoneMsg:
		var parts []string
		for _, r := range msg.results {
			switch {
			case r.Unavailable:
				parts = append(parts, fmt.Sprintf("[%s] not found", r.Provider))
			case r.Err != nil:
				parts = append(parts, fmt.Sprintf("[%s] failed", r.Provider))
			default:
				parts = append(parts, fmt.Sprintf("[%s] %d synced", r.Provider, r.ThreadsSaved))
			}
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
		m.groups = buildGroups(m.threads)
		m.applyFilter()
		if m.state == stateThread {
			for _, g := range m.groups {
				if g.Primary.ID == m.thread.ID {
					m.groupThreads = g.Members
					break
				}
			}
		}

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
		return m, m.recordHandoffLink(msg.writer.Name(), msg.sessionID)

	case handoffErrMsg:
		m.status = "handoff error: " + msg.err.Error()
		m.state = stateThread

	case handoffLinkErrMsg:
		m.status = "warning: handoff link: " + msg.err.Error()

	case managementDoneMsg:
		m.state = stateList
		return m, m.loadThreads()

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
			if m.cursor < len(m.filteredGroups)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.filteredGroups) > 0 {
				g := m.filteredGroups[m.cursor]
				m.groupThreads = g.Members
				return m, m.openThread(g.Primary)
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
		case "a":
			if len(m.filteredGroups) == 0 {
				return m, nil
			}
			g := m.filteredGroups[m.cursor]
			if g.Primary.Archived {
				return m, m.unarchiveThreadCmd(g.Primary)
			}
			return m, m.archiveThreadCmd(g.Primary)
		case "A":
			m.showArchived = !m.showArchived
			return m, m.loadThreads()
		case "n":
			if len(m.filteredGroups) == 0 {
				return m, nil
			}
			g := m.filteredGroups[m.cursor]
			m.renameDraft = g.Primary.CustomTitle
			if m.renameDraft == "" {
				m.renameDraft = g.Primary.Title
			}
			m.state = stateRename
		case "d":
			if len(m.filteredGroups) == 0 {
				return m, nil
			}
			g := m.filteredGroups[m.cursor]
			m.deleteTargets = g.Members
			m.deleteSelected = make([]bool, len(g.Members))
			m.deleteDraft = ""
			m.cursor = 0
			m.state = stateDeletePicker
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
		case "r":
			if len(m.groupThreads) <= 1 {
				var w ports.ThreadWriter
				for _, wr := range m.writers {
					if wr.Name() == m.thread.Provider {
						w = wr
						break
					}
				}
				if w == nil {
					m.status = "no writer for provider: " + m.thread.Provider
					return m, nil
				}
				cmd := w.OpenCommand(m.thread.OriginalID, m.thread.WorkspacePath)
				return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return nil })
			}
			m.resumeCursor = 0
			m.state = stateResumePicker
		case "h":
			if len(m.thread.Messages) == 0 {
				m.status = "no messages to hand off"
				return m, nil
			}
			m.handoffTargets = m.availableHandoffTargets()
			if len(m.handoffTargets) == 0 {
				m.status = "thread already exists in all providers"
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
			if m.handoffCursor < len(m.handoffTargets)-1 {
				m.handoffCursor++
			}
		case "enter":
			if len(m.handoffTargets) == 0 {
				return m, nil
			}
			w := m.handoffTargets[m.handoffCursor]
			m.handoffTarget = w
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

	case stateDeletePicker:
		switch msg.String() {
		case "esc", "q":
			m.state = stateList
			m.cursor = 0
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.deleteTargets)-1 {
				m.cursor++
			}
		case " ":
			if len(m.deleteTargets) > 0 {
				m.deleteSelected[m.cursor] = !m.deleteSelected[m.cursor]
			}
		case "a":
			for i := range m.deleteSelected {
				m.deleteSelected[i] = true
			}
		case "enter":
			selected := m.selectedDeleteThreads()
			if len(selected) == 0 {
				return m, nil
			}
			if m.requiresTypingConfirm(selected) {
				m.deleteDraft = ""
				m.state = stateDeleteTyping
			} else {
				m.state = stateDeleteConfirm
			}
		}

	case stateDeleteConfirm:
		switch msg.String() {
		case "y", "Y":
			selected := m.selectedDeleteThreads()
			m.deleteTargets = nil
			m.deleteSelected = nil
			m.state = stateList
			m.cursor = 0
			return m, m.deleteThreadCopiesCmd(selected)
		case "n", "N", "esc":
			m.deleteTargets = nil
			m.deleteSelected = nil
			m.state = stateList
			m.cursor = 0
		}

	case stateDeleteTyping:
		switch msg.String() {
		case "esc":
			m.state = stateList
			m.deleteDraft = ""
			m.deleteTargets = nil
			m.deleteSelected = nil
			m.cursor = 0
		case "enter":
			if strings.ToLower(m.deleteDraft) == "delete thread" {
				selected := m.selectedDeleteThreads()
				m.deleteDraft = ""
				m.deleteTargets = nil
				m.deleteSelected = nil
				m.state = stateList
				m.cursor = 0
				return m, m.deleteThreadCopiesCmd(selected)
			}
		case "backspace":
			if len(m.deleteDraft) > 0 {
				m.deleteDraft = m.deleteDraft[:len(m.deleteDraft)-1]
			}
		default:
			if len(msg.Runes) == 1 {
				m.deleteDraft += string(msg.Runes)
			}
		}

	case stateRename:
		switch msg.String() {
		case "esc":
			m.state = stateList
			m.renameDraft = ""
		case "enter":
			if len(m.filteredGroups) == 0 {
				m.state = stateList
				return m, nil
			}
			g := m.filteredGroups[m.cursor]
			draft := m.renameDraft
			m.renameDraft = ""
			m.state = stateList
			return m, m.renameThreadCmd(g.Primary, draft)
		case "backspace":
			if len(m.renameDraft) > 0 {
				m.renameDraft = m.renameDraft[:len(m.renameDraft)-1]
			}
		default:
			if len(msg.Runes) == 1 {
				m.renameDraft += string(msg.Runes)
			}
		}

	case stateResumePicker:
		switch msg.String() {
		case "esc", "q":
			m.state = stateThread
		case "up", "k":
			if m.resumeCursor > 0 {
				m.resumeCursor--
			}
		case "down", "j":
			if m.resumeCursor < len(m.groupThreads)-1 {
				m.resumeCursor++
			}
		case "enter":
			if len(m.groupThreads) == 0 {
				return m, nil
			}
			t := m.groupThreads[m.resumeCursor]
			var w ports.ThreadWriter
			for _, wr := range m.writers {
				if wr.Name() == t.Provider {
					w = wr
					break
				}
			}
			if w == nil {
				m.status = "no writer for provider: " + t.Provider
				m.state = stateThread
				return m, nil
			}
			m.state = stateThread
			cmd := w.OpenCommand(t.OriginalID, t.WorkspacePath)
			return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return nil })
		}
	}

	return m, nil
}

func (m *Model) applyFilter() {
	if m.query == "" {
		m.filteredGroups = m.groups
		return
	}
	q := strings.ToLower(m.query)
	m.filteredGroups = make([]ThreadGroup, 0)
	for _, g := range m.groups {
		if strings.Contains(strings.ToLower(g.Primary.Title), q) {
			m.filteredGroups = append(m.filteredGroups, g)
			continue
		}
		for _, mem := range g.Members {
			if strings.Contains(strings.ToLower(mem.Provider), q) {
				m.filteredGroups = append(m.filteredGroups, g)
				break
			}
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
	case stateResumePicker:
		return m.viewResumePicker()
	case stateRename:
		return m.viewRename()
	case stateDeletePicker:
		return m.viewDeletePicker()
	case stateDeleteConfirm:
		return m.viewDeleteConfirm()
	case stateDeleteTyping:
		return m.viewDeleteTyping()
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
	for i, w := range m.handoffTargets {
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

func (m Model) viewResumePicker() string {
	dialogWidth := 40

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString("  Resume in:\n")
	inner.WriteString("\n")
	for i, t := range m.groupThreads {
		label := providerLabels[t.Provider]
		if label == "" {
			label = t.Provider
		}
		line := fmt.Sprintf("  %s", label)
		if i == m.resumeCursor {
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

func (m Model) viewRename() string {
	dialogWidth := 50

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString("  Rename thread:\n")
	inner.WriteString("\n")
	inner.WriteString(fmt.Sprintf("  > %s_\n", m.renameDraft))
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  Enter save    Esc cancel") + "\n")
	inner.WriteString(styleHint.Render("  (empty string clears custom title)") + "\n")
	inner.WriteString("\n")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Width(dialogWidth).
		Render(inner.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) viewDeletePicker() string {
	dialogWidth := 44

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString("  Delete copies:\n")
	inner.WriteString("\n")
	for i, t := range m.deleteTargets {
		check := " "
		if m.deleteSelected[i] {
			check = "x"
		}
		label := providerLabels[t.Provider]
		if label == "" {
			label = t.Provider
		}
		line := fmt.Sprintf("  [%s] %s", check, label)
		if i == m.cursor {
			line = styleCursor.Render("▶") + fmt.Sprintf(" [%s] %s", check, label)
		}
		inner.WriteString(line + "\n")
	}
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  Space toggle  a all  Enter confirm") + "\n")
	inner.WriteString(styleHint.Render("  Esc cancel") + "\n")
	inner.WriteString("\n")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Width(dialogWidth).
		Render(inner.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) viewDeleteConfirm() string {
	dialogWidth := 44

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString("  Delete selected copies?\n")
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  y confirm    n / Esc cancel") + "\n")
	inner.WriteString("\n")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Width(dialogWidth).
		Render(inner.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) viewDeleteTyping() string {
	dialogWidth := 50

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString("  This is the original or last remaining copy.\n")
	inner.WriteString("  Type \"delete thread\" to confirm:\n")
	inner.WriteString("\n")
	inner.WriteString(fmt.Sprintf("  > %s_\n", m.deleteDraft))
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  Enter confirm    Esc cancel") + "\n")
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
	archivedHint := "[A]show-archived"
	if m.showArchived {
		archivedHint = "[A]hide-archived"
	}
	hints := styleHint.Render("[s]sync [/]search [d]del [n]ren [a]arch " + archivedHint + " [q]quit")
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
		titleWidth := m.width - 47
		if titleWidth < 10 {
			titleWidth = 10
		}
		b.WriteString(styleHeader.Render(fmt.Sprintf("  %-3s  %-7s  %-*s  %-18s  %s", "#", "PROV", titleWidth, "TITLE", "WORKSPACE", "DATE")) + "\n")
	}
	b.WriteString(styleSep.Render(strings.Repeat("─", m.width)) + "\n")

	// rows
	listHeight := m.height - 6
	start := 0
	if m.cursor >= listHeight {
		start = m.cursor - listHeight + 1
	}
	for i := start; i < len(m.filteredGroups) && i < start+listHeight; i++ {
		g := m.filteredGroups[i]
		cursor := "  "
		row := renderListRow(i+1, g, m.width)
		if i == m.cursor {
			cursor = styleCursor.Render("▶ ")
			row = styleSelected.Width(m.width - 2).Render(row)
		} else if g.Primary.Archived {
			row = styleArchived.Render(row)
		}
		b.WriteString(cursor + row + "\n")
	}

	// fill remaining space
	rendered := start + min(listHeight, len(m.filteredGroups)-start)
	for i := rendered; i < listHeight; i++ {
		b.WriteString("\n")
	}

	// status bar
	var statusLine string
	if m.status != "" {
		statusLine = m.status
	} else {
		count := fmt.Sprintf("%d threads", len(m.filteredGroups))
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
	var tag string
	if len(m.groupThreads) > 1 {
		tag = providerIcons(ThreadGroup{Primary: m.thread, Members: m.groupThreads})
	} else {
		tag = providerStyle(m.thread.Provider).Render("[" + m.thread.Provider + "]")
	}
	hint := styleHint.Render("[r] resume  [h] handoff  [q] back")
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

func renderListRow(n int, g ThreadGroup, width int) string {
	// fixed: cursor(2) + num(3) + gap(2) + icons(7) + gap(2) + gap(2) + ws(18) + gap(2) + date(9) = 47
	titleWidth := width - 47
	if titleWidth < 10 {
		titleWidth = 10
	}

	icons := providerIcons(g)
	iconsVisual := lipgloss.Width(icons)
	if iconsVisual < 7 {
		icons += strings.Repeat(" ", 7-iconsVisual)
	}

	title := g.Primary.Title
	if title == "" {
		title = "(untitled)"
	}
	if len(title) > titleWidth {
		title = title[:titleWidth-3] + "..."
	}
	ws := filepath.Base(g.Primary.WorkspacePath)
	if len(ws) > 18 {
		ws = ws[:15] + "..."
	}
	date := lastMessageDate(g.Primary)
	return fmt.Sprintf("%-3d  %s  %-*s  %-18s  %s", n, icons, titleWidth, title, ws, date)
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
