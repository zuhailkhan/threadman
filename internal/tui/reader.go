package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/zuhailkhan/threadman/internal/conversation"
	"github.com/zuhailkhan/threadman/internal/domain"
)

// The reader shows the outline of prompts beside the conversation when the
// terminal is at least previewMinWidth wide.
const outlineWidth = 30

// promptMark is where one of the user's messages starts in the reader.
type promptMark struct {
	line int
	text string
}

func (m Model) outlineShown() bool { return m.width >= previewMinWidth }

// readerWidth is the width of the conversation itself.
func (m Model) readerWidth() int {
	if m.outlineShown() {
		return max(20, m.width-outlineWidth-3)
	}
	return max(20, m.width)
}

// setReaderContent lays the open conversation out for the current width,
// keeping the scroll position.
func (m *Model) setReaderContent() {
	offset := m.viewport.YOffset
	text, prompts := layoutConversation(m.groupThreads, m.readerWidth()-1)
	m.viewport.SetContent(text)
	m.prompts = prompts
	m.viewport.SetYOffset(offset)
}

// renderConversation lays a conversation out at a fixed width.
func renderConversation(threads []domain.Thread) string {
	text, _ := layoutConversation(threads, 100)
	return text
}

// renderThread lays out a single session.
func renderThread(t domain.Thread) string {
	return renderConversation([]domain.Thread{t})
}

// layoutConversation lays a conversation's sessions out as one timeline.
// When more than one tool took part, each message is tagged with its tool,
// and each handoff shows as a divider naming the tool it went to.
func layoutConversation(threads []domain.Thread, width int) (string, []promptMark) {
	entries := conversation.Timeline(threads)
	providers := map[string]bool{}
	for _, t := range threads {
		providers[t.Provider] = true
	}
	multi := len(providers) > 1
	now := time.Now()

	var lines []string
	var prompts []promptMark
	textW := max(10, width-2)
	for _, e := range entries {
		msg := e.Message
		if msg.Kind == domain.KindHandoff {
			lines = append(lines, handoffDivider(e.Provider, msg, width), "")
			continue
		}
		content := cleanText(msg.Content)
		bar, label := styleAI, "Assistant"
		switch msg.Role {
		case domain.RoleUser:
			bar, label = styleYou, "You"
			prompts = append(prompts, promptMark{line: len(lines), text: firstLine(content)})
		case domain.RoleAssistant:
		default:
			bar, label = styleHint, string(msg.Role)
		}
		head := (&row{}).add(bar, "▎ ")
		if multi {
			head.add(providerStyle(e.Provider), providerIcon(e.Provider)).space(1)
		}
		head.add(bar, label)
		if !msg.Timestamp.IsZero() {
			head.add(styleFaint, "  ·  "+messageTime(msg.Timestamp, now))
		}
		lines = append(lines, head.String())
		lines = append(lines, contentLines(content, textW)...)
		for _, call := range msg.Tools {
			lines = append(lines, styleFaint.Render("▎ ")+toolLine(call))
		}
		lines = append(lines, "")
	}
	// A line wider than the reader would wrap in the terminal and push every
	// line below it down.
	for i, l := range lines {
		if lipgloss.Width(l) > width {
			lines[i] = ansi.Truncate(l, width, "…")
		}
	}
	return strings.Join(lines, "\n"), prompts
}

func messageTime(t, now time.Time) string {
	t = t.Local()
	if dateBucket(t, now) == "Today" {
		return t.Format("15:04")
	}
	return t.Format("02 Jan 15:04")
}

func firstLine(s string) string {
	for _, l := range strings.Split(cleanText(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// contentLines renders a message body under its bar: wrapped text, inline
// code in the accent colour, and fenced code on a tinted background.
func contentLines(content string, width int) []string {
	bar := styleFaint.Render("▎ ")
	var out []string
	inCode := false
	for _, l := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inCode = !inCode
			out = append(out, bar+styleCode.Render(strings.Repeat(" ", width)))
			continue
		}
		if inCode {
			code := strings.ReplaceAll(l, "\t", "    ")
			out = append(out, bar+styleCode.Render(padLine("  "+fit(code, width-2), width)))
			continue
		}
		for _, w := range wrapText(l, width) {
			out = append(out, bar+inlineCode(w))
		}
	}
	return out
}

// inlineCode colours `code` spans in a line of text.
func inlineCode(s string) string {
	parts := strings.Split(s, "`")
	if len(parts) < 3 {
		return styleFg.Render(s)
	}
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 && i < len(parts)-1 {
			b.WriteString(styleAccent.Render(p))
		} else {
			b.WriteString(styleFg.Render(p))
		}
	}
	return b.String()
}

// toolLine is a one-line summary of a tool call for the reader: the tool and
// the first line of its input. threadman show prints calls in full.
func toolLine(call domain.ToolCall) string {
	input := firstLine(call.Input)
	text := "▸ " + oneLine(call.Name)
	if input != "" {
		text += "  " + truncateRunes(input, 100)
	}
	if call.Error {
		return styleHint.Render(text) + styleErr.Render("  (failed)")
	}
	return styleHint.Render(text)
}

// handoffDivider stands in for a handoff block threadman wrote into a session.
// The block repeats messages that are on the timeline in their own right, so
// only its description is shown.
func handoffDivider(provider string, m domain.Message, width int) string {
	label := "handed off to " + domain.ProviderLabel(provider)
	if _, detail, ok := domain.ParseHandoffMarker(m.Content); ok && detail != "" {
		label += " · " + oneLine(detail)
	}
	label = fit(label, width-8)
	rest := max(0, width-lipgloss.Width(label)-6)
	return styleSep.Render("──── ") + providerStyle(provider).Render(providerIcon(provider)) + " " +
		styleHint.Render(label) + " " + styleSep.Render(strings.Repeat("─", max(0, rest-2)))
}

// activePrompt is the prompt the reader is at.
func (m Model) activePrompt() int {
	active := -1
	for i, p := range m.prompts {
		if p.line <= m.viewport.YOffset+1 {
			active = i
		}
	}
	if active < 0 && len(m.prompts) > 0 {
		active = 0
	}
	return active
}

// jumpPrompt scrolls to the next (step 1) or previous (step -1) prompt.
func (m *Model) jumpPrompt(step int) {
	y := m.viewport.YOffset
	if step > 0 {
		for _, p := range m.prompts {
			if p.line > y {
				m.viewport.SetYOffset(p.line)
				return
			}
		}
		m.viewport.GotoBottom()
		return
	}
	for i := len(m.prompts) - 1; i >= 0; i-- {
		if m.prompts[i].line < y {
			m.viewport.SetYOffset(m.prompts[i].line)
			return
		}
	}
	m.viewport.GotoTop()
}

func (m Model) outlineLines(h int) []string {
	out := []string{styleFaint.Bold(true).Render(" PROMPTS") + styleFaint.Render(fmt.Sprintf("  %d", len(m.prompts))), ""}
	active := m.activePrompt()
	rows := h - 4
	start := 0
	if active >= rows {
		start = active - rows + 1
	}
	for i := start; i < len(m.prompts) && i < start+rows; i++ {
		on := i == active
		r := choiceRow(on)
		numSt, textSt := styleFaint, styleHint
		if on {
			numSt, textSt = styleCursor, styleBold
		}
		num := fmt.Sprintf("%2d ", i+1)
		r.add(numSt, num).add(textSt, fit(m.prompts[i].text, outlineWidth-r.width()-len(num)))
		out = append(out, r.pad(outlineWidth))
	}
	if len(m.prompts) > 0 {
		out = append(out, "", styleFaint.Render(" [ ] jump between prompts"))
	}
	return out
}

func (m Model) viewThread() string {
	w := m.width
	bodyH := m.height - 4

	left := &row{}
	left.add(styleBrand, " threadman ").add(styleFaint, " › ")
	right := (&row{}).space(1)
	for i, t := range groupTools(ThreadGroup{Primary: m.thread, Members: m.groupThreads}) {
		if i > 0 {
			right.add(styleFaint, " → ")
		}
		st := providerStyle(t.name)
		if !t.live {
			st = styleArchived
		}
		right.add(st, providerIcon(t.name)+" "+domain.ProviderLabel(t.name))
	}
	right.space(1)
	if right.width() > w/2 {
		right = toolIcons((&row{}).space(1), groupTools(ThreadGroup{Primary: m.thread, Members: m.groupThreads})).space(1)
	}
	avail := w - left.width() - right.width() - 2
	title := fit(displayTitle(m.thread), avail)
	left.add(styleBold, title)
	if ws := shortPath(m.thread.WorkspacePath); avail-lipgloss.Width(title) > 10 && m.thread.WorkspacePath != "" {
		left.add(styleFaint, "   "+fit(ws, avail-lipgloss.Width(title)-3))
	}
	header := spread(left, right, w)

	note := ""
	if gone := goneTools(m.groupThreads); gone != "" {
		note = "deleted in " + gone + ", kept by threadman"
	}

	view := strings.Split(m.viewport.View(), "\n")
	var body []string
	if m.outlineShown() {
		body = joinColumns(bodyH, styleSep.Render(" │ "), []int{outlineWidth, m.readerWidth()}, m.outlineLines(bodyH), view)
	} else {
		body = joinColumns(bodyH, "", []int{w}, view)
	}

	total := 0
	for _, t := range m.groupThreads {
		for _, msg := range t.Messages {
			if msg.Kind != domain.KindHandoff {
				total++
			}
		}
	}
	status := fmt.Sprintf("%s · %d%%", plural(total, "message"), int(m.viewport.ScrollPercent()*100))
	keys := []keyHint{{"j/k", "scroll"}, {"[ ]", "prompt"}, {"h", "hand off"}, {"r", "resume"}, {"esc", "back"}, {"?", "keys"}}

	var b strings.Builder
	b.WriteString(header + "\n")
	b.WriteString(rule(w, note, styleWarn) + "\n")
	b.WriteString(strings.Join(body, "\n") + "\n")
	b.WriteString(rule(w, "", styleFaint) + "\n")
	b.WriteString(m.viewFooter(keys, status))
	return b.String()
}
