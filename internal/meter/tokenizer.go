package meter

import (
	"encoding/json"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Counter is deliberately an interface: P0 ships a deterministic heuristic,
// and an exact HF-tokenizers implementation can be swapped in per model
// without touching the billing pipeline.
type Counter struct {
	Family string
}

func (c Counter) CountText(s string) int64 {
	if s == "" {
		return 0
	}
	var ascii, cjk, other, spaces int64
	for _, r := range s {
		switch {
		case r == ' ' || r == '\n' || r == '\t' || r == '\r':
			spaces++
		case r < utf8.RuneSelf:
			ascii++
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r), unicode.Is(unicode.Hangul, r):
			cjk++
		default:
			other++
		}
	}
	// Latin text is roughly 4 chars/token; CJK is roughly 1 char/token.
	tokens := cjk + other + int64(math.Ceil(float64(ascii)/4.0))
	if spaces > 0 && tokens == 0 {
		tokens = 1
	}
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// CountMessages tokenizes the fully serialized prompt, including tool schemas
// and tool results, and applies the per-message envelope overhead used by
// OpenAI-compatible chat templates.
func (c Counter) CountMessages(messages []json.RawMessage, tools json.RawMessage) int64 {
	var total int64
	for _, raw := range messages {
		total += 4
		var m struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCalls  json.RawMessage `json:"tool_calls"`
			ToolCallID string          `json:"tool_call_id"`
			Name       string          `json:"name"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			total += c.CountText(string(raw))
			continue
		}
		total += c.CountText(m.Role)
		total += c.CountText(extractText(m.Content))
		total += c.CountText(string(m.ToolCalls))
		total += c.CountText(m.ToolCallID) + c.CountText(m.Name)
	}
	if len(tools) > 2 {
		total += c.CountText(string(tools))
	}
	return total + 3
}

func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

type ToolText struct {
	Name string
	Args string
}

func (c Counter) CountOutput(content, reasoning string, tools []ToolText) int64 {
	total := c.CountText(content) + c.CountText(reasoning)
	for _, t := range tools {
		total += c.CountText(t.Name) + c.CountText(t.Args)
	}
	if total == 0 && (content != "" || reasoning != "") {
		total = 1
	}
	return total
}

func Calibrate(local int64, k float64) int64 {
	if local <= 0 {
		return 0
	}
	if k <= 0 {
		k = 1
	}
	return int64(math.Round(float64(local) * k))
}

func Clamp(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
