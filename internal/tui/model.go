package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fsnotify/fsnotify"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/handoff"
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
	stateHandoffMode
	stateHandoffWorking
	stateLLMSetup
	stateHandoffConfirm
	stateResumePicker
	stateRename
	stateDeletePicker
	stateDeleteConfirm
	stateDeleteTyping
	stateMergePicker
)

type onboardingProvider struct {
	name     string
	label    string
	selected bool
}

type syncDoneMsg struct{ results []syncsvc.SyncResult }
type backgroundThreadsMsg []domain.Thread
type hookSetupDoneMsg struct{ errs []error }
type dbChangedMsg struct{}
type errMsg struct{ err error }

// launchDoneMsg reports a tool session that threadman started has exited.
type launchDoneMsg struct {
	command string
	err     error
}
type countMsg int
type handoffWrittenMsg struct {
	sessionID string
	writer    ports.ThreadWriter
	record    domain.Handoff
	// source is the thread the record is filed under: the open conversation,
	// or the newest of the threads merged.
	source domain.Thread
	// workspace is where the target session runs.
	workspace string
	// appended is set when the messages went into an existing session.
	appended bool
	// count is how many messages were carried.
	count int
}
type handoffErrMsg struct{ err error }
type handoffLinkErrMsg struct{ err error }
type llmStatusMsg struct{ status handoff.Status }
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
	handoffTargets      []handoffTarget
	handoffChoice       handoffTarget
	handoffTarget       ports.ThreadWriter
	handoffSessionID    string
	handoffWorkspace    string
	handoffAppended     bool
	handoffCount        int
	// handoffFullSize is the size of the full-transcript block for the chosen
	// target, and handoffFullFits whether it is within the block budget.
	handoffFullSize   int
	handoffFullFits   bool
	handoffModeCursor int
	handoffMode       handoff.Mode
	handoffCancel     context.CancelFunc
	summarizer        *handoff.Summarizer
	llmStatus         handoff.Status
	llmChecking       bool
	llmSetup          llmSetupForm
	resumeCursor      int
	showArchived      bool
	renameDraft       string
	deleteTargets     []domain.Thread
	deleteSelected    []bool
	deleteDraft       string
	// mergeSelected holds the list rows picked for a merge, by groupKey.
	mergeSelected map[string]bool
	// mergeGroups are the rows being merged, oldest first.
	mergeGroups []ThreadGroup
	// merging is set from starting a merge until its result is dismissed,
	// so the shared handoff dialogs return to the list.
	merging bool
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
	live := map[string]bool{}
	for _, m := range append([]domain.Thread{g.Primary}, g.Members...) {
		if !seen[m.Provider] {
			seen[m.Provider] = true
			providers = append(providers, m.Provider)
		}
		if !m.SourceMissing {
			live[m.Provider] = true
		}
	}
	parts := make([]string, len(providers))
	for i, p := range providers {
		// A tool that no longer has any of the sessions shows dimmed.
		style := providerStyle(p)
		if !live[p] {
			style = styleArchived
		}
		parts[i] = style.Render(providerIcon(p))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

var providerLabels = domain.ProviderLabels

func New(svc *syncsvc.Service, dbPath string, writers []ports.ThreadWriter, summarizer *handoff.Summarizer) Model {
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
	return Model{svc: svc, dbChanges: ch, onboardingProviders: providers, writers: writers, summarizer: summarizer, llmChecking: true}
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
	return tea.Batch(m.checkFirstRun(), waitForDBChange(m.dbChanges), m.checkLLM())
}

// checkLLM pings the configured LLM in the background so the handoff dialog
// knows whether summaries can be offered.
func (m Model) checkLLM() tea.Cmd {
	s := m.summarizer
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return llmStatusMsg{s.Check(ctx)}
	}
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

func (m Model) syncAll() tea.Cmd {
	return func() tea.Msg {
		results := m.svc.SyncAll(context.Background())
		return syncDoneMsg{results}
	}
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
		// A new viewport starts empty, so the open conversation is laid out
		// again and the reader stays where it was.
		offset := m.viewport.YOffset
		m.viewport = viewport.New(msg.Width, msg.Height-4)
		if len(m.groupThreads) > 0 {
			m.viewport.SetContent(renderConversation(m.groupThreads))
			m.viewport.SetYOffset(offset)
		}

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
			cmds = append(cmds, m.loadGroup(m.thread, m.groupThreads, false))
		}
		return m, tea.Batch(cmds...)

	case backgroundThreadsMsg:
		m.threads = []domain.Thread(msg)
		m.groups = buildGroups(m.threads)
		m.applyFilter()
		// A handoff can add a session to the open conversation.
		if m.state == stateThread {
			for _, g := range m.groups {
				if containsThread(g.Members, m.thread.ID) && !sameMembers(g.Members, m.groupThreads) {
					return m, m.loadGroup(m.thread, g.Members, false)
				}
			}
		}

	case groupLoadedMsg:
		if !msg.initial && (m.state != stateThread || !containsThread(msg.members, m.thread.ID)) {
			return m, nil
		}
		prev := 0
		for _, t := range m.groupThreads {
			prev += len(t.Messages)
		}
		m.thread = msg.primary
		m.groupThreads = msg.members
		m.viewport.SetContent(renderConversation(msg.members))
		now := 0
		for _, t := range msg.members {
			now += len(t.Messages)
		}
		if msg.initial {
			m.state = stateThread
			m.viewport.GotoBottom()
		} else if now > prev {
			m.viewport.GotoBottom()
		}
		return m, nil

	case llmStatusMsg:
		m.llmStatus = msg.status
		m.llmChecking = false
		if m.state == stateHandoffMode {
			m.handoffModeCursor = m.firstUsableMode()
		}

	case handoffWrittenMsg:
		m.handoffCancel = nil
		m.handoffTarget = msg.writer
		m.handoffSessionID = msg.sessionID
		m.handoffWorkspace = msg.workspace
		m.handoffAppended = msg.appended
		m.handoffCount = msg.count
		m.status = ""
		m.state = stateHandoffConfirm
		if m.merging {
			m.mergeSelected = nil
		}
		return m, m.recordHandoffLink(msg.source, msg.record)

	case handoffErrMsg:
		m.handoffCancel = nil
		if errors.Is(msg.err, context.Canceled) {
			// the user pressed Esc; the state was already reset
			return m, nil
		}
		if m.merging {
			// The selection is kept so the merge can be retried.
			m.status = "merge error: " + msg.err.Error()
			m.merging = false
			m.state = stateList
			return m, nil
		}
		m.status = "handoff error: " + msg.err.Error()
		m.state = stateThread

	case handoffLinkErrMsg:
		m.status = "warning: handoff link: " + msg.err.Error()

	case managementDoneMsg:
		m.state = stateList
		return m, m.loadThreads()

	case launchDoneMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("could not run %s: %v", msg.command, msg.err)
		}
		return m, nil

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
		results := make([]syncsvc.SyncResult, 0, len(selected))
		for _, name := range selected {
			if r, ok := m.svc.SyncProvider(context.Background(), name); ok {
				results = append(results, r)
			}
		}
		return syncDoneMsg{results}
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
				return m, m.loadGroup(g.Primary, g.Members, true)
			}
		case "s":
			m.status = "syncing..."
			return m, m.syncAll()
		case "H":
			m.status = "configuring hooks..."
			return m, m.setupHooksCmd()
		case "L":
			m.openLLMSetup()
		case " ":
			if len(m.filteredGroups) == 0 {
				return m, nil
			}
			key := groupKey(m.filteredGroups[m.cursor])
			if m.mergeSelected == nil {
				m.mergeSelected = map[string]bool{}
			}
			if m.mergeSelected[key] {
				delete(m.mergeSelected, key)
			} else {
				m.mergeSelected[key] = true
			}
			if m.cursor < len(m.filteredGroups)-1 {
				m.cursor++
			}
		case "m":
			return m, m.startMerge()
		case "esc":
			m.mergeSelected = nil
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
				if m.thread.SourceMissing {
					m.status = goneMessage(m.thread)
					return m, nil
				}
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
				return m, launch(w.OpenCommand(m.thread.OriginalID, m.thread.WorkspacePath))
			}
			m.resumeCursor = 0
			m.state = stateResumePicker
		case "h":
			m.handoffTargets = m.handoffTargetsFor()
			first, ok := firstUsableTarget(m.handoffTargets)
			if !ok {
				m.status = "every tool is up to date with this conversation"
				return m, nil
			}
			m.handoffCursor = first
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
			for i := m.handoffCursor - 1; i >= 0; i-- {
				if m.handoffTargets[i].usable() {
					m.handoffCursor = i
					break
				}
			}
		case "down", "j":
			for i := m.handoffCursor + 1; i < len(m.handoffTargets); i++ {
				if m.handoffTargets[i].usable() {
					m.handoffCursor = i
					break
				}
			}
		case "enter":
			if m.handoffCursor >= len(m.handoffTargets) || !m.handoffTargets[m.handoffCursor].usable() {
				return m, nil
			}
			m.handoffChoice = m.handoffTargets[m.handoffCursor]
			m.handoffTarget = m.handoffChoice.writer
			// Sized now so the dialog can say whether the full transcript
			// fits; composing a block needs no LLM.
			m.handoffFullSize, m.handoffFullFits = handoff.FullSize(
				handoffSource(m.thread, m.handoffChoice), m.handoffChoice.session != nil)
			m.state = stateHandoffMode
			// Re-check so an LLM server started (or configured) since launch
			// is picked up.
			m.llmChecking = true
			m.handoffModeCursor = m.firstUsableMode()
			return m, m.checkLLM()
		}

	case stateLLMSetup:
		return m.handleLLMSetupKey(msg)

	case stateMergePicker:
		switch msg.String() {
		case "esc", "q":
			m.state = stateList
		case "L":
			m.openLLMSetup()
		case "up", "k":
			if m.handoffCursor > 0 {
				m.handoffCursor--
			}
		case "down", "j":
			if m.handoffCursor < len(m.targetWriters())-1 {
				m.handoffCursor++
			}
		case "enter":
			if m.llmChecking || !m.llmStatus.Ready() {
				return m, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			m.handoffCancel = cancel
			m.handoffMode = handoff.ModeDetailed
			m.merging = true
			m.status = ""
			m.state = stateHandoffWorking
			return m, m.writeMerge(ctx, m.targetWriters()[m.handoffCursor], m.mergeGroups)
		}

	case stateHandoffMode:
		switch msg.String() {
		case "esc", "q":
			m.state = stateHandoffPicker
		case "L":
			m.openLLMSetup()
		case "up", "k":
			for i := m.handoffModeCursor - 1; i >= 0; i-- {
				if m.modeUsable(handoff.Modes[i]) {
					m.handoffModeCursor = i
					break
				}
			}
		case "down", "j":
			for i := m.handoffModeCursor + 1; i < len(handoff.Modes); i++ {
				if m.modeUsable(handoff.Modes[i]) {
					m.handoffModeCursor = i
					break
				}
			}
		case "enter":
			mode := handoff.Modes[m.handoffModeCursor]
			if !m.modeUsable(mode) {
				return m, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			m.handoffCancel = cancel
			m.handoffMode = mode
			m.status = ""
			m.state = stateHandoffWorking
			return m, m.writeHandoff(ctx, m.handoffChoice, mode)
		}

	case stateHandoffWorking:
		if msg.String() == "esc" {
			if m.handoffCancel != nil {
				m.handoffCancel()
				m.handoffCancel = nil
			}
			m.status = "handoff cancelled"
			m.state = stateThread
			if m.merging {
				m.status = "merge cancelled"
				m.merging = false
				m.state = stateList
			}
		}

	case stateHandoffConfirm:
		back := stateThread
		if m.merging {
			back = stateList
		}
		switch msg.String() {
		case "y", "Y":
			m.state = back
			m.merging = false
			m.status = ""
			return m, launch(m.handoffTarget.OpenCommand(m.handoffSessionID, m.handoffWorkspace))
		case "n", "N", "esc":
			openCmd := m.handoffTarget.OpenCommand(m.handoffSessionID, m.handoffWorkspace)
			m.status = "run: " + strings.Join(openCmd.Args, " ")
			m.state = back
			m.merging = false
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
			if t.SourceMissing {
				m.status = goneMessage(t)
				m.state = stateThread
				return m, nil
			}
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
			return m, launch(w.OpenCommand(t.OriginalID, t.WorkspacePath))
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
	case stateLLMSetup:
		return m.viewLLMSetup()
	case stateHandoffMode:
		return m.viewHandoffMode()
	case stateHandoffWorking:
		return m.viewHandoffWorking()
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
	case stateMergePicker:
		return m.viewMergePicker()
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
	dialogWidth := 48

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString("  Hand off to:\n")
	inner.WriteString("\n")
	for i, t := range m.handoffTargets {
		text := fmt.Sprintf("%-12s %s", domain.ProviderLabel(t.writer.Name()), t.status())
		switch {
		case !t.usable():
			inner.WriteString(styleHint.Render("  "+text) + "\n")
		case i == m.handoffCursor:
			inner.WriteString(styleCursor.Render("▶") + " " + text + "\n")
		default:
			inner.WriteString("  " + text + "\n")
		}
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

// modeUsable reports whether a handoff mode can be chosen right now. The
// summary modes need a configured LLM that answered the last check.
func (m Model) modeUsable(mode handoff.Mode) bool {
	return !mode.NeedsLLM() || (!m.llmChecking && m.llmStatus.Ready())
}

func (m Model) firstUsableMode() int {
	for i, mode := range handoff.Modes {
		if m.modeUsable(mode) {
			return i
		}
	}
	return 0
}

// llmSummary is a one-line description of the LLM's state.
func (m Model) llmSummary() string {
	switch {
	case m.llmChecking:
		return "LLM: checking…"
	case !m.llmStatus.Configured && m.llmStatus.Err != nil:
		return "LLM: config error"
	case !m.llmStatus.Configured:
		return "LLM: not configured"
	case m.llmStatus.Err != nil:
		return "LLM: " + m.llmStatus.Label + " unavailable"
	default:
		return "LLM: " + m.llmStatus.Label + " ready"
	}
}

func (m Model) viewHandoffMode() string {
	dialogWidth := 56

	label := providerLabels[m.handoffTarget.Name()]
	if label == "" {
		label = m.handoffTarget.Name()
	}

	var inner strings.Builder
	inner.WriteString("\n")
	if m.handoffChoice.session != nil {
		inner.WriteString(fmt.Sprintf("  Catch %s up on %s as:\n", label, m.handoffChoice.status()))
	} else {
		inner.WriteString(fmt.Sprintf("  Hand off to %s as:\n", label))
	}
	inner.WriteString("\n")
	for i, mode := range handoff.Modes {
		size := ""
		if mode == handoff.ModeFull && m.handoffFullSize > 0 {
			size = styleHint.Render("  " + tokenEstimate(m.handoffFullSize))
		}
		switch {
		case !m.modeUsable(mode):
			inner.WriteString(styleHint.Render("  "+mode.Label()) + size + "\n")
		case i == m.handoffModeCursor:
			inner.WriteString(styleCursor.Render("▶") + " " + mode.Label() + size + "\n")
		default:
			inner.WriteString("  " + mode.Label() + size + "\n")
		}
	}
	if m.handoffFullSize > 0 && !m.handoffFullFits {
		// Without an LLM, the lines below already say how to set one up.
		warning := lipgloss.NewStyle().Width(dialogWidth - 4).Render(
			fmt.Sprintf("The full transcript is %s, more than many models accept in one message. A summary is recommended.",
				tokenEstimate(m.handoffFullSize)))
		inner.WriteString("\n")
		for _, line := range strings.Split(warning, "\n") {
			inner.WriteString(styleWarn.Render("  "+line) + "\n")
		}
	}
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  "+m.llmSummary()) + "\n")
	switch {
	case m.llmChecking:
	case m.llmStatus.Err != nil:
		reason := lipgloss.NewStyle().Width(dialogWidth - 4).Render(m.llmStatus.Err.Error())
		for _, line := range strings.Split(reason, "\n") {
			inner.WriteString(styleHint.Render("  "+line) + "\n")
		}
	case !m.llmStatus.Configured:
		inner.WriteString(styleHint.Render("  Summaries need an LLM: press L to set one up") + "\n")
	}
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  ↑↓ navigate  Enter select  L LLM setup  Esc back") + "\n")
	inner.WriteString("\n")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Width(dialogWidth).
		Render(inner.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) viewHandoffWorking() string {
	dialogWidth := 56

	line := "  Writing session…"
	switch {
	case m.merging:
		line = fmt.Sprintf("  Summarizing %d threads with %s…", len(m.mergeGroups), m.llmStatus.Label)
	case m.handoffMode.NeedsLLM():
		line = "  Summarizing with " + m.llmStatus.Label + "…"
	}

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString(line + "\n")
	inner.WriteString("\n")
	if m.handoffMode.NeedsLLM() {
		inner.WriteString(styleHint.Render("  This can take a minute for long threads.") + "\n")
	}
	inner.WriteString(styleHint.Render("  Esc cancel") + "\n")
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
		if t.SourceMissing {
			label += styleHint.Render("  deleted")
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
	if m.merging {
		prompt = fmt.Sprintf("  Merged %d threads into a new %s session.\n  Open it now?", m.handoffCount, label)
	} else if m.handoffAppended {
		noun := "messages"
		if m.handoffCount == 1 {
			noun = "message"
		}
		prompt = fmt.Sprintf("  Added %d %s to %s.\n  Resume it now?", m.handoffCount, noun, label)
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
	hints := styleHint.Render("[s]sync [/]search [m]merge [d]del [n]ren [a]arch " + archivedHint + " [L]llm [q]quit")
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
		picked := m.mergeSelected[groupKey(g)]
		if picked {
			cursor = styleCursor.Render(" ✓")
		}
		row := renderListRow(i+1, g, m.width)
		if i == m.cursor {
			cursor = styleCursor.Render("▶ ")
			if picked {
				cursor = styleCursor.Render("▶✓")
			}
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
		count := fmt.Sprintf("%d threads  ·  %s", len(m.filteredGroups), m.llmSummary())
		nav := "↑↓ navigate  Enter open  Space select"
		if n := len(m.mergeSelected); n > 0 {
			count = fmt.Sprintf("%d selected  ·  %s", n, m.llmSummary())
			nav = "Space select  m merge  Esc clear"
		}
		statusGap := m.width - lipgloss.Width(count) - lipgloss.Width(nav) - 2
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
	if gone := goneTools(m.groupThreads); gone != "" {
		ws += styleHint.Render("  · deleted in " + gone + ", kept by threadman")
	}
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
	title = truncateRunes(title, titleWidth)
	ws := truncateRunes(filepath.Base(g.Primary.WorkspacePath), 18)
	date := lastMessageDate(g.Primary)
	return fmt.Sprintf("%-3d  %s  %-*s  %-18s  %s", n, icons, titleWidth, title, ws, date)
}

// truncateRunes shortens s to at most n characters, ending it with "...".
// Cutting by byte could split a multi-byte character.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}

// launch runs a tool session in the terminal and reports if it can't start,
// for example because the tool isn't installed or the workspace is gone.
func launch(cmd *exec.Cmd) tea.Cmd {
	name := strings.Join(cmd.Args, " ")
	if cmd.Dir != "" {
		if info, err := os.Stat(cmd.Dir); err != nil || !info.IsDir() {
			return func() tea.Msg {
				return launchDoneMsg{command: name, err: fmt.Errorf("workspace %s no longer exists", cmd.Dir)}
			}
		}
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return launchDoneMsg{command: name, err: err}
	})
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
