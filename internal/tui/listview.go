package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/zuhailkhan/threadman/internal/conversation"
	"github.com/zuhailkhan/threadman/internal/domain"
)

// The list shows a preview pane beside it when the terminal is this wide.
const previewMinWidth = 100

func displayTitle(t domain.Thread) string {
	switch {
	case t.CustomTitle != "":
		return t.CustomTitle
	case domain.IsHandoffMarker(t.Title):
		return handoffTitle(t)
	case t.Title != "":
		return t.Title
	default:
		return "(untitled)"
	}
}

// handoffTitle names a session that a handoff created, whose own title is
// the block's marker line: its first prompt after the block, or else what
// the block says it carries.
func handoffTitle(t domain.Thread) string {
	// Sessions synced before blocks were recognised don't mark the block as
	// one, so its marker line is checked as well as its kind.
	for _, m := range t.Messages {
		if m.Kind == domain.KindHandoff || m.Role != domain.RoleUser {
			continue
		}
		if l := firstLine(m.Content); l != "" && !domain.IsHandoffMarker(l) {
			return l
		}
	}
	for _, s := range append([]string{t.Title}, messageContents(t)...) {
		if _, detail, ok := domain.ParseHandoffMarker(s); ok && detail != "" {
			return "Handoff " + detail
		}
	}
	return "Handoff"
}

func messageContents(t domain.Thread) []string {
	out := make([]string, len(t.Messages))
	for i, m := range t.Messages {
		out[i] = m.Content
	}
	return out
}

// plural is n with the noun, made plural when n isn't 1.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// groupActivity is when a conversation was last active in any of its tools.
func groupActivity(g ThreadGroup) time.Time {
	var last time.Time
	for _, t := range append([]domain.Thread{g.Primary}, g.Members...) {
		for _, m := range t.Messages {
			if m.Timestamp.After(last) {
				last = m.Timestamp
			}
		}
		if last.IsZero() && t.CreatedAt.After(last) {
			last = t.CreatedAt
		}
	}
	return last
}

type groupTool struct {
	name string
	// live is false when the tool deleted all its sessions in the group.
	live bool
}

// groupTools lists the tools a conversation lives in, its original first.
func groupTools(g ThreadGroup) []groupTool {
	var out []groupTool
	index := map[string]int{}
	for _, t := range append([]domain.Thread{g.Primary}, g.Members...) {
		i, ok := index[t.Provider]
		if !ok {
			i = len(out)
			index[t.Provider] = i
			out = append(out, groupTool{name: t.Provider})
		}
		if !t.SourceMissing {
			out[i].live = true
		}
	}
	return out
}

// providerIcons is the row's tool icons, dimmed for tools that no longer
// have the conversation.
func providerIcons(g ThreadGroup) string {
	r := &row{}
	toolIcons(r, groupTools(g))
	return r.String()
}

func toolIcons(r *row, tools []groupTool) *row {
	for i, t := range tools {
		if i > 0 {
			r.space(1)
		}
		st := providerStyle(t.name)
		if !t.live {
			st = styleArchived
		}
		r.add(st, providerIcon(t.name))
	}
	return r
}

// messageCounts counts a conversation's messages across its tools. Handoff
// blocks repeat messages already counted, so they are left out.
func messageCounts(g ThreadGroup) (total, you, assistant int) {
	for _, t := range g.Members {
		for _, m := range t.Messages {
			if m.Kind == domain.KindHandoff {
				continue
			}
			total++
			switch m.Role {
			case domain.RoleUser:
				you++
			case domain.RoleAssistant:
				assistant++
			}
		}
	}
	return
}

func startOfDay(t time.Time) time.Time {
	y, mo, d := t.Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, t.Location())
}

// dateBucket is the heading a conversation is listed under.
func dateBucket(t, now time.Time) string {
	days := int(startOfDay(now).Sub(startOfDay(t.Local())).Hours() / 24)
	switch {
	case days <= 0:
		return "Today"
	case days == 1:
		return "Yesterday"
	case days < 7:
		return "This week"
	case days < 31:
		return "This month"
	default:
		return "Earlier"
	}
}

// whenLabel is a short time for a row: the time today and yesterday, the
// weekday this week, and the date before that.
func whenLabel(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	days := int(startOfDay(now).Sub(startOfDay(t)).Hours() / 24)
	switch {
	case days <= 1:
		return t.Format("15:04")
	case days < 7:
		return t.Format("Mon")
	case t.Year() == now.Year():
		return t.Format("02 Jan")
	default:
		return t.Format("02 Jan 06")
	}
}

// longWhen is a time with its day, for the preview.
func longWhen(t, now time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	t = t.Local()
	switch dateBucket(t, now) {
	case "Today":
		return "today " + t.Format("15:04")
	case "Yesterday":
		return "yesterday " + t.Format("15:04")
	}
	return t.Format("02 Jan 2006, 15:04")
}

// shortPath replaces the home directory with ~.
func shortPath(p string) string {
	if p == "" {
		return "unknown"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(filepath.Join("~", rel))
		}
	}
	return p
}

// toolTabs lists the tools that have conversations, most used first.
func (m Model) toolTabs() []string {
	count := map[string]int{}
	for _, g := range m.groups {
		for _, t := range groupTools(g) {
			count[t.name]++
		}
	}
	tabs := make([]string, 0, len(count))
	for name := range count {
		tabs = append(tabs, name)
	}
	sort.Slice(tabs, func(i, j int) bool {
		if count[tabs[i]] != count[tabs[j]] {
			return count[tabs[i]] > count[tabs[j]]
		}
		return tabs[i] < tabs[j]
	})
	return tabs
}

// cycleToolFilter moves the tool filter to the next (or previous) tab.
func (m *Model) cycleToolFilter(step int) {
	tabs := append([]string{""}, m.toolTabs()...)
	i := 0
	for j, t := range tabs {
		if t == m.toolFilter {
			i = j
		}
	}
	m.toolFilter = tabs[(i+step+len(tabs))%len(tabs)]
	m.applyFilter()
	m.cursor = 0
	m.listTop = 0
}

func inTool(g ThreadGroup, tool string) bool {
	for _, t := range groupTools(g) {
		if t.name == tool {
			return true
		}
	}
	return false
}

// matchSnippet finds q in a conversation's messages and returns the text
// around it, or "" when only the title or tools match.
func matchSnippet(g ThreadGroup, q string, width int) string {
	q = strings.ToLower(q)
	for _, t := range g.Members {
		for _, msg := range t.Messages {
			if msg.Kind == domain.KindHandoff {
				continue
			}
			text := strings.Join(strings.Fields(msg.Content), " ")
			i := strings.Index(strings.ToLower(text), q)
			if i < 0 {
				continue
			}
			start := max(0, i-width/4)
			for start > 0 && start < len(text) && !isRuneStart(text[start]) {
				start--
			}
			prefix := ""
			if start > 0 {
				prefix = "…"
			}
			return prefix + text[start:]
		}
	}
	return ""
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func groupMatches(g ThreadGroup, q string) bool {
	q = strings.ToLower(q)
	if strings.Contains(strings.ToLower(displayTitle(g.Primary)), q) ||
		strings.Contains(strings.ToLower(filepath.Base(g.Primary.WorkspacePath)), q) {
		return true
	}
	for _, t := range groupTools(g) {
		if strings.Contains(strings.ToLower(domain.ProviderLabel(t.name)), q) {
			return true
		}
	}
	return matchSnippet(g, q, 80) != ""
}

func (m *Model) applyFilter() {
	m.filteredGroups = make([]ThreadGroup, 0, len(m.groups))
	for _, g := range m.groups {
		if m.toolFilter != "" && !inTool(g, m.toolFilter) {
			continue
		}
		if m.query != "" && !groupMatches(g, m.query) {
			continue
		}
		m.filteredGroups = append(m.filteredGroups, g)
	}
	if m.cursor >= len(m.filteredGroups) {
		m.cursor = max(0, len(m.filteredGroups)-1)
	}
}

// listItem is a date heading (group < 0) or a conversation row.
type listItem struct {
	heading string
	group   int
}

func (m Model) listItems(now time.Time) []listItem {
	items := make([]listItem, 0, len(m.filteredGroups)+5)
	last := ""
	for i, g := range m.filteredGroups {
		// Search results are ordered by relevance to the eye, not by date.
		if m.query == "" {
			if b := dateBucket(groupActivity(g), now); b != last {
				items = append(items, listItem{heading: b, group: -1})
				last = b
			}
		}
		items = append(items, listItem{group: i})
	}
	return items
}

func itemHeight(it listItem) int {
	if it.group < 0 {
		return 1
	}
	return 2
}

// listHeight is how many lines the rows get: the screen less the header,
// footer, dividers and search line.
func (m Model) listHeight() int {
	return max(2, m.height-6)
}

// keepCursorVisible scrolls the list so the cursor's row, and the date
// heading above it, are on screen.
func (m *Model) keepCursorVisible() {
	items := m.listItems(time.Now())
	top, line := 0, 0
	for i, it := range items {
		if it.group == m.cursor {
			top = line
			if i > 0 && items[i-1].group < 0 {
				top--
			}
			break
		}
		line += itemHeight(it)
	}
	bottom := line + 2
	h := m.listHeight()
	if top < m.listTop {
		m.listTop = top
	}
	if bottom > m.listTop+h {
		m.listTop = bottom - h
	}
	if m.listTop < 0 {
		m.listTop = 0
	}
}

// viewHeader is the top line: the name, the tool tabs and a count.
func (m Model) viewHeader() string {
	left := &row{}
	left.add(styleBrand, " threadman ").space(3)
	tab := func(name, label string, n int) {
		st := styleHint
		if m.toolFilter == name {
			st = styleTab
		}
		if name != "" {
			left.add(providerStyle(name), providerIcon(name)).space(1)
		}
		left.add(st, label).add(styleFaint, fmt.Sprintf(" %d", n)).space(3)
	}

	right := &row{}
	if m.showArchived {
		right.add(styleFaint, "archived shown · ")
	}
	right.add(styleFaint, plural(len(m.filteredGroups), "thread")).space(1)

	tab("", "All", len(m.groups))
	tabs := m.toolTabs()
	for i, name := range tabs {
		n := 0
		for _, g := range m.groups {
			if inTool(g, name) {
				n++
			}
		}
		label := domain.ProviderLabel(name)
		need := lipgloss.Width(providerIcon(name)) + 1 + lipgloss.Width(label) + len(fmt.Sprint(n)) + 4
		if left.width()+need+8 > m.width-right.width() && name != m.toolFilter {
			left.add(styleFaint, fmt.Sprintf("+%d", len(tabs)-i))
			break
		}
		tab(name, label, n)
	}
	if left.width()+right.width() > m.width {
		return padLine(left.String(), m.width)
	}
	return spread(left, right, m.width)
}

// viewFooter is the bottom line: the keys that work here, and the status or
// the LLM's state.
func (m Model) viewFooter(keys []keyHint, right string) string {
	if m.status != "" {
		right = m.status
	}
	l := (&row{}).space(1)
	l.raw(renderKeys(keys).String())
	r := &row{}
	avail := m.width - l.width() - 3
	if avail < 12 {
		// Not enough room for both: the status wins.
		return padLine(" "+styleHint.Render(fit(right, m.width-2)), m.width)
	}
	r.add(styleHint, fit(right, avail)).space(1)
	return spread(l, r, m.width)
}

func (m Model) llmFooter() string {
	dot := "● "
	switch {
	case m.llmChecking:
		return dot + "LLM checking…"
	case m.llmStatus.Configured && m.llmStatus.Err == nil:
		return dot + m.llmStatus.Label
	case m.llmStatus.Configured:
		return "○ " + m.llmStatus.Label + " unavailable"
	default:
		return "○ no LLM"
	}
}

func (m Model) viewList() string {
	now := time.Now()
	w := m.width
	listW := w
	showPreview := w >= previewMinWidth
	if showPreview {
		listW = min(72, w/2+2)
	}
	bodyH := m.height - 4

	// left pane
	left := make([]string, 0, bodyH)
	search := &row{}
	switch {
	case m.state == stateSearch:
		search.add(styleCursor, " / ").add(styleBold, m.query).add(styleCursor, "▏")
	case m.query != "":
		search.add(styleCursor, " / ").add(styleBold, m.query).add(styleFaint, "   esc clears")
	default:
		search.add(styleFaint, " / search titles and messages")
	}
	if m.query != "" || m.toolFilter != "" {
		count := (&row{}).add(styleFaint, fmt.Sprintf("%d of %d ", len(m.filteredGroups), len(m.groups)))
		left = append(left, spread(search, count, listW))
	} else {
		left = append(left, search.pad(listW))
	}
	left = append(left, "")

	items := m.listItems(now)
	line := 0
	h := m.listHeight()
	for _, it := range items {
		ih := itemHeight(it)
		if line+ih <= m.listTop {
			line += ih
			continue
		}
		if line >= m.listTop+h {
			break
		}
		if it.group < 0 {
			left = append(left, styleFaint.Bold(true).Render(" "+strings.ToUpper(it.heading)+" ")+
				styleSep.Render(strings.Repeat("─", max(0, listW-len(it.heading)-3))))
		} else {
			left = append(left, m.listRow(it.group, listW, now)...)
		}
		line += ih
	}
	if len(m.filteredGroups) == 0 {
		msg := "   No threads yet. Press s to sync."
		switch {
		case m.query != "":
			msg = "   No threads match. Esc clears the search."
		case m.toolFilter != "":
			msg = "   No threads in " + domain.ProviderLabel(m.toolFilter) + ". Tab switches tools."
		}
		left = append(left, styleHint.Render(msg))
	}

	var body []string
	if showPreview {
		previewW := w - listW - 3
		var preview []string
		if len(m.filteredGroups) > 0 {
			preview = m.previewLines(m.filteredGroups[m.cursor], previewW, bodyH, now)
		}
		body = joinColumns(bodyH, styleSep.Render(" │ "), []int{listW, previewW}, left, preview)
	} else {
		body = joinColumns(bodyH, "", []int{listW}, left)
	}

	keys := []keyHint{{"↵", "open"}, {"/", "search"}, {"tab", "tool"}, {"space", "select"}, {"d", "delete"}, {"?", "keys"}}
	right := m.llmFooter()
	switch {
	case m.state == stateSearch:
		keys = []keyHint{{"type", "filter"}, {"↵", "done"}, {"esc", "clear"}}
	case len(m.mergeSelected) > 0:
		keys = []keyHint{{"space", "select"}, {"m", "merge"}, {"esc", "clear"}}
		right = fmt.Sprintf("%d selected", len(m.mergeSelected))
	}

	var b strings.Builder
	b.WriteString(m.viewHeader() + "\n")
	b.WriteString(rule(w, "", styleFaint) + "\n")
	b.WriteString(strings.Join(body, "\n") + "\n")
	b.WriteString(rule(w, "", styleFaint) + "\n")
	b.WriteString(m.viewFooter(keys, right))
	return b.String()
}

// listRow is a conversation's two lines in the list.
func (m Model) listRow(i, width int, now time.Time) []string {
	g := m.filteredGroups[i]
	selected := i == m.cursor
	picked := m.mergeSelected[groupKey(g)]

	titleStyle := styleFg
	switch {
	case selected:
		titleStyle = styleBold
	case g.Primary.Archived:
		titleStyle = styleHint
	}

	one := choiceRow(selected)
	if picked {
		one.add(styleCursor, "✓ ")
	}
	icons := toolIcons(one.sub(), groupTools(g)).space(1)
	title := fit(displayTitle(g.Primary), width-one.width()-icons.width()-2)
	one.add(titleStyle, title)
	if m.query != "" {
		one = m.highlightTitle(selected, picked, title, titleStyle)
	}
	line1 := spread(one, icons, width)

	two := newRow(selected)
	if selected {
		two.add(styleCursor, "▌")
	} else {
		two.space(1)
	}
	two.space(3)
	when := two.sub().add(styleFaint, whenLabel(groupActivity(g), now)).space(1)
	avail := width - two.width() - when.width() - 2
	if m.query != "" {
		// When the match is in a message, show where instead of the details.
		snip := matchSnippet(g, m.query, avail)
		if snip != "" && !strings.Contains(strings.ToLower(displayTitle(g.Primary)), strings.ToLower(m.query)) {
			addHighlighted(two, fit(snip, avail), m.query, styleHint)
			return []string{line1, spread(two, when, width)}
		}
	}
	total, _, _ := messageCounts(g)
	meta := filepath.Base(g.Primary.WorkspacePath)
	if g.Primary.WorkspacePath == "" {
		meta = "no workspace"
	}
	parts := []string{meta, plural(total, "msg")}
	if len(groupTools(g)) > 1 {
		parts = append(parts, "handed off")
	}
	if g.Primary.Archived {
		parts = append(parts, "archived")
	}
	two.add(styleHint, fit(strings.Join(parts, " · "), avail))
	return []string{line1, spread(two, when, width)}
}

// highlightTitle rebuilds a row's start with the search match marked.
func (m Model) highlightTitle(selected, picked bool, title string, st lipgloss.Style) *row {
	r := choiceRow(selected)
	if picked {
		r.add(styleCursor, "✓ ")
	}
	addHighlighted(r, title, m.query, st)
	return r
}

// addHighlighted adds text to r with every match of q marked.
func addHighlighted(r *row, text, q string, st lipgloss.Style) {
	lower, lq := strings.ToLower(text), strings.ToLower(q)
	for q != "" {
		i := strings.Index(lower, lq)
		if i < 0 {
			break
		}
		r.add(st, text[:i])
		m := styleMatch
		if r.bg != nil {
			// The match colour stands in for the selection on this run.
			m = m.UnsetBackground().Background(colorMatch)
		}
		r.raw(m.Render(text[i : i+len(q)]))
		text, lower = text[i+len(q):], lower[i+len(q):]
	}
	r.add(st, text)
}

// previewLines is the pane beside the list: the selected conversation's
// details and its latest messages.
func (m Model) previewLines(g ThreadGroup, w, h int, now time.Time) []string {
	var out []string
	for i, l := range wrapText(displayTitle(g.Primary), w-1) {
		if i == 2 {
			break
		}
		out = append(out, " "+styleBold.Render(l))
	}

	chips := (&row{}).space(1)
	for i, t := range groupTools(g) {
		if i > 0 {
			chips.add(styleFaint, "  →  ")
		}
		st := providerStyle(t.name)
		if !t.live {
			st = styleArchived
		}
		chips.add(st, providerIcon(t.name)+" "+domain.ProviderLabel(t.name))
	}
	out = append(out, padLine(chips.String(), w), "")

	kv := func(k, v string) {
		out = append(out, " "+styleFaint.Render(fmt.Sprintf("%-13s", k))+fit(v, w-15))
	}
	total, you, ai := messageCounts(g)
	started := g.Primary.CreatedAt
	for _, t := range g.Members {
		if !t.CreatedAt.IsZero() && (started.IsZero() || t.CreatedAt.Before(started)) {
			started = t.CreatedAt
		}
	}
	kv("workspace", shortPath(g.Primary.WorkspacePath))
	kv("messages", fmt.Sprintf("%d   you %d · assistant %d", total, you, ai))
	kv("started", longWhen(started, now))
	kv("last active", longWhen(groupActivity(g), now))
	if n := len(g.Members); n > 1 {
		kv("sessions", fmt.Sprintf("%d, original in %s", n, domain.ProviderLabel(g.Primary.Provider)))
	}
	if gone := goneTools(g.Members); gone != "" {
		kv("deleted in", gone+", kept by threadman")
	}

	entries := conversation.Timeline(g.Members)
	var latest []conversation.Entry
	for i := len(entries) - 1; i >= 0 && len(latest) < 3; i-- {
		if e := entries[i]; e.Message.Kind != domain.KindHandoff && strings.TrimSpace(e.Message.Content) != "" {
			latest = append([]conversation.Entry{e}, latest...)
		}
	}
	if len(latest) == 0 {
		return out
	}
	out = append(out, "", styleFaint.Bold(true).Render(" LATEST ")+styleSep.Render(strings.Repeat("─", max(0, w-9))))
	for _, e := range latest {
		if len(out) >= h-2 {
			break
		}
		msg := e.Message
		label, st := "Assistant", styleAI
		if msg.Role == domain.RoleUser {
			label, st = "You", styleYou
		}
		head := (&row{}).space(1).add(st, "▎ "+label)
		if !msg.Timestamp.IsZero() {
			head.add(styleFaint, "  "+whenLabel(msg.Timestamp, now))
		}
		out = append(out, "", head.String())
		text := strings.Join(strings.Fields(stripFences(msg.Content)), " ")
		for i, l := range wrapText(text, w-5) {
			if i == 3 || len(out) >= h {
				break
			}
			out = append(out, " "+styleFaint.Render("▎ ")+styleHint.Render(l))
		}
	}
	return out
}

// stripFences shortens fenced code to a placeholder for one-line previews.
func stripFences(s string) string {
	var b strings.Builder
	inCode := false
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			if !inCode {
				b.WriteString(" [code] ")
			}
			inCode = !inCode
			continue
		}
		if !inCode {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}
