// Package host abstracts the terminal multiplexer or emulator the sessions
// live in, so the dashboard can label and focus panes the same way whether
// they are iTerm2 tabs or tmux panes.
package host

import (
	"context"
	"errors"

	"github.com/paaaco/oversight/internal/registry"
)

// Location is where a pane sits, plus the label the dashboard prints for it.
type Location struct {
	Window, Tab int    // iTerm: window and tab index; tmux: window index and pane index
	Label       string // "tab 3", "w2 tab 1", "dev:2.1"
}

// ErrMissing means no pane with that key exists any more.
var ErrMissing = errors.New("pane missing")

// Host is one terminal backend.
type Host interface {
	// Name is "iterm" or "tmux"; it appears in user-facing messages.
	Name() string
	// Key returns the stored id this host addresses a session by, or "".
	Key(s *registry.Session) string
	// Locations lists every pane the host knows about, keyed like Key.
	Locations(ctx context.Context) (map[string]Location, error)
	// Focus brings the pane to the front. ErrMissing when it is gone.
	Focus(ctx context.Context, key string) error
	// Notify shows a desktop notification, or does nothing if it cannot.
	Notify(title, body string)
}

// Detect picks the host for the dashboard's own environment: tmux when the
// dashboard runs inside tmux, iTerm on macOS otherwise, nil elsewhere.
func Detect(getenv func(string) string, goos string) Host {
	if getenv("TMUX") != "" {
		return Tmux()
	}
	if goos == "darwin" {
		return ITerm()
	}
	return nil
}
