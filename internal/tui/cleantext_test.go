package tui

import (
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/zuhailkhan/threadman/internal/domain"
)

// brokenLines returns the lines of a frame that would scramble a terminal:
// ones holding a control character, or wider than the screen.
func brokenLines(frame string, width int) []string {
	var bad []string
	for _, l := range strings.Split(frame, "\n") {
		if strings.ContainsFunc(ansi.Strip(l), unicode.IsControl) || ansi.StringWidth(l) > width {
			bad = append(bad, ansi.Strip(l))
		}
	}
	return bad
}

// Text saved with Windows line endings left a carriage return on every line,
// which sent the cursor back to the start of the row and scrambled the reader.
func TestReaderCleansControlCharacters(t *testing.T) {
	th := domain.Thread{Provider: "claude", Messages: []domain.Message{
		{Role: domain.RoleUser, Content: "## Summary\r\n\r\n- one\r\n- two\tcolumn"},
		{Role: domain.RoleAssistant, Content: "build \x1b[31mfailed\x1b[0m\x07 here", Tools: []domain.ToolCall{
			{Name: "Bash", Input: "go test\r\n./..."},
		}},
	}}
	text, prompts := layoutConversation([]domain.Thread{th}, 80)

	if bad := brokenLines(text, 80); len(bad) > 0 {
		t.Errorf("lines that would break the terminal: %q", bad)
	}
	for _, want := range []string{"## Summary", "- two    column", "build failed here", "▸ Bash  go test"} {
		if !strings.Contains(ansi.Strip(text), want) {
			t.Errorf("missing %q:\n%s", want, ansi.Strip(text))
		}
	}
	// \r\n is one line break, not two.
	if strings.Contains(ansi.Strip(text), "## Summary\n▎ \n▎ \n") {
		t.Errorf("a Windows line break was doubled:\n%s", ansi.Strip(text))
	}
	if len(prompts) != 1 || prompts[0].text != "## Summary" {
		t.Errorf("prompt = %+v", prompts)
	}
}

// A tool call's input was cut to 100 characters whatever the reader's width,
// so on a narrow reader it wrapped and pushed every line below it down.
func TestReaderLinesFitItsWidth(t *testing.T) {
	th := domain.Thread{Provider: "claude", Messages: []domain.Message{
		{Role: domain.RoleAssistant, Content: "ok", Tools: []domain.ToolCall{{Name: "Grep", Input: strings.Repeat("pattern ", 40)}}},
	}}
	text, _ := layoutConversation([]domain.Thread{th}, 50)
	if bad := brokenLines(text, 50); len(bad) > 0 {
		t.Errorf("lines wider than the reader: %q", bad)
	}
}

func TestTitlesAreOneCleanLine(t *testing.T) {
	th := domain.Thread{Title: "fix the\r\nparser\tnow \x1b[1mplease\x1b[0m"}
	if got := displayTitle(th); got != "fix the parser now please" {
		t.Errorf("title = %q", got)
	}
	if got := displayTitle(domain.Thread{Title: "\r\n\t"}); got != "(untitled)" {
		t.Errorf("blank title = %q", got)
	}
}
