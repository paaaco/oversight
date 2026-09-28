package recap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paaaco/oversight/internal/registry"
)

func TestGenerateWritesAndDebounces(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	tp := filepath.Join(t.TempDir(), "t.jsonl")
	line := `{"type":"user","message":{"role":"user","content":"hello"}}` + "\n"
	_ = os.WriteFile(tp, []byte(strings.Repeat(line, 5)), 0o644)
	_ = registry.SaveSession(&registry.Session{SessionID: "s1", TranscriptPath: tp})

	calls := 0
	fake := func(ctx context.Context, system, body string) (string, error) {
		calls++
		if !strings.Contains(body, "User: hello") {
			t.Fatalf("body missing turns: %q", body)
		}
		return "  \"Doing things; needs a yes.\"\nextra line ignored", nil
	}
	if err := Generate("s1", false, fake); err != nil {
		t.Fatal(err)
	}
	r, err := registry.LoadRecap("s1")
	if err != nil || r.Recap != "Doing things; needs a yes." || r.TranscriptLines != 5 {
		t.Fatalf("recap: %v %+v", err, r)
	}
	// Fewer than 4 new lines: skipped, no call.
	_ = os.WriteFile(tp, []byte(strings.Repeat(line, 7)), 0o644)
	if err := Generate("s1", false, fake); !errors.Is(err, errSkipped) {
		t.Fatalf("want skip, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("summarizer called %d times", calls)
	}
	// Force ignores the debounce.
	if err := Generate("s1", true, fake); err != nil || calls != 2 {
		t.Fatalf("force: %v calls=%d", err, calls)
	}
	// A failing summarizer keeps the previous recap.
	_ = os.WriteFile(tp, []byte(strings.Repeat(line, 20)), 0o644)
	bad := func(context.Context, string, string) (string, error) { return "", errors.New("boom") }
	if err := Generate("s1", false, bad); err == nil {
		t.Fatal("expected error")
	}
	r, _ = registry.LoadRecap("s1")
	if r.Recap != "Doing things; needs a yes." {
		t.Fatalf("previous recap lost: %+v", r)
	}
}

func TestClean(t *testing.T) {
	long := strings.Repeat("x", 200)
	if got := Clean(long); len([]rune(got)) != 120 || !strings.HasSuffix(got, "…") {
		t.Fatalf("bad truncation: %q", got)
	}
}
