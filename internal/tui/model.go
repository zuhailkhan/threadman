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
	// toolFilter limits the list to one tool's conversations; "" shows all.
	toolFilter string
	// listTop is the first line of the list on screen.
	listTop int
	// help shows the full key list over the current screen.
	help bool
	// palette is the open command list, nil when closed.
	palette *palette
	// llmAsked is set while a check the user asked for runs, so its result
	// is reported.
	llmAsked bool
	// failure is an error shown in full in a dialog until it is dismissed.
	failure *failure
	// While a handoff or merge runs: its progress, the channel it comes on,
	// and the spinner.
	progress     handoff.Progress
	progressCh   chan handoff.Progress
	progressSeen bool
	workStart    time.Time
	spin         int
	deleteCursor int
	// onboardingHooks installs live-update hooks after the first import.
	onboardingHooks bool
	// prompts are where the user's messages start in the reader.
	prompts []promptMark
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
	return Model{svc: svc, dbChanges: ch, onboardingProviders: providers, writers: writers, summarizer: summarizer, llmChecking: true, onboardingHooks: true}
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

// reportLLMCheck shows the result of a check the user asked for: a line in
// the footer when it worked or nothing is set up, the full error otherwise.
func (m *Model) reportLLMCheck() {
	s := m.llmStatus
	switch {
	case s.Err != nil:
		m.status = m.llmSummary()
		m.failure = &failure{title: "LLM check failed", err: s.Err}
	case !s.Configured:
		m.status = "LLM: not configured, press L to set one up"
	default:
		m.status = m.llmSummary()
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
		m.viewport = viewport.New(m.readerWidth(), max(1, msg.Height-4))
		if len(m.groupThreads) > 0 {
			m.setReaderContent()
			m.viewport.SetYOffset(offset)
		}
		m.keepCursorVisible()

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
		m.keepCursorVisible()
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
			if m.onboardingHooks {
				m.status = "installing live-update hooks…"
				return m, m.setupHooksCmd()
			}
			m.state = stateList
			return m, m.loadThreads()
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
		m.keepCursorVisible()
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
		m.setReaderContent()
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
		if m.llmAsked {
			m.llmAsked = false
			m.reportLLMCheck()
		}
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
			m.failure = &failure{title: "Merge failed", err: msg.err}
			m.merging = false
			m.state = stateList
			return m, nil
		}
		m.status = "handoff error: " + msg.err.Error()
		m.failure = &failure{title: "Handoff failed", err: msg.err}
		m.state = stateThread

	case progressMsg:
		// A report from work that was cancelled or replaced is dropped.
		if msg.ch != m.progressCh || m.state != stateHandoffWorking {
			return m, nil
		}
		m.progress = msg.p
		m.progressSeen = true
		return m, listenProgress(msg.ch)

	case spinMsg:
		if m.state != stateHandoffWorking || !msg.start.Equal(m.workStart) {
			return m, nil
		}
		m.spin++
		return m, spinTick(m.workStart)

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
	if m.failure != nil {
		switch msg.String() {
		case "esc", "enter", "q":
			m.failure = nil
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}
	if m.help {
		switch msg.String() {
		case "esc", "?", "q":
			m.help = false
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}
	if m.palette != nil {
		return m.handlePaletteKey(msg)
	}
	if m.state == stateList || m.state == stateThread {
		switch msg.String() {
		case "?":
			m.help = true
			return m, nil
		case ":", "ctrl+p":
			m.openPalette()
			return m, nil
		}
	}
	switch m.state {

	case stateOnboarding:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.onboardingCursor > 0 {
				m.onboardingCursor--
			}
		case "down", "j":
			// The last line is the live-updates option.
			if m.onboardingCursor < len(m.onboardingProviders) {
				m.onboardingCursor++
			}
		case " ":
			if m.onboardingCursor == len(m.onboardingProviders) {
				m.onboardingHooks = !m.onboardingHooks
			} else {
				m.onboardingProviders[m.onboardingCursor].selected = !m.onboardingProviders[m.onboardingCursor].selected
			}
		case "enter":
			m.status = "importing…"
			return m, m.syncSelected()
		}

	case stateList:
		defer m.keepCursorVisible()
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
		case "g", "home":
			m.cursor = 0
		case "G", "end":
			m.cursor = max(0, len(m.filteredGroups)-1)
		case "tab":
			m.cycleToolFilter(1)
		case "shift+tab":
			m.cycleToolFilter(-1)
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
			switch {
			case len(m.mergeSelected) > 0:
				m.mergeSelected = nil
			case m.query != "":
				m.query = ""
				m.applyFilter()
			}
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
			m.deleteCursor = 0
			m.state = stateDeletePicker
		}

	case stateSearch:
		defer m.keepCursorVisible()
		switch msg.String() {
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down":
			if m.cursor < len(m.filteredGroups)-1 {
				m.cursor++
			}
		case "esc":
			m.state = stateList
			m.query = ""
			m.applyFilter()
			m.cursor = 0
			m.listTop = 0
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
		case "L":
			m.openLLMSetup()
		case "]":
			m.jumpPrompt(1)
		case "[":
			m.jumpPrompt(-1)
		case "g", "home":
			m.viewport.GotoTop()
		case "G", "end":
			m.viewport.GotoBottom()
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
			target, groups := m.targetWriters()[m.handoffCursor], m.mergeGroups
			return m, m.startWork(ctx, func(ctx context.Context) tea.Cmd { return m.writeMerge(ctx, target, groups) })
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
			choice := m.handoffChoice
			return m, m.startWork(ctx, func(ctx context.Context) tea.Cmd { return m.writeHandoff(ctx, choice, mode) })
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
		case "up", "k":
			if m.deleteCursor > 0 {
				m.deleteCursor--
			}
		case "down", "j":
			if m.deleteCursor < len(m.deleteTargets)-1 {
				m.deleteCursor++
			}
		case " ":
			if i := m.deleteCursor; i < len(m.deleteTargets) && m.deletable(m.deleteTargets[i]) {
				m.deleteSelected[i] = !m.deleteSelected[i]
			}
		case "a":
			for i, t := range m.deleteTargets {
				m.deleteSelected[i] = m.deletable(t)
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
			return m, m.deleteThreadCopiesCmd(selected)
		case "n", "N", "esc":
			m.deleteTargets = nil
			m.deleteSelected = nil
			m.state = stateList
		}

	case stateDeleteTyping:
		switch msg.String() {
		case "esc":
			m.state = stateList
			m.deleteDraft = ""
			m.deleteTargets = nil
			m.deleteSelected = nil
		case "enter":
			if strings.ToLower(m.deleteDraft) == "delete thread" {
				selected := m.selectedDeleteThreads()
				m.deleteDraft = ""
				m.deleteTargets = nil
				m.deleteSelected = nil
				m.state = stateList
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

func (m Model) View() string {
	if m.width == 0 {
		return "loading..."
	}
	var v string
	switch m.state {
	case stateOnboarding:
		return m.viewOnboarding()
	case stateThread:
		v = m.viewThread()
	case stateHandoffPicker, stateHandoffMode, stateHandoffWorking, stateHandoffConfirm:
		v = m.viewHandoff()
	case stateMergePicker:
		v = m.viewMergePicker()
	case stateLLMSetup:
		v = m.viewLLMSetup()
	case stateResumePicker:
		v = m.viewResumePicker()
	case stateRename:
		v = m.viewRename()
	case stateDeletePicker, stateDeleteConfirm, stateDeleteTyping:
		v = m.viewDelete()
	default:
		v = m.viewList()
	}
	if m.failure != nil {
		return m.viewFailure(v)
	}
	if m.help {
		return m.viewHelp(v)
	}
	if m.palette != nil {
		return m.viewPalette(v)
	}
	return v
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
