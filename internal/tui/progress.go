package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zuhailkhan/threadman/internal/handoff"
)

// While a handoff or merge runs, the summarizer's progress comes over a
// channel and a spinner ticks, so a long summary on a local model visibly
// moves.

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinInterval = 120 * time.Millisecond

// progressMsg carries one progress report from the running work.
type progressMsg struct {
	p  handoff.Progress
	ch chan handoff.Progress
}

// spinMsg advances the spinner of the work started at start.
type spinMsg struct{ start time.Time }

// startWork runs work with progress reporting, and starts the spinner.
func (m *Model) startWork(ctx context.Context, work func(context.Context) tea.Cmd) tea.Cmd {
	ch := make(chan handoff.Progress, 1)
	m.progressCh = ch
	m.progress = handoff.Progress{}
	m.progressSeen = false
	m.workStart = time.Now()
	m.spin = 0
	ctx = handoff.WithProgress(ctx, func(p handoff.Progress) {
		// Keep only the newest report: the screen shows where the work is,
		// not every step it took.
		for {
			select {
			case ch <- p:
				return
			default:
			}
			select {
			case <-ch:
			default:
			}
		}
	})
	run := work(ctx)
	return tea.Batch(
		func() tea.Msg {
			defer close(ch)
			return run()
		},
		listenProgress(ch),
		spinTick(m.workStart),
	)
}

func listenProgress(ch chan handoff.Progress) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return nil
		}
		return progressMsg{p: p, ch: ch}
	}
}

func spinTick(start time.Time) tea.Cmd {
	return tea.Tick(spinInterval, func(time.Time) tea.Msg { return spinMsg{start: start} })
}

// elapsed is how long the work has run, as m:ss.
func (m Model) elapsed() string {
	d := time.Duration(m.spin) * spinInterval
	if !m.workStart.IsZero() {
		if real := time.Since(m.workStart); real > d {
			d = real
		}
	}
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// progressBar is a bar of width cells, filled for done of total.
func progressBar(width, done, total int) string {
	if total <= 0 || width <= 0 {
		return ""
	}
	filled := min(width, width*done/total)
	return styleAccent.Render(strings.Repeat("━", filled)) + styleFaint.Render(strings.Repeat("─", width-filled))
}

// workingLines are the lines of the working step: the spinner with what is
// being done, and how far it has got.
func (m Model) workingLines(headline string, iw int) []string {
	frame := spinnerFrames[m.spin%len(spinnerFrames)]
	head := (&row{}).add(styleCursor, frame+" ").add(styleBold, fit(headline, iw-10)).add(styleFaint, "  "+m.elapsed())
	lines := []string{head.String()}

	p := m.progress
	// The label takes 24 cells and the count after the bar up to 11.
	barW := max(10, min(36, iw-24-11))
	if p.Threads > 0 {
		label := fmt.Sprintf("Thread %d of %d", p.Thread, p.Threads)
		lines = append(lines, "", padLine(styleHint.Render(label), 24)+progressBar(barW, p.Done, p.Threads)+styleFaint.Render(fmt.Sprintf(" %d/%d done", p.Done, p.Threads)))
	}
	switch {
	case p.Parts > 0 && p.Stage == handoff.StageReading:
		lines = append(lines, "", padLine(styleHint.Render(fmt.Sprintf("Reading part %d of %d", p.Part, p.Parts)), 24)+progressBar(barW, p.Part-1, p.Parts))
		lines = append(lines, wrapStyled("The conversation is longer than the model's context, so it is read in parts.", iw, styleFaint)...)
	case p.Parts > 0 && p.Stage == handoff.StageCondensing:
		lines = append(lines, "", padLine(styleHint.Render(fmt.Sprintf("Condensing notes %d of %d", p.Part, p.Parts)), 24)+progressBar(barW, p.Part-1, p.Parts))
	case p.Stage == handoff.StageWriting && m.progressSeen:
		lines = append(lines, "", styleHint.Render("Writing the brief"))
	}
	return lines
}
