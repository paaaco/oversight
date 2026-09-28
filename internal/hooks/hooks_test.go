package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/paaaco/oversight/internal/registry"
	"github.com/paaaco/oversight/internal/transcript"
)

func testEnv(spawned *[]string) Env {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	return Env{
		ITermSessionID: "w0t3p0:UUID-A",
		ClaudePID:      func() int { return 4242 },
		Branch:         func(string) string { return "fiscal-receipts" },
		SpawnRecap:     func(id string) { *spawned = append(*spawned, id) },
		Now:            func() time.Time { return now },
		Meta:           func(path string) transcript.Meta { return metas[path] },
	}
}

var metas = map[string]transcript.Meta{}

func TestLifecycle(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	var spawned []string
	env := testEnv(&spawned)
	in := Input{SessionID: "s1", CWD: "/x/salonized", TranscriptPath: "/t/s1.jsonl"}

	must(t, Handle("SessionStart", in, env))
	s, err := registry.LoadSession("s1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Repo != "salonized" || s.Branch != "fiscal-receipts" || s.PID != 4242 || s.ITermSessionID != "w0t3p0:UUID-A" || s.Status != registry.StatusWaiting {
		t.Fatalf("bad session: %+v", s)
	}

	in.Prompt = strings.Repeat("x", 300)
	metas["/t/s1.jsonl"] = transcript.Meta{Title: "splitwalletspr", Color: "green"}
	must(t, Handle("UserPromptSubmit", in, env))
	s, _ = registry.LoadSession("s1")
	if s.Status != registry.StatusWorking || len([]rune(s.LastPrompt)) != 120 || s.Title != "splitwalletspr" || s.Color != "green" {
		t.Fatalf("after prompt: %+v", s)
	}
	// A transcript without title/color lines keeps the last known values.
	metas["/t/s1.jsonl"] = transcript.Meta{}
	must(t, Handle("Stop", Input{SessionID: "s1", TranscriptPath: "/t/s1.jsonl"}, env))
	s, _ = registry.LoadSession("s1")
	if s.Title != "splitwalletspr" || s.Color != "green" {
		t.Fatalf("title/color lost: %+v", s)
	}
	must(t, Handle("UserPromptSubmit", Input{SessionID: "s1", Prompt: "again"}, env))

	must(t, Handle("Notification", Input{SessionID: "s1", NotificationType: "permission_prompt"}, env))
	s, _ = registry.LoadSession("s1")
	if s.Status != registry.StatusApproval {
		t.Fatalf("want approval, got %s", s.Status)
	}

	must(t, Handle("PostToolUse", Input{SessionID: "s1"}, env))
	s, _ = registry.LoadSession("s1")
	if s.Status != registry.StatusWorking {
		t.Fatalf("want working after approval, got %s", s.Status)
	}
	before := s.UpdatedAt
	must(t, Handle("PostToolUse", Input{SessionID: "s1"}, env))
	s, _ = registry.LoadSession("s1")
	if !s.UpdatedAt.Equal(before) {
		t.Fatal("PostToolUse wrote although status was not approval")
	}

	must(t, Handle("Notification", Input{SessionID: "s1", NotificationType: "auth_success"}, env))
	s, _ = registry.LoadSession("s1")
	if s.Status != registry.StatusWorking {
		t.Fatalf("unrelated notification changed status to %s", s.Status)
	}

	must(t, Handle("Stop", Input{SessionID: "s1", TranscriptPath: "/t/s1.jsonl"}, env))
	s, _ = registry.LoadSession("s1")
	if s.Status != registry.StatusWaiting || len(spawned) != 2 || spawned[1] != "s1" {
		t.Fatalf("after stop: %+v spawned=%v", s, spawned)
	}

	must(t, Handle("Notification", Input{SessionID: "s1", Message: "Claude needs your permission to use Bash"}, env))
	s, _ = registry.LoadSession("s1")
	if s.Status != registry.StatusApproval {
		t.Fatalf("message fallback failed: %s", s.Status)
	}

	must(t, Handle("SessionEnd", Input{SessionID: "s1", Reason: "other"}, env))
	if _, err := registry.LoadSession("s1"); err == nil {
		t.Fatal("session not deleted")
	}
}

func TestSessionStartReplacesSameTab(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	var spawned []string
	env := testEnv(&spawned)
	must(t, Handle("SessionStart", Input{SessionID: "old", CWD: "/x"}, env))
	must(t, Handle("SessionStart", Input{SessionID: "new", CWD: "/x", Source: "clear"}, env))
	list, _ := registry.ListSessions()
	if len(list) != 1 || list[0].SessionID != "new" {
		t.Fatalf("want only new, got %+v", list)
	}
}

func TestEventsWithoutSessionStartAreIgnored(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	var spawned []string
	env := testEnv(&spawned)
	must(t, Handle("UserPromptSubmit", Input{SessionID: "ghost", Prompt: "hi"}, env))
	list, _ := registry.ListSessions()
	if len(list) != 0 {
		t.Fatalf("ghost session created: %+v", list)
	}
}

func TestSubagentEventsIgnored(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	var spawned []string
	env := testEnv(&spawned)
	must(t, Handle("SessionStart", Input{SessionID: "s1", CWD: "/x"}, env))
	must(t, Handle("Stop", Input{SessionID: "s1", AgentID: "agent-1"}, env))
	if len(spawned) != 0 {
		t.Fatal("subagent Stop spawned a recap")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
