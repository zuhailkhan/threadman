package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestToolInput(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`{"command":"go test ./...","description":"run tests"}`, "go test ./..."},
		{`{"file_path":"/a/b.go","limit":20}`, `{"file_path":"/a/b.go","limit":20}`},
		{`"plain text"`, "plain text"},
		{`{}`, ""},
		{``, ""},
	}
	for _, c := range cases {
		if got := ToolInput(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("ToolInput(%s) = %q, want %q", c.raw, got, c.want)
		}
	}
	long := ToolInput(json.RawMessage(`{"command":"` + strings.Repeat("x", MaxToolInput+10) + `"}`))
	if !strings.HasSuffix(long, "… (10 more characters)") {
		t.Errorf("long input not shortened: ...%q", long[len(long)-30:])
	}
}

func TestShortenCountsCharacters(t *testing.T) {
	if got := Shorten("héllo wörld", 5); got != "héllo… (6 more characters)" {
		t.Errorf("Shorten = %q", got)
	}
	if got := Shorten("short", 10); got != "short" {
		t.Errorf("Shorten = %q", got)
	}
}

func TestEncodeDecodeTools(t *testing.T) {
	tools := []ToolCall{{Name: "Bash", Input: "ls", Output: "a\nb", Error: true}, {Name: "Read"}}
	if got := DecodeTools(EncodeTools(tools)); !reflect.DeepEqual(got, tools) {
		t.Errorf("round trip = %+v", got)
	}
	if EncodeTools(nil) != "" || DecodeTools("") != nil || DecodeTools("not json") != nil {
		t.Error("empty or broken values should map to no calls")
	}
}

func TestWriteTools(t *testing.T) {
	var b strings.Builder
	WriteTools(&b, []ToolCall{
		{Name: "Bash", Input: "go test\n  ./...", Output: "ok\r\nPASS\n"},
		{Name: "Edit", Input: `{"file_path":"a.go"}`, Error: true, Output: "no such file"},
		{Name: "Glob"},
	}, 100, 100)
	want := "→ Bash: go test\n    ./...\n" +
		"    ok\n    PASS\n" +
		"→ Edit (failed): {\"file_path\":\"a.go\"}\n" +
		"    no such file\n" +
		"→ Glob\n"
	if b.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), want)
	}

	b.Reset()
	WriteTools(&b, []ToolCall{{Name: "Bash", Input: "ls", Output: "secret"}}, 100, 0)
	if strings.Contains(b.String(), "secret") {
		t.Errorf("output written with maxOutput 0: %q", b.String())
	}
}
