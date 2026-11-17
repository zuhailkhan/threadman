package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToolCall is one tool an assistant used during a turn, with what it got back.
type ToolCall struct {
	Name string `json:"name"`
	// Input summarizes the arguments: the command for a shell tool, compact
	// JSON otherwise.
	Input string `json:"input,omitempty"`
	// Output is the result text, shortened to MaxToolOutput characters.
	Output string `json:"output,omitempty"`
	Error  bool   `json:"error,omitempty"`
}

const (
	// MaxToolInput and MaxToolOutput bound what is kept of a tool call. A
	// file read or test run can return far more than is useful to carry
	// into another tool.
	MaxToolInput  = 500
	MaxToolOutput = 2000
)

// ToolInput summarizes a tool's arguments as stored by a provider. A command
// is shown as it was run; anything else as compact JSON.
func ToolInput(raw json.RawMessage) string {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return Shorten(s, MaxToolInput)
		}
		return Shorten(strings.TrimSpace(string(raw)), MaxToolInput)
	}
	return ToolInputFromMap(args)
}

// ToolInputFromMap is ToolInput for arguments already decoded.
func ToolInputFromMap(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	if cmd, ok := args["command"].(string); ok {
		return Shorten(cmd, MaxToolInput)
	}
	data, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return Shorten(string(data), MaxToolInput)
}

// Shorten cuts s to at most n characters, saying how much was left out.
func Shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf("… (%d more characters)", len(r)-n)
}

// WriteTools lays tool calls out as text, one per call: an arrow line with the
// tool and its input, then its output indented below. Input and output are
// cut to maxInput and maxOutput characters; maxOutput 0 leaves output out.
func WriteTools(b *strings.Builder, tools []ToolCall, maxInput, maxOutput int) {
	for _, call := range tools {
		b.WriteString("→ " + call.Name)
		if call.Error {
			b.WriteString(" (failed)")
		}
		if call.Input != "" {
			b.WriteString(": " + indent(Shorten(call.Input, maxInput), "  "))
		}
		b.WriteString("\n")
		if maxOutput > 0 && call.Output != "" {
			out := strings.TrimRight(Shorten(call.Output, maxOutput), "\n")
			b.WriteString("    " + indent(out, "    ") + "\n")
		}
	}
}

// indent prefixes every line after the first, so a multi-line value stays
// under the line it belongs to.
func indent(s, prefix string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// EncodeTools stores tool calls as JSON; no calls is the empty string.
func EncodeTools(tools []ToolCall) string {
	if len(tools) == 0 {
		return ""
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return ""
	}
	return string(data)
}

// DecodeTools reads tool calls stored by EncodeTools.
func DecodeTools(s string) []ToolCall {
	if s == "" {
		return nil
	}
	var tools []ToolCall
	if json.Unmarshal([]byte(s), &tools) != nil {
		return nil
	}
	return tools
}
