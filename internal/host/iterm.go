package host

import (
	"context"
	"errors"
	"fmt"

	"github.com/paaaco/oversight/internal/iterm"
	"github.com/paaaco/oversight/internal/registry"
)

type itermHost struct{}

// ITerm returns the iTerm2 backend, implemented with the embedded AppleScripts.
func ITerm() Host { return itermHost{} }

func (itermHost) Name() string { return "iterm" }

func (itermHost) Key(s *registry.Session) string {
	if s.ITermSessionID == "" {
		return ""
	}
	return iterm.UUID(s.ITermSessionID)
}

func (itermHost) Locations(ctx context.Context) (map[string]Location, error) {
	tabs, err := iterm.Tabs(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Location, len(tabs))
	for uuid, l := range tabs {
		label := fmt.Sprintf("tab %d", l.Tab)
		if l.Window > 1 {
			label = fmt.Sprintf("w%d tab %d", l.Window, l.Tab)
		}
		out[uuid] = Location{Window: l.Window, Tab: l.Tab, Label: label}
	}
	return out, nil
}

func (itermHost) Focus(ctx context.Context, key string) error {
	err := iterm.Focus(ctx, key)
	if errors.Is(err, iterm.ErrMissing) {
		return ErrMissing
	}
	return err
}

func (itermHost) Notify(title, body string) { iterm.Notify(title, body) }
