// Package transcript reads the tail of a Claude Code JSONL transcript.
//
// The format is internal to Claude Code and changes between versions, so this
// package is deliberately lenient: lines it does not understand are skipped
// and no parse failure is ever fatal.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// Turn is one user or assistant text message.
type Turn struct {
	Role string
	Text string
}

const tailBytes = 1 << 20 // read at most the last 1 MiB

// CountLines counts newline-terminated lines in the file.
func CountLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	n := 0
	for {
		c, err := f.Read(buf)
		n += bytes.Count(buf[:c], []byte{'\n'})
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// Tail returns up to maxTurns of the most recent user/assistant text turns.
func Tail(path string, maxTurns int) ([]Turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if st.Size() > tailBytes {
		start = st.Size() - tailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	var turns []Turn
	first := start > 0 // first line may be partial when we seeked mid-file
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		if t, ok := ParseLine(sc.Bytes()); ok {
			turns = append(turns, t)
		}
	}
	if len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
	}
	return turns, nil
}

type line struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ParseLine extracts a text turn from one JSONL line. Tool calls, tool results,
// thinking blocks, sidechain (subagent) entries and metadata lines yield ok=false.
func ParseLine(raw []byte) (Turn, bool) {
	var l line
	if err := json.Unmarshal(raw, &l); err != nil {
		return Turn{}, false
	}
	if l.IsSidechain || (l.Type != "user" && l.Type != "assistant") {
		return Turn{}, false
	}
	text := contentText(l.Message.Content)
	text = strings.TrimSpace(text)
	if text == "" {
		return Turn{}, false
	}
	return Turn{Role: l.Type, Text: text}, true
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []block
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Render flattens turns into a prompt body capped at maxChars, keeping the
// most recent text when it has to cut.
func Render(turns []Turn, maxChars int) string {
	var sb strings.Builder
	for _, t := range turns {
		sb.WriteString(strings.ToUpper(t.Role[:1]) + t.Role[1:] + ": ")
		sb.WriteString(t.Text)
		sb.WriteString("\n\n")
	}
	s := sb.String()
	if len(s) > maxChars {
		s = s[len(s)-maxChars:]
	}
	return strings.TrimSpace(s)
}

const metaTailBytes = 64 * 1024

// Meta is what /rename and /color leave in the transcript.
type Meta struct {
	Title string // custom-title (/rename), else ai-title (auto), else ""
	Color string // agent-color (/color) name such as "green", else ""
}

// Title is Meta(path).Title.
func Title(path string) string { return ReadMeta(path).Title }

// ReadMeta scans the transcript tail for the newest title and color lines.
// Claude Code re-appends them roughly every turn, so the tail is enough.
func ReadMeta(path string) Meta {
	var m Meta
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return m
	}
	start := int64(0)
	if st.Size() > metaTailBytes {
		start = st.Size() - metaTailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return m
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	var ai string
	for sc.Scan() {
		raw := sc.Bytes()
		if !bytes.Contains(raw, []byte(`-title"`)) && !bytes.Contains(raw, []byte(`-color"`)) {
			continue
		}
		var l struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
			AgentColor  string `json:"agentColor"`
		}
		if err := json.Unmarshal(raw, &l); err != nil {
			continue
		}
		switch l.Type {
		case "custom-title":
			if t := strings.TrimSpace(l.CustomTitle); t != "" {
				m.Title = t
			}
		case "ai-title":
			if t := strings.TrimSpace(l.AITitle); t != "" {
				ai = t
			}
		case "agent-color":
			if c := strings.ToLower(strings.TrimSpace(l.AgentColor)); c != "" {
				m.Color = c
			}
		}
	}
	if m.Title == "" {
		m.Title = ai
	}
	return m
}
