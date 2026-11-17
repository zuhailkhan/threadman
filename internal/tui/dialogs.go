package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/handoff"
	"github.com/zuhailkhan/threadman/internal/ports"
)

// readOnly reports whether a tool's sessions can be read and resumed but not
// written to or deleted.
func readOnly(w ports.ThreadWriter) bool {
	ro, ok := w.(interface{ ReadOnly() bool })
	return ok && ro.ReadOnly()
}

func (m Model) writerFor(provider string) ports.ThreadWriter {
	for _, w := range m.writers {
		if w.Name() == provider {
			return w
		}
	}
	return nil
}

// deletable reports whether threadman can delete a session: a read-only tool's
// session can only go once the tool itself has removed it.
func (m Model) deletable(t domain.Thread) bool {
	if t.SourceMissing {
		return true
	}
	w := m.writerFor(t.Provider)
	return w != nil && !readOnly(w)
}

// baseFor is the screen a dialog opened from state is drawn over.
func (m Model) baseFor(state appState) string {
	switch state {
	case stateThread, stateResumePicker:
		return m.viewThread()
	case stateHandoffPicker, stateHandoffMode, stateHandoffWorking, stateHandoffConfirm:
		if m.merging {
			return m.viewList()
		}
		return m.viewThread()
	}
	return m.viewList()
}

func (m Model) dialog(title string, titleStyle lipgloss.Style, border lipgloss.TerminalColor, lines []string, width int) string {
	return overlay(m.baseFor(m.state), panel(title, titleStyle, border, lines, width), m.width, m.height)
}

// toolLabel is a tool's icon and name.
func toolLabel(r *row, provider string, usable bool) *row {
	st := providerStyle(provider)
	if !usable {
		st = styleFaint
	}
	return r.add(st, providerIcon(provider)).space(1).add(usableStyle(usable, styleFg), domain.ProviderLabel(provider))
}

func wrapStyled(text string, w int, st lipgloss.Style) []string {
	var out []string
	for _, l := range wrapText(text, w) {
		out = append(out, st.Render(l))
	}
	return out
}

// stepper shows where a handoff is: done steps ticked, the current one lit.
func stepper(current int, done bool) string {
	steps := []string{"Target", "Mode", "Open"}
	r := &row{}
	for i, s := range steps {
		if i > 0 {
			r.add(styleFaint, " ──── ")
		}
		switch {
		case i < current || done:
			r.add(styleOK, "✓ ").add(styleHint, s)
		case i == current:
			r.add(styleCursor, "● ").add(styleBold, s)
		default:
			r.add(styleFaint, "○ "+s)
		}
	}
	return r.String()
}

var modeNotes = map[handoff.Mode]string{
	handoff.ModeDetailed: "a brief of the older messages, then the last 6 word for word",
	handoff.ModeLight:    "a brief of up to 200 words, then the last 2 word for word",
	handoff.ModeFull:     "every message, unchanged",
}

func (m Model) viewHandoff() string {
	pw := dialogWidth(72, m.width)
	iw := pw - 4
	if m.merging {
		return m.viewMergeProgress(pw)
	}

	lines := []string{styleHint.Render(fit(displayTitle(m.thread), iw)), ""}
	keys := []keyHint{}
	target := ""
	if m.handoffTarget != nil {
		target = domain.ProviderLabel(m.handoffTarget.Name())
	}

	switch m.state {
	case stateHandoffPicker:
		lines = append(lines, stepper(0, false), "")
		for i, t := range m.handoffTargets {
			r := choiceRow(i == m.handoffCursor)
			toolLabel(r, t.writer.Name(), t.usable())
			right := r.sub().add(usableStyle(t.usable(), styleHint), t.status()).space(1)
			lines = append(lines, spread(r, right, iw))
		}
		lines = append(lines, "")
		lines = append(lines, wrapStyled("A tool that already has this conversation is sent only what it missed.", iw, styleFaint)...)
		keys = []keyHint{{"↑↓", "choose"}, {"↵", "next"}, {"esc", "cancel"}}

	case stateHandoffMode:
		lines = append(lines, stepper(1, false), "")
		if m.handoffChoice.session != nil {
			lines = append(lines, styleBold.Render(fmt.Sprintf("Catch %s up on %s as:", target, m.handoffChoice.status())))
		} else {
			lines = append(lines, styleBold.Render(fmt.Sprintf("Hand off to %s as:", target)))
		}
		lines = append(lines, "")
		for i, mode := range handoff.Modes {
			usable := m.modeUsable(mode)
			on := i == m.handoffModeCursor && usable
			r := choiceRow(on)
			nameSt := styleFg
			if on {
				nameSt = styleBold
			}
			r.add(usableStyle(usable, nameSt), mode.Label())
			right := r.sub()
			switch {
			case mode == handoff.ModeFull && m.handoffFullSize > 0:
				st := styleHint
				if on {
					st = styleAccent
				}
				right.add(st, tokenEstimate(m.handoffFullSize))
			case !usable:
				right.add(styleFaint, "needs an LLM")
			}
			right.space(1)
			lines = append(lines, spread(r, right, iw))
			note := newRow(on).space(4).add(usableStyle(usable, styleHint), fit(modeNotes[mode], iw-4))
			lines = append(lines, note.pad(iw))
		}
		if m.handoffFullSize > 0 && !m.handoffFullFits {
			// Without an LLM, the lines below already say how to set one up.
			lines = append(lines, "")
			lines = append(lines, wrapStyled(fmt.Sprintf(
				"The full transcript is %s, more than many models accept in one message. A summary is recommended.",
				tokenEstimate(m.handoffFullSize)), iw, styleWarn)...)
		}
		lines = append(lines, "", m.llmLine())
		switch {
		case m.llmChecking:
		case m.llmStatus.Err != nil:
			lines = append(lines, wrapStyled(m.llmStatus.Err.Error(), iw, styleFaint)...)
		case !m.llmStatus.Configured:
			lines = append(lines, styleFaint.Render("Summaries need an LLM: press L to set one up"))
		}
		keys = []keyHint{{"↑↓", "choose"}, {"↵", "hand off"}, {"L", "LLM"}, {"esc", "back"}}

	case stateHandoffWorking:
		lines = append(lines, stepper(2, false), "")
		text := "Writing the " + target + " session…"
		if m.handoffMode.NeedsLLM() {
			text = "Summarizing with " + m.llmStatus.Label + "…"
		}
		lines = append(lines, styleCursor.Render("● ")+styleBold.Render(text))
		if m.handoffMode.NeedsLLM() {
			lines = append(lines, "", styleFaint.Render("This can take a minute for long threads."))
		}
		keys = []keyHint{{"esc", "cancel"}}

	case stateHandoffConfirm:
		lines = append(lines, stepper(2, true), "")
		lines = append(lines, m.confirmLines(iw)...)
		keys = []keyHint{{"y", "open now"}, {"n", "not now"}}
	}

	lines = append(lines, "", renderKeys(keys).String())
	return m.dialog("Hand off", styleCursor, colorFaint, lines, pw)
}

// confirmLines say what a finished handoff or merge wrote and what opening it
// runs.
func (m Model) confirmLines(iw int) []string {
	label := domain.ProviderLabel(m.handoffTarget.Name())
	var done, ask string
	switch {
	case m.merging:
		done = fmt.Sprintf("Merged %d threads into a new %s session.", m.handoffCount, label)
		ask = "Open it now?"
	case m.handoffAppended:
		noun := "messages"
		if m.handoffCount == 1 {
			noun = "message"
		}
		done = fmt.Sprintf("Added %d %s to %s.", m.handoffCount, noun, label)
		ask = "Resume it now?"
	default:
		done = fmt.Sprintf("Wrote a new %s session.", label)
		ask = fmt.Sprintf("Open in %s?", label)
	}
	cmd := m.handoffTarget.OpenCommand(m.handoffSessionID, m.handoffWorkspace)
	out := []string{styleOK.Render("✓ ") + styleBold.Render(fit(done, iw-2)), "", styleFg.Render(ask)}
	return append(out, styleFaint.Render(fit("runs "+strings.Join(cmd.Args, " "), iw)))
}

// llmLine is the LLM's state for dialogs that need it.
func (m Model) llmLine() string {
	dot := styleFaint.Render("LLM  ")
	switch {
	case m.llmChecking:
		return dot + styleHint.Render("checking…")
	case m.llmStatus.Configured && m.llmStatus.Err == nil:
		return dot + styleOK.Render("● ") + styleHint.Render(m.llmStatus.Label+" ready")
	case m.llmStatus.Configured:
		return dot + styleErr.Render("○ ") + styleHint.Render(m.llmStatus.Label+" unavailable")
	case m.llmStatus.Err != nil:
		return dot + styleErr.Render("○ ") + styleHint.Render("config error")
	default:
		return dot + styleFaint.Render("○ ") + styleHint.Render("not configured")
	}
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

func (m Model) viewMergePicker() string {
	return overlay(m.viewList(), m.mergePanel(), m.width, m.height)
}

// mergePanel is the merge dialog on its own.
func (m Model) mergePanel() string {
	pw := dialogWidth(72, m.width)
	iw := pw - 4
	now := time.Now()
	lines := []string{styleBold.Render(fmt.Sprintf("Merge %d threads into a new session in:", len(m.mergeGroups))), ""}
	for i, w := range m.targetWriters() {
		r := choiceRow(i == m.handoffCursor)
		lines = append(lines, toolLabel(r, w.Name(), true).pad(iw))
	}
	lines = append(lines, "", styleFaint.Bold(true).Render("OLDEST FIRST"))
	for i, g := range m.mergeGroups {
		r := (&row{}).add(styleFaint, fmt.Sprintf("%d. ", i+1))
		toolIcons(r, groupTools(g)).space(1)
		date := (&row{}).add(styleFaint, whenLabel(groupActivity(g), now))
		r.add(styleFg, fit(displayTitle(g.Primary), iw-r.width()-date.width()-2))
		lines = append(lines, spread(r, date, iw))
	}
	if n := len(m.mergeGroups); n > 0 {
		newest := mergeSource(m.mergeGroups[n-1].Primary, m.mergeGroups[n-1].Members)
		if newest.WorkspacePath != "" {
			lines = append(lines, styleFaint.Render(fit("Runs in "+filepath.Clean(newest.WorkspacePath), iw)))
		}
	}
	lines = append(lines, "")
	lines = append(lines, wrapStyled("Each thread gets a brief of its own with its project and dates. The newest keeps its last 2 messages word for word.", iw, styleHint)...)
	lines = append(lines, "", m.llmLine())
	switch {
	case m.llmChecking:
	case m.llmStatus.Err != nil:
		lines = append(lines, wrapStyled(m.llmStatus.Err.Error(), iw, styleFaint)...)
	case !m.llmStatus.Configured:
		lines = append(lines, styleWarn.Render("Merging needs an LLM: press L to set one up"))
	}
	lines = append(lines, "", renderKeys([]keyHint{{"↑↓", "choose"}, {"↵", "merge"}, {"L", "LLM"}, {"esc", "back"}}).String())
	return panel("Merge", styleCursor, colorFaint, lines, pw)
}

// viewMergeProgress covers a merge from writing to opening the result.
func (m Model) viewMergeProgress(pw int) string {
	iw := pw - 4
	var lines []string
	var keys []keyHint
	if m.state == stateHandoffConfirm {
		lines = m.confirmLines(iw)
		keys = []keyHint{{"y", "open now"}, {"n", "not now"}}
	} else {
		lines = []string{styleCursor.Render("● ") + styleBold.Render(fmt.Sprintf("Summarizing %d threads with %s…", len(m.mergeGroups), m.llmStatus.Label)),
			"", styleFaint.Render("This can take a minute for long threads.")}
		keys = []keyHint{{"esc", "cancel"}}
	}
	lines = append(lines, "", renderKeys(keys).String())
	return m.dialog("Merge", styleCursor, colorFaint, lines, pw)
}

func (m Model) viewResumePicker() string {
	pw := dialogWidth(52, m.width)
	iw := pw - 4
	lines := []string{styleBold.Render("Resume in:"), ""}
	for i, t := range m.groupThreads {
		r := choiceRow(i == m.resumeCursor)
		toolLabel(r, t.Provider, !t.SourceMissing)
		right := r.sub()
		if t.SourceMissing {
			right.add(styleFaint, "deleted")
		}
		right.space(1)
		lines = append(lines, spread(r, right, iw))
	}
	lines = append(lines, "", renderKeys([]keyHint{{"↑↓", "choose"}, {"↵", "resume"}, {"esc", "back"}}).String())
	return m.dialog("Resume", styleCursor, colorFaint, lines, pw)
}

func (m Model) viewRename() string {
	pw := dialogWidth(60, m.width)
	iw := pw - 4
	lines := []string{
		styleBold.Render("Rename thread:"), "",
		styleFaint.Render("> ") + styleFg.Render(fit(m.renameDraft, iw-4)) + styleCursor.Render("▏"),
		"", styleFaint.Render("An empty title goes back to the original one."),
		"", renderKeys([]keyHint{{"↵", "save"}, {"esc", "cancel"}}).String(),
	}
	return m.dialog("Rename", styleCursor, colorFaint, lines, pw)
}

func (m Model) viewDelete() string {
	pw := dialogWidth(64, m.width)
	iw := pw - 4
	var title string
	if len(m.deleteTargets) > 0 {
		title = displayTitle(m.deleteTargets[0])
	}
	lines := []string{styleHint.Render(fit(title, iw)), "", styleBold.Render("Which copies?"), ""}
	for i, t := range m.deleteTargets {
		can := m.deletable(t)
		r := choiceRow(m.state == stateDeletePicker && i == m.deleteCursor)
		switch {
		case m.deleteSelected[i]:
			r.add(styleErr.Bold(true), "[x] ")
		default:
			r.add(styleFaint, "[ ] ")
		}
		toolLabel(r, t.Provider, can)
		role := "copy"
		if t.CanonicalID == "" || t.ID == t.CanonicalID {
			role = "original"
		}
		if !can {
			role += " · read-only"
		}
		right := r.sub().add(styleFaint, role).space(1)
		lines = append(lines, spread(r, right, iw))
	}
	lines = append(lines, "")

	var keys []keyHint
	switch m.state {
	case stateDeletePicker:
		lines = append(lines, wrapStyled("Deletes the chosen sessions from their tools and from threadman.", iw, styleFaint)...)
		keys = []keyHint{{"space", "toggle"}, {"a", "all"}, {"↵", "delete"}, {"esc", "cancel"}}
	case stateDeleteConfirm:
		n := len(m.selectedDeleteThreads())
		noun := "copies"
		if n == 1 {
			noun = "copy"
		}
		lines = append(lines, styleErr.Render(fmt.Sprintf("Delete %d %s? This can't be undone.", n, noun)))
		keys = []keyHint{{"y", "delete"}, {"n", "cancel"}}
	case stateDeleteTyping:
		lines = append(lines, wrapStyled("This is the original or the last copy. Type \"delete thread\" to confirm:", iw, styleErr)...)
		lines = append(lines, styleFaint.Render("> ")+styleFg.Render(fit(m.deleteDraft, iw-4))+styleCursor.Render("▏"))
		keys = []keyHint{{"↵", "confirm"}, {"esc", "cancel"}}
	}
	lines = append(lines, "", renderKeys(keys).String())
	return m.dialog("Delete", styleErr.Bold(true), colorErr, lines, pw)
}

func (m Model) viewOnboarding() string {
	pw := dialogWidth(64, m.width)
	iw := pw - 4
	lines := []string{styleBrand.Render(" threadman "), "", styleBold.Render("Import conversations from:"), ""}
	for i, p := range m.onboardingProviders {
		r := choiceRow(i == m.onboardingCursor)
		if p.selected {
			r.add(styleOK, "[x] ")
		} else {
			r.add(styleFaint, "[ ] ")
		}
		lines = append(lines, toolLabel(r, p.name, true).pad(iw))
	}
	lines = append(lines, "", styleBold.Render("Live updates"), "")
	r := choiceRow(m.onboardingCursor == len(m.onboardingProviders))
	if m.onboardingHooks {
		r.add(styleOK, "[x] ")
	} else {
		r.add(styleFaint, "[ ] ")
	}
	r.add(styleFg, "Import after every reply").add(styleFaint, "  installs a hook in each tool")
	lines = append(lines, r.pad(iw))
	lines = append(lines, "", styleFaint.Render("Everything stays on this machine."))
	if m.status != "" {
		lines = append(lines, "", styleCursor.Render("● ")+styleHint.Render(fit(m.status, iw-2)))
	}
	lines = append(lines, "", renderKeys([]keyHint{{"↑↓", "move"}, {"space", "toggle"}, {"↵", "import"}, {"q", "quit"}}).String())
	return centered(panel("Welcome", styleCursor, colorFaint, lines, pw), m.width, m.height)
}

// helpGroups is the full key list, shown with ?.
var helpGroups = []struct {
	title string
	keys  []keyHint
}{
	{"Threads", []keyHint{{"↵", "open"}, {"/", "search"}, {"tab", "next tool"}, {"A", "show archived"}, {"s", "sync now"}}},
	{"Act", []keyHint{{"space", "select"}, {"m", "merge"}, {"n", "rename"}, {"a", "archive"}, {"d", "delete"}}},
	{"Reading", []keyHint{{"j/k", "scroll"}, {"[ ]", "prev/next prompt"}, {"g G", "top/bottom"}, {"h", "hand off"}, {"r", "resume"}}},
	{"Setup", []keyHint{{"L", "LLM for summaries"}, {"H", "live hooks"}, {"q", "quit"}}},
}

func (m Model) viewHelp(base string) string {
	colW := 24
	perRow := max(1, min(len(helpGroups), (m.width-8)/colW))
	pw := colW*perRow + 4
	var lines []string
	for start := 0; start < len(helpGroups); start += perRow {
		end := min(len(helpGroups), start+perRow)
		var blocks [][]string
		var widths []int
		for _, g := range helpGroups[start:end] {
			block := []string{styleFaint.Bold(true).Render(strings.ToUpper(g.title)), ""}
			for _, k := range g.keys {
				block = append(block, styleAccent.Render(fmt.Sprintf("%-6s", k.key))+styleHint.Render(k.does))
			}
			blocks = append(blocks, block)
			widths = append(widths, colW)
		}
		h := 0
		for _, b := range blocks {
			h = max(h, len(b))
		}
		if start > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, joinColumns(h, "", widths, blocks...)...)
	}
	lines = append(lines, "", renderKeys([]keyHint{{"esc", "close"}}).String())
	return overlay(base, panel("Keys", styleCursor, colorFaint, lines, pw), m.width, m.height)
}
