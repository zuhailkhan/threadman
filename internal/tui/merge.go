package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zuhailkhan/threadman/internal/conversation"
	"github.com/zuhailkhan/threadman/internal/domain"
	"github.com/zuhailkhan/threadman/internal/handoff"
	"github.com/zuhailkhan/threadman/internal/ports"
)

// groupKey identifies a row of the thread list across reloads.
func groupKey(g ThreadGroup) string {
	if g.Primary.CanonicalID != "" {
		return g.Primary.CanonicalID
	}
	return g.Primary.ID
}

// selectedGroups returns the rows picked for a merge, oldest first.
func (m Model) selectedGroups() []ThreadGroup {
	var out []ThreadGroup
	for _, g := range m.groups {
		if m.mergeSelected[groupKey(g)] {
			out = append(out, g)
		}
	}
	byKey := make(map[string]ThreadGroup, len(out))
	threads := make([]domain.Thread, len(out))
	for i, g := range out {
		t := mergeSource(g.Primary, g.Members)
		threads[i] = t
		byKey[t.ID] = g
	}
	ordered := make([]ThreadGroup, 0, len(out))
	for _, t := range handoff.ByActivity(threads) {
		ordered = append(ordered, byKey[t.ID])
	}
	return ordered
}

// mergeSource is one row of a merge as a single thread: the conversation's
// messages from all its sessions in time order, under its primary session's
// title and ID, in the workspace of the session last active.
func mergeSource(primary domain.Thread, members []domain.Thread) domain.Thread {
	src := primary
	src.Messages = conversation.Messages(conversation.Everything(conversation.Timeline(members)))
	if head, ok := conversation.Head(members); ok && head.WorkspacePath != "" {
		src.WorkspacePath = head.WorkspacePath
	}
	return src
}

// writeMerge reads every session of the chosen rows, has each row briefed and
// writes the result into the target tool as a new session.
func (m Model) writeMerge(ctx context.Context, w ports.ThreadWriter, groups []ThreadGroup) tea.Cmd {
	svc := m.svc
	s := m.summarizer
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()

		sources := make([]domain.Thread, 0, len(groups))
		for _, g := range groups {
			members := make([]domain.Thread, 0, len(g.Members))
			for _, mem := range g.Members {
				full, err := svc.GetThread(ctx, mem.ID)
				if err != nil {
					full = mem
				}
				members = append(members, full)
			}
			sources = append(sources, mergeSource(g.Primary, members))
		}

		out, record, err := s.PrepareMerge(ctx, sources)
		if err != nil {
			return handoffErrMsg{err}
		}
		if err := ctx.Err(); err != nil {
			return handoffErrMsg{err}
		}
		id, err := w.WriteThread(ctx, out)
		if err != nil {
			return handoffErrMsg{err}
		}
		record.TargetProvider = w.Name()
		record.TargetOriginalID = id
		newest := handoff.ByActivity(sources)[len(sources)-1]
		return handoffWrittenMsg{sessionID: id, writer: w, record: record, source: newest,
			workspace: out.WorkspacePath, count: len(sources)}
	}
}

// startMerge opens the merge dialog for the selected rows, or says why it
// can't.
func (m *Model) startMerge() tea.Cmd {
	n := len(m.selectedGroups())
	switch {
	case n < 2:
		m.status = "select 2 or more threads with Space, then press m to merge them"
		return nil
	case n > handoff.MaxMergeThreads:
		m.status = fmt.Sprintf("a merge takes at most %d threads; %d are selected", handoff.MaxMergeThreads, n)
		return nil
	case len(m.targetWriters()) == 0:
		m.status = "no tool to write the merged session into"
		return nil
	}
	m.mergeGroups = m.selectedGroups()
	m.handoffCursor = 0
	m.state = stateMergePicker
	m.llmChecking = true
	return m.checkLLM()
}

func (m Model) viewMergePicker() string {
	dialogWidth := 64

	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString(fmt.Sprintf("  Merge %d threads into a new session in:\n", len(m.mergeGroups)))
	inner.WriteString("\n")
	for i, w := range m.targetWriters() {
		label := domain.ProviderLabel(w.Name())
		if i == m.handoffCursor {
			inner.WriteString(styleCursor.Render("▶") + " " + label + "\n")
		} else {
			inner.WriteString("  " + label + "\n")
		}
	}
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  Oldest first:") + "\n")
	for i, g := range m.mergeGroups {
		title := g.Primary.CustomTitle
		if title == "" {
			title = g.Primary.Title
		}
		if title == "" {
			title = "(untitled)"
		}
		line := fmt.Sprintf("%d. %s %s", i+1, providerIcons(g), truncateRunes(title, dialogWidth-26))
		date := styleHint.Render(lastMessageDate(g.Primary))
		gap := dialogWidth - 4 - lipgloss.Width(line) - lipgloss.Width(date)
		if gap < 1 {
			gap = 1
		}
		inner.WriteString("  " + line + strings.Repeat(" ", gap) + date + "\n")
	}
	if n := len(m.mergeGroups); n > 0 {
		newest := mergeSource(m.mergeGroups[n-1].Primary, m.mergeGroups[n-1].Members)
		if newest.WorkspacePath != "" {
			inner.WriteString(styleHint.Render("  Runs in "+filepath.Clean(newest.WorkspacePath)) + "\n")
		}
	}
	inner.WriteString("\n")
	note := lipgloss.NewStyle().Width(dialogWidth - 4).Render(
		"Each thread gets a brief of its own with its project and dates. The newest keeps its last 2 messages word for word.")
	for _, line := range strings.Split(note, "\n") {
		inner.WriteString(styleHint.Render("  "+line) + "\n")
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
		inner.WriteString(styleWarn.Render("  Merging needs an LLM: press L to set one up") + "\n")
	}
	inner.WriteString("\n")
	inner.WriteString(styleHint.Render("  ↑↓ navigate  Enter merge  L LLM setup  Esc back") + "\n")
	inner.WriteString("\n")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#444444")).
		Width(dialogWidth).
		Render(inner.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}
