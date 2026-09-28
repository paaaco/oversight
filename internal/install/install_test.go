package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunIsIdempotentAndKeepsOtherHooks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	orig := `{"model":"opus","hooks":{"Notification":[{"hooks":[{"type":"command","command":"/x/cc-status"}]}]}}`
	_ = os.WriteFile(p, []byte(orig), 0o600)

	changed, err := Run(p, "/usr/local/bin/oversight")
	if err != nil || !changed {
		t.Fatalf("first run: %v changed=%v", err, changed)
	}
	changed, err = Run(p, "/usr/local/bin/oversight")
	if err != nil || changed {
		t.Fatalf("second run should be a no-op: %v changed=%v", err, changed)
	}
	raw, _ := os.ReadFile(p)
	var s map[string]any
	_ = json.Unmarshal(raw, &s)
	if s["model"] != "opus" {
		t.Fatal("other settings lost")
	}
	hooks := s["hooks"].(map[string]any)
	for _, ev := range Events {
		if !strings.Contains(string(raw), "oversight hook "+ev) {
			t.Fatalf("missing hook for %s", ev)
		}
		if _, ok := hooks[ev]; !ok {
			t.Fatalf("no %s group", ev)
		}
	}
	if !strings.Contains(string(raw), "/x/cc-status") {
		t.Fatal("existing Notification hook removed")
	}
	if len(hooks["Notification"].([]any)) != 2 {
		t.Fatalf("Notification groups: %v", hooks["Notification"])
	}
	post := hooks["PostToolUse"].([]any)[0].(map[string]any)
	if post["matcher"] != "*" {
		t.Fatal("PostToolUse needs matcher *")
	}
	backups, _ := filepath.Glob(p + ".bak-*")
	if len(backups) != 1 {
		t.Fatalf("want exactly one backup, got %v", backups)
	}

	// Moving the binary repoints the commands without duplicating them.
	changed, _ = Run(p, "/opt/oversight")
	raw, _ = os.ReadFile(p)
	if !changed || strings.Contains(string(raw), "/usr/local/bin/oversight") || strings.Count(string(raw), "hook Stop") != 1 {
		t.Fatalf("repoint failed: %s", raw)
	}
}

func TestRunCreatesMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "settings.json")
	if _, err := Run(p, "oversight"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("file not created")
	}
}
