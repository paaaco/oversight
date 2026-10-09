package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/paaaco/oversight/internal/registry"
)

// fakeRunner records tmux invocations and replies from a script keyed by the
// first argument (the tmux subcommand).
type fakeRunner struct {
	calls   [][]string
	replies map[string]string
	errs    map[string]error
}

func (f *fakeRunner) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if err := f.errs[args[0]]; err != nil {
		return "", err
	}
	return f.replies[args[0]], nil
}

const listing = "%0\tdev\t1\t0\n%12\tdev\t2\t1\n%7\twork: api\t0\t0\n"

func TestTmuxLocationsParsesOddNames(t *testing.T) {
	f := &fakeRunner{replies: map[string]string{"list-panes": listing}}
	h := NewTmux(f.run)
	locs, err := h.Locations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 3 {
		t.Fatalf("want 3 panes, got %v", locs)
	}
	if l := locs["%12"]; l.Window != 2 || l.Tab != 1 || l.Label != "dev:2.1" {
		t.Fatalf("%%12 parsed as %+v", l)
	}
	if l := locs["%7"]; l.Label != "work: api:0.0" {
		t.Fatalf("session name with colon and space mangled: %+v", l)
	}
	args := f.calls[0]
	if args[0] != "list-panes" || args[1] != "-a" || args[2] != "-F" || !strings.Contains(args[3], "#{pane_id}") {
		t.Fatalf("unexpected list-panes args: %v", args)
	}
}

func TestTmuxLocationsSkipsGarbageLines(t *testing.T) {
	f := &fakeRunner{replies: map[string]string{"list-panes": "garbage\n%1\tdev\tx\t0\n%2\tdev\t3\t4\n"}}
	locs, err := NewTmux(f.run).Locations(context.Background())
	if err != nil || len(locs) != 1 || locs["%2"].Label != "dev:3.4" {
		t.Fatalf("got %v %v", locs, err)
	}
}

func TestTmuxFocusSequence(t *testing.T) {
	f := &fakeRunner{replies: map[string]string{"list-panes": listing}}
	if err := NewTmux(f.run).Focus(context.Background(), "%12"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"list-panes", "-a", "-F", "#{pane_id}"},
		{"switch-client", "-t", "%12"},
		{"select-window", "-t", "%12"},
		{"select-pane", "-t", "%12"},
	}
	if len(f.calls) != len(want) {
		t.Fatalf("calls: %v", f.calls)
	}
	for i := range want {
		if f.calls[i][0] != want[i][0] || f.calls[i][len(f.calls[i])-1] != want[i][len(want[i])-1] {
			t.Fatalf("call %d = %v, want %v", i, f.calls[i], want[i])
		}
	}
}

func TestTmuxFocusMissingPane(t *testing.T) {
	f := &fakeRunner{replies: map[string]string{"list-panes": listing}}
	err := NewTmux(f.run).Focus(context.Background(), "%99")
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("want ErrMissing, got %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("should not try to select a missing pane: %v", f.calls)
	}
}

func TestTmuxFocusIgnoresSwitchClientFailure(t *testing.T) {
	// Dashboard not inside tmux: switch-client fails, the rest still runs.
	f := &fakeRunner{replies: map[string]string{"list-panes": listing}, errs: map[string]error{"switch-client": errors.New("no current client")}}
	if err := NewTmux(f.run).Focus(context.Background(), "%12"); err != nil {
		t.Fatalf("switch-client failure should be ignored: %v", err)
	}
	if len(f.calls) != 4 {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestTmuxFocusPropagatesRunnerError(t *testing.T) {
	f := &fakeRunner{replies: map[string]string{"list-panes": listing}, errs: map[string]error{"select-window": errors.New("can't find pane")}}
	err := NewTmux(f.run).Focus(context.Background(), "%12")
	if err == nil || errors.Is(err, ErrMissing) || !strings.Contains(err.Error(), "can't find pane") {
		t.Fatalf("want the tmux error surfaced, got %v", err)
	}
}

func TestTmuxKeyAndName(t *testing.T) {
	h := NewTmux((&fakeRunner{}).run)
	if h.Name() != "tmux" {
		t.Fatal(h.Name())
	}
	if h.Key(&registry.Session{TmuxPane: "%3", ITermSessionID: "w0t1p0:X"}) != "%3" {
		t.Fatal("tmux host should address the tmux pane")
	}
	if h.Key(&registry.Session{ITermSessionID: "w0t1p0:X"}) != "" {
		t.Fatal("no tmux pane means no key")
	}
}
