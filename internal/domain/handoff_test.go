package domain

import "testing"

func TestHandoffMarkerRoundTrip(t *testing.T) {
	line := HandoffMarker("3f2a-11", "from Claude Code · full transcript · 4 messages")
	if line != "[threadman handoff 3f2a-11 · from Claude Code · full transcript · 4 messages]" {
		t.Fatalf("marker = %q", line)
	}
	for _, content := range []string{line, line + "\n\nbody", line + "\r\nbody"} {
		id, detail, ok := ParseHandoffMarker(content)
		if !ok || id != "3f2a-11" || detail != "from Claude Code · full transcript · 4 messages" {
			t.Errorf("ParseHandoffMarker(%q) = %q, %q, %v", content, id, detail, ok)
		}
	}
	if id, detail, ok := ParseHandoffMarker(HandoffMarker("abc", "")); !ok || id != "abc" || detail != "" {
		t.Errorf("marker without detail: %q, %q, %v", id, detail, ok)
	}
}

func TestParseHandoffMarkerRejects(t *testing.T) {
	for _, content := range []string{
		"",
		"hello",
		"see [threadman handoff abc] above", // not at the start
		"[threadman handoff abc",            // unterminated
		"[threadman handoff ]",              // no ID
		"[threadman handoff a b]",           // ID with a space
		"text\n[threadman handoff abc · from pi]", // on a later line
	} {
		if _, _, ok := ParseHandoffMarker(content); ok {
			t.Errorf("ParseHandoffMarker(%q) accepted it", content)
		}
	}
}

func TestIsHandoffMarkerCatchesTruncatedTitle(t *testing.T) {
	if !IsHandoffMarker("[threadman handoff 3f2a-11 · from Cla...") {
		t.Error("a truncated marker title was not recognised")
	}
	if IsHandoffMarker("Fix the parser") {
		t.Error("an ordinary title was taken for a marker")
	}
}
