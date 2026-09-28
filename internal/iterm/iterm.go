// Package iterm talks to iTerm2 through embedded AppleScripts.
package iterm

import (
	"context"
	_ "embed"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

//go:embed focus.applescript
var focusScript string

//go:embed tabs.applescript
var tabsScript string

// ErrMissing means no pane with that UUID exists any more.
var ErrMissing = errors.New("iterm session missing")

// UUID returns the stable part of an ITERM_SESSION_ID (after the colon).
func UUID(itermSessionID string) string {
	if i := strings.Index(itermSessionID, ":"); i >= 0 {
		return itermSessionID[i+1:]
	}
	return itermSessionID
}

// Focus brings the pane with the given UUID to the front.
func Focus(ctx context.Context, uuid string) error {
	out, err := run(ctx, focusScript, uuid)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "ok" {
		return ErrMissing
	}
	return nil
}

// Location is a pane's window and tab index, 1-based.
type Location struct{ Window, Tab int }

// Tabs returns every pane's location keyed by UUID, in one AppleScript call.
func Tabs(ctx context.Context) (map[string]Location, error) {
	out, err := run(ctx, tabsScript)
	if err != nil {
		return nil, err
	}
	m := map[string]Location{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) != 3 {
			continue
		}
		w, err1 := strconv.Atoi(f[1])
		t, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil {
			continue
		}
		m[f[0]] = Location{Window: w, Tab: t}
	}
	return m, nil
}

// Notify shows a macOS notification.
func Notify(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	script := "display notification " + quote(body) + " with title " + quote(title)
	_ = exec.CommandContext(ctx, "osascript", "-e", script).Run()
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func run(ctx context.Context, script string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "osascript", append([]string{"-"}, args...)...)
	cmd.Stdin = strings.NewReader(script)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return string(out), err
}
