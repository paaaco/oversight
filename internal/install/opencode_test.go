package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritePluginCreatesAndRepoints(t *testing.T) {
	dir := t.TempDir()
	changed, err := WritePlugin(dir, "/usr/local/bin/oversight")
	if err != nil || !changed {
		t.Fatalf("first write: %v changed=%v", err, changed)
	}
	p := PluginPath(dir)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, `"/usr/local/bin/oversight"`) {
		t.Fatalf("binary path not embedded:\n%s", src)
	}
	for _, ev := range []string{"session.created", "message.updated", "message.part.updated", "permission.asked", "permission.replied", "question.asked", "question.replied", "session.idle", "session.deleted"} {
		if !strings.Contains(src, ev) {
			t.Fatalf("plugin does not handle %s", ev)
		}
	}
	for _, hook := range []string{"SessionStart", "UserPromptSubmit", "Notification", "PostToolUse", "Stop", "SessionEnd"} {
		if !strings.Contains(src, `"`+hook+`"`) {
			t.Fatalf("plugin never sends %s", hook)
		}
	}
	if !strings.Contains(src, "--agent") || !strings.Contains(src, "opencode") || !strings.Contains(src, "--pid") {
		t.Fatal("plugin must pass --agent opencode --pid")
	}
	if strings.Contains(src, "__OVERSIGHT__") {
		t.Fatal("template placeholder left in output")
	}

	changed, err = WritePlugin(dir, "/usr/local/bin/oversight")
	if err != nil || changed {
		t.Fatalf("same command should be a no-op: %v changed=%v", err, changed)
	}

	changed, err = WritePlugin(dir, "/opt/oversight")
	raw, _ = os.ReadFile(p)
	if err != nil || !changed || !strings.Contains(string(raw), `"/opt/oversight"`) || strings.Contains(string(raw), "/usr/local/bin") {
		t.Fatalf("repoint: %v changed=%v\n%s", err, changed, raw)
	}
}

func TestWritePluginEscapesCommand(t *testing.T) {
	dir := t.TempDir()
	if _, err := WritePlugin(dir, `/Users/pio "quoted"/oversight`); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(PluginPath(dir))
	if !strings.Contains(string(raw), `"/Users/pio \"quoted\"/oversight"`) {
		t.Fatalf("command not JSON-escaped:\n%s", raw)
	}
}

func TestPluginPath(t *testing.T) {
	if got := PluginPath("/c/opencode"); got != filepath.Join("/c/opencode", "plugins", "oversight.ts") {
		t.Fatal(got)
	}
}
