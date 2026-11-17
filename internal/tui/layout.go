package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// fit shortens plain text to at most w cells, ending it with "…". It counts
// cells, so wide characters are measured correctly.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// wrapText breaks plain text into lines of at most w cells, keeping its own
// line breaks.
func wrapText(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(para, w, ""), "\n")...)
	}
	return out
}

// row is one line of styled pieces. A selected row carries the selection
// background through every piece, since a terminal resets the background
// at the end of each styled run.
type row struct {
	b  strings.Builder
	w  int
	bg lipgloss.TerminalColor
}

func newRow(selected bool) *row {
	r := &row{}
	if selected {
		r.bg = colorSel
	}
	return r
}

func (r *row) add(st lipgloss.Style, s string) *row {
	if s == "" {
		return r
	}
	if r.bg != nil {
		st = st.Background(r.bg)
	}
	r.b.WriteString(st.Render(s))
	r.w += lipgloss.Width(s)
	return r
}

func (r *row) space(n int) *row {
	if n > 0 {
		r.add(lipgloss.NewStyle(), strings.Repeat(" ", n))
	}
	return r
}

// raw appends text that is already styled.
func (r *row) raw(s string) *row {
	r.b.WriteString(s)
	r.w += lipgloss.Width(s)
	return r
}

func (r *row) width() int { return r.w }

func (r *row) String() string { return r.b.String() }

// pad fills the row to w cells.
func (r *row) pad(w int) string {
	r.space(w - r.w)
	return r.b.String()
}

// spread places right at the far end of a w-cell line that starts with left.
// left must already fit beside right.
func spread(left, right *row, w int) string {
	left.space(w - left.w - right.w)
	return left.String() + right.String()
}

// padLine fills or cuts an already-styled line to exactly w cells.
func padLine(s string, w int) string {
	if n := lipgloss.Width(s); n > w {
		return ansi.Truncate(s, w, "")
	} else if n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// joinColumns lays blocks of lines side by side, each padded to its width,
// with sep between them, for exactly h lines.
func joinColumns(h int, sep string, widths []int, blocks ...[]string) []string {
	out := make([]string, h)
	for i := 0; i < h; i++ {
		var b strings.Builder
		for j, block := range blocks {
			line := ""
			if i < len(block) {
				line = block[i]
			}
			b.WriteString(padLine(line, widths[j]))
			if j < len(blocks)-1 {
				b.WriteString(sep)
			}
		}
		out[i] = b.String()
	}
	return out
}

// keyHint is one key and what it does, for footers and dialogs.
type keyHint struct{ key, does string }

func renderKeys(keys []keyHint) *row {
	r := &row{}
	for i, k := range keys {
		if i > 0 {
			r.space(2)
		}
		r.add(styleKey, " "+k.key+" ")
		r.add(styleHint, " "+k.does)
	}
	return r
}

// rule is a full-width divider, optionally with a note set into it.
func rule(w int, note string, noteStyle lipgloss.Style) string {
	if note == "" {
		return styleSep.Render(strings.Repeat("─", w))
	}
	note = fit(note, w-6)
	rest := w - 3 - lipgloss.Width(note) - 1
	if rest < 0 {
		rest = 0
	}
	return styleSep.Render("── ") + noteStyle.Render(note) + " " + styleSep.Render(strings.Repeat("─", rest))
}

// panel draws a dialog: a rounded border with the title set into the top
// edge. Lines wider than the panel are cut.
func panel(title string, titleStyle lipgloss.Style, border lipgloss.TerminalColor, lines []string, width int) string {
	bs := lipgloss.NewStyle().Foreground(border)
	inner := width - 4
	var b strings.Builder
	top := bs.Render("╭─ ") + titleStyle.Render(title) + " "
	b.WriteString(top + bs.Render(strings.Repeat("─", max(0, width-lipgloss.Width(top)-1))+"╮") + "\n")
	for _, l := range lines {
		b.WriteString(bs.Render("│ ") + padLine(l, inner) + bs.Render(" │") + "\n")
	}
	b.WriteString(bs.Render("╰" + strings.Repeat("─", width-2) + "╯"))
	return b.String()
}

// dialogWidth is a panel width that leaves a margin on narrow terminals.
func dialogWidth(want, screen int) int {
	if screen-4 < want {
		return max(24, screen-4)
	}
	return want
}

// overlay draws box centred over base, with base dimmed behind it.
func overlay(base, box string, width, height int) string {
	lines := strings.Split(base, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	lines = lines[:height]
	boxLines := strings.Split(box, "\n")
	bw := 0
	for _, l := range boxLines {
		bw = max(bw, lipgloss.Width(l))
	}
	x := max(0, (width-bw)/2)
	y := max(0, (height-len(boxLines))/2)

	for i, l := range lines {
		plain := padLine(ansi.Strip(l), width)
		if i < y || i >= y+len(boxLines) {
			lines[i] = styleFaint.Render(plain)
			continue
		}
		left := ansi.Truncate(plain, x, "")
		right := ansi.TruncateLeft(plain, x+bw, "")
		lines[i] = styleFaint.Render(left) + padLine(boxLines[i-y], bw) + styleFaint.Render(right)
	}
	return strings.Join(lines, "\n")
}

// centered places box in the middle of an empty screen.
func centered(box string, width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// choiceRow starts one selectable line in a list or dialog: a bar when it
// has the cursor. Build the rest into it so the selection background carries.
func choiceRow(selected bool) *row {
	r := newRow(selected)
	if selected {
		return r.add(styleCursor, "▌ ")
	}
	return r.space(2)
}

// sub starts a row that shares r's background, for its right-hand part.
func (r *row) sub() *row { return &row{bg: r.bg} }

// usableStyle is st, or faint when the choice can't be taken.
func usableStyle(usable bool, st lipgloss.Style) lipgloss.Style {
	if usable {
		return st
	}
	return styleFaint
}
