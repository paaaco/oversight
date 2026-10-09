package host

import (
	"testing"

	"github.com/paaaco/oversight/internal/registry"
)

func TestDetect(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	if h := Detect(env(map[string]string{"TMUX": "/tmp/tmux-501/default,123,0"}), "linux"); h == nil || h.Name() != "tmux" {
		t.Fatalf("TMUX set should pick tmux, got %v", h)
	}
	if h := Detect(env(map[string]string{"TMUX": "/x", "ITERM_SESSION_ID": "w0t0p0:A"}), "darwin"); h == nil || h.Name() != "tmux" {
		t.Fatalf("tmux inside iTerm should pick tmux, got %v", h)
	}
	if h := Detect(env(map[string]string{"ITERM_SESSION_ID": "w0t0p0:A"}), "darwin"); h == nil || h.Name() != "iterm" {
		t.Fatalf("plain iTerm should pick iterm, got %v", h)
	}
	if h := Detect(env(map[string]string{}), "darwin"); h == nil || h.Name() != "iterm" {
		t.Fatalf("darwin without env still offers iterm (Terminal.app users get 'not an iTerm session'), got %v", h)
	}
	if h := Detect(env(map[string]string{}), "linux"); h != nil {
		t.Fatalf("linux without tmux should have no host, got %v", h)
	}
}

func TestITermKey(t *testing.T) {
	h := ITerm()
	if h.Name() != "iterm" {
		t.Fatal(h.Name())
	}
	if h.Key(&registry.Session{ITermSessionID: "w0t1p0:UUID-A", TmuxPane: "%1"}) != "UUID-A" {
		t.Fatal("iterm key should be the UUID after the colon")
	}
	if h.Key(&registry.Session{TmuxPane: "%1"}) != "" {
		t.Fatal("no iterm id means no key")
	}
}
