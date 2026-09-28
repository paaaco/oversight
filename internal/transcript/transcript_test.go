package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `{"type":"last-prompt","sessionId":"x"}
{"type":"user","message":{"role":"user","content":"fix the rounding"},"isSidechain":false}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"On it."}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a b c"}]}}
{"type":"user","isSidechain":true,"message":{"role":"user","content":"subagent prompt"}}
not json at all
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done, confirm the migration?"}]}}
`

func TestTailKeepsOnlyText(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	_ = os.WriteFile(p, []byte(sample), 0o644)
	turns, err := Tail(p, 40)
	if err != nil {
		t.Fatal(err)
	}
	want := []Turn{
		{"user", "fix the rounding"},
		{"assistant", "On it."},
		{"assistant", "Done, confirm the migration?"},
	}
	if len(turns) != len(want) {
		t.Fatalf("got %d turns: %+v", len(turns), turns)
	}
	for i := range want {
		if turns[i] != want[i] {
			t.Fatalf("turn %d: got %+v want %+v", i, turns[i], want[i])
		}
	}
	n, _ := CountLines(p)
	if n != 8 {
		t.Fatalf("CountLines = %d", n)
	}
	if turns, _ = Tail(p, 1); len(turns) != 1 || turns[0].Role != "assistant" {
		t.Fatalf("maxTurns not honoured: %+v", turns)
	}
}

func TestRenderCaps(t *testing.T) {
	turns := []Turn{{"user", strings.Repeat("a", 100)}, {"assistant", strings.Repeat("b", 100)}}
	out := Render(turns, 60)
	if len(out) > 60 || !strings.Contains(out, "b") || strings.Contains(out, "a") {
		t.Fatalf("bad cap: %q", out)
	}
}

func TestTitle(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	_ = os.WriteFile(p, []byte(`{"type":"ai-title","aiTitle":"Tmux config symlink","sessionId":"x"}
{"type":"user","message":{"role":"user","content":"hi"}}
{"type":"custom-title","customTitle":"old name","sessionId":"x"}
{"type":"custom-title","customTitle":"splitwalletspr","sessionId":"x"}
{"type":"agent-color","agentColor":"cyan","sessionId":"x"}
{"type":"agent-color","agentColor":"Green","sessionId":"x"}
`), 0o644)
	if got := ReadMeta(p); got.Title != "splitwalletspr" || got.Color != "green" {
		t.Fatalf("Meta = %+v", got)
	}
	_ = os.WriteFile(p, []byte(`{"type":"ai-title","aiTitle":"Tmux config symlink","sessionId":"x"}`+"\n"), 0o644)
	if got := Title(p); got != "Tmux config symlink" {
		t.Fatalf("ai fallback = %q", got)
	}
	_ = os.WriteFile(p, []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"), 0o644)
	if got := Title(p); got != "" {
		t.Fatalf("no title = %q", got)
	}
	if got := Title(filepath.Join(t.TempDir(), "missing.jsonl")); got != "" {
		t.Fatalf("missing file = %q", got)
	}
}
