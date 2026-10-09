package host

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/paaaco/oversight/internal/iterm"
	"github.com/paaaco/oversight/internal/registry"
)

// Runner executes tmux with the given arguments and returns stdout.
type Runner func(ctx context.Context, args ...string) (string, error)

const tmuxTimeout = 4 * time.Second

// listFormat keeps session names last-but-not-least: names may contain
// colons or spaces, so fields are tab separated and parsed by position.
const listFormat = "#{pane_id}\t#{session_name}\t#{window_index}\t#{pane_index}"

type tmuxHost struct{ run Runner }

// Tmux returns the tmux backend using the tmux binary on PATH.
func Tmux() Host { return NewTmux(execTmux) }

// NewTmux returns a tmux backend with a custom runner, for tests.
func NewTmux(run Runner) Host { return tmuxHost{run: run} }

func execTmux(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, tmuxTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("tmux %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("tmux %s: %w", args[0], err)
	}
	return string(out), nil
}

func (tmuxHost) Name() string { return "tmux" }

func (tmuxHost) Key(s *registry.Session) string { return s.TmuxPane }

func (h tmuxHost) Locations(ctx context.Context) (map[string]Location, error) {
	out, err := h.run(ctx, "list-panes", "-a", "-F", listFormat)
	if err != nil {
		return nil, err
	}
	return parsePanes(out), nil
}

func parsePanes(out string) map[string]Location {
	m := map[string]Location{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) != 4 || !strings.HasPrefix(f[0], "%") {
			continue
		}
		w, err1 := strconv.Atoi(f[2])
		p, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			continue
		}
		m[f[0]] = Location{Window: w, Tab: p, Label: fmt.Sprintf("%s:%d.%d", f[1], w, p)}
	}
	return m
}

// Focus switches the attached client to the pane's session, then selects
// its window and the pane itself. switch-client fails when the dashboard is
// not itself inside tmux; that is fine, the other two still move the client
// attached to the target session.
func (h tmuxHost) Focus(ctx context.Context, key string) error {
	out, err := h.run(ctx, "list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		return err
	}
	found := false
	for _, id := range strings.Split(out, "\n") {
		if strings.TrimRight(strings.SplitN(id, "\t", 2)[0], "\r") == key {
			found = true
			break
		}
	}
	if !found {
		return ErrMissing
	}
	_, _ = h.run(ctx, "switch-client", "-t", key)
	if _, err := h.run(ctx, "select-window", "-t", key); err != nil {
		return err
	}
	if _, err := h.run(ctx, "select-pane", "-t", key); err != nil {
		return err
	}
	return nil
}

// Notify uses notify-send where it exists (Linux desktops), osascript on
// macOS, and stays silent otherwise.
func (tmuxHost) Notify(title, body string) {
	if runtime.GOOS == "darwin" {
		iterm.Notify(title, body)
		return
	}
	if _, err := exec.LookPath("notify-send"); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "notify-send", title, body).Run()
}
