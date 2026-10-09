# tmux and OpenCode Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run oversight inside tmux on Linux (and macOS), tracking Claude Code and OpenCode sessions in one list, with Enter jumping to the right tmux pane, while the existing iTerm2 mode keeps working.

**Architecture:** A small `internal/host` interface fronts two terminal backends (the existing `iterm` package and a new tmux one); hooks record both `ITERM_SESSION_ID` and `TMUX_PANE`, the dashboard picks one host from its own environment. OpenCode reports through a plugin file that `oversight install` writes, which shells out to `oversight hook <Event> --agent opencode --pid N` with Claude-shaped JSON. The registry gains `tmux_pane` and `agent` fields; everything else stays agent-agnostic.

**Tech Stack:** Go 1.27, Bubble Tea / Lipgloss, fsnotify, tmux 3.x CLI, Bun-based OpenCode plugin (TypeScript, no dependencies).

**Spec:** `docs/superpowers/specs/2026-10-09-tmux-and-opencode-design.md`

## Global Constraints

- One binary per OS. No darwin-only Go files; `osascript` and `security` calls sit behind `runtime.GOOS == "darwin"` checks.
- Hook handler rules stand: never print to stdout, never exit non-zero, errors go to `~/.oversight/log`.
- Every tmux call has a 4 second timeout, like the iTerm scripts.
- Stored JSON field names: `tmux_pane`, `agent`. Agent values: `"claude"`, `"opencode"`. Empty agent means claude.
- tmux label format: `session:window.pane`, e.g. `dev:2.1`.
- Commit messages: sentence case, imperative, no prefix, no attribution lines (matches `git log`).
- Run `gofmt -l .` before every commit; it must print nothing.
- No recaps for OpenCode sessions. No `/rename` or `/color` for OpenCode.

## Review Focus

Inputs the spec implies but no step below would exercise without the tests added for them:

1. A session file written before this change has no `agent` field; it must render and filter as `claude`. Test added in Task 1 (`TestAgentNameDefaultsToClaude`).
2. A tmux session named with a colon or a space (`work: api`) must still parse and label correctly. Test added in Task 3 (`TestTmuxLocationsParsesOddNames`).
3. The pane disappears between the last `list-panes` and Enter, so `select-window` fails. The error must reach the flash line, not panic or hang. Test added in Task 3 (`TestTmuxFocusPropagatesRunnerError`).
4. The OpenCode plugin passes a malformed `--pid`. The hook must fall back to the ancestor walk, not drop the event. Test added in Task 2 (`TestRunParsesAgentAndPIDFlags`).
5. The dashboard is started in a plain terminal over ssh, inside neither tmux nor iTerm. Rows render without a label and Enter flashes `no terminal host`. Test added in Task 4 (`TestNoHost`).

---

## File Structure

| Path | Responsibility |
|------|----------------|
| `internal/registry/registry.go` | Add `TmuxPane`, `Agent`, `AgentName()`, `DeleteOthersWithPane`; remove `DeleteOthersWithITerm` |
| `internal/hooks/hooks.go` | Parse `--agent` / `--pid`, record `TMUX_PANE`, skip recap for opencode |
| `internal/host/host.go` | `Host` interface, `Location`, `ErrMissing`, `Detect()` |
| `internal/host/iterm.go` | Adapter over the existing `internal/iterm` package |
| `internal/host/tmux.go` | tmux backend with an injectable command runner |
| `internal/host/tmux_test.go` | Parsing, focus sequence, missing pane, runner errors |
| `internal/dash/model.go` | Use `host.Host`; own-pane hiding by tmux id; agent tag; header; footer; `--once` |
| `internal/dash/model_test.go` | Updated seed, new host/agent tests |
| `internal/usage/usage.go` | Token from Keychain (darwin) or `~/.claude/.credentials.json` |
| `internal/usage/usage_test.go` | File source tests |
| `internal/recap/recap.go` | Keychain API key only on darwin; skip opencode sessions |
| `internal/install/opencode.go` | Write the OpenCode plugin file |
| `internal/install/oversight.plugin.ts` | Embedded plugin template |
| `internal/install/opencode_test.go` | Plugin write / rewrite / skip tests |
| `main.go` | Wire flags: hook args, `dash` host detection, `install --opencode*` |
| `README.md` | Linux + tmux + OpenCode sections |

---

### Task 1: Registry fields and pane-based replacement

**Files:**
- Modify: `internal/registry/registry.go`
- Test: `internal/registry/registry_test.go`

**Interfaces:**
- Produces:
  - `Session.TmuxPane string` (json `tmux_pane,omitempty`)
  - `Session.Agent string` (json `agent,omitempty`)
  - `const AgentClaude = "claude"`, `const AgentOpenCode = "opencode"`
  - `func (s *Session) AgentName() string` — `Agent`, or `AgentClaude` when empty
  - `func DeleteOthersWithPane(itermID, tmuxPane, keepID string)` — replaces `DeleteOthersWithITerm`

- [ ] **Step 1: Write the failing tests**

Replace `TestDeleteOthersWithITerm` in `internal/registry/registry_test.go` with:

```go
func TestDeleteOthersWithPane(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	_ = SaveSession(&Session{SessionID: "old-iterm", ITermSessionID: "w0t1p0:X"})
	_ = SaveSession(&Session{SessionID: "old-tmux", TmuxPane: "%12"})
	_ = SaveSession(&Session{SessionID: "other", ITermSessionID: "w0t2p0:Y", TmuxPane: "%3"})
	_ = SaveSession(&Session{SessionID: "new", ITermSessionID: "w0t1p0:X", TmuxPane: "%12"})
	DeleteOthersWithPane("w0t1p0:X", "%12", "new")
	list, _ := ListSessions()
	ids := map[string]bool{}
	for _, s := range list {
		ids[s.SessionID] = true
	}
	if ids["old-iterm"] || ids["old-tmux"] || !ids["new"] || !ids["other"] {
		t.Fatalf("unexpected survivors: %v", ids)
	}
}

func TestDeleteOthersWithPaneIgnoresEmptyIDs(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	_ = SaveSession(&Session{SessionID: "a"}) // no pane ids at all
	_ = SaveSession(&Session{SessionID: "b"})
	DeleteOthersWithPane("", "", "b")
	list, _ := ListSessions()
	if len(list) != 2 {
		t.Fatalf("empty ids must not match anything, got %d sessions", len(list))
	}
}

func TestAgentNameDefaultsToClaude(t *testing.T) {
	if (&Session{}).AgentName() != AgentClaude {
		t.Fatal("empty agent should read as claude")
	}
	if (&Session{Agent: AgentOpenCode}).AgentName() != "opencode" {
		t.Fatal("explicit agent lost")
	}
}

func TestTmuxPaneAndAgentRoundTrip(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	_ = SaveSession(&Session{SessionID: "x", TmuxPane: "%7", Agent: AgentOpenCode})
	got, err := LoadSession("x")
	if err != nil || got.TmuxPane != "%7" || got.Agent != AgentOpenCode {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/registry/ -run 'DeleteOthersWithPane|AgentName|TmuxPaneAndAgent' -v`
Expected: compile error, `undefined: DeleteOthersWithPane`, `unknown field TmuxPane`.

- [ ] **Step 3: Implement**

In `internal/registry/registry.go`, after the derived-status constants add:

```go
// Agents whose sessions the registry tracks.
const (
	AgentClaude   = "claude"
	AgentOpenCode = "opencode"
)
```

In the `Session` struct, after `ITermSessionID` add:

```go
	TmuxPane       string    `json:"tmux_pane,omitempty"` // $TMUX_PANE, e.g. "%12"
	Agent          string    `json:"agent,omitempty"`     // claude (default) or opencode
```

After `SetStatus` add:

```go
// AgentName returns the agent, defaulting to claude for files written before
// the field existed.
func (s *Session) AgentName() string {
	if s.Agent == "" {
		return AgentClaude
	}
	return s.Agent
}
```

Replace `DeleteOthersWithITerm` entirely with:

```go
// DeleteOthersWithPane removes sessions sharing a terminal pane with the new
// one, except keepID. Either id matches; empty ids match nothing. This covers
// /clear and --resume in the same pane, and swapping one agent for another.
func DeleteOthersWithPane(itermID, tmuxPane, keepID string) {
	if itermID == "" && tmuxPane == "" {
		return
	}
	sessions, err := ListSessions()
	if err != nil {
		return
	}
	for _, s := range sessions {
		if s.SessionID == keepID {
			continue
		}
		sameITerm := itermID != "" && s.ITermSessionID == itermID
		sameTmux := tmuxPane != "" && s.TmuxPane == tmuxPane
		if sameITerm || sameTmux {
			Delete(s.SessionID)
		}
	}
}
```

Update the one caller in `internal/hooks/hooks.go` (`sessionStart`) so the package compiles:

```go
	registry.DeleteOthersWithPane(s.ITermSessionID, s.TmuxPane, s.SessionID)
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/registry/ ./internal/hooks/`
Expected: PASS for both packages.

- [ ] **Step 5: Commit**

```bash
gofmt -l .
git add internal/registry internal/hooks/hooks.go
git commit -m "Track tmux pane and agent per session"
```

---

### Task 2: Hook flags, TMUX_PANE, and no recap for OpenCode

**Files:**
- Modify: `internal/hooks/hooks.go`
- Modify: `main.go:30-35` (hook case)
- Modify: `internal/recap/recap.go:52-62` (Generate)
- Test: `internal/hooks/hooks_test.go`, `internal/recap/recap_test.go`

**Interfaces:**
- Consumes: `registry.Session.TmuxPane`, `registry.Session.Agent`, `registry.AgentOpenCode`, `registry.DeleteOthersWithPane`.
- Produces:
  - `hooks.Run(event string, args []string, stdin io.Reader) int` — `args` are flags after the event name: `--agent NAME`, `--pid N`.
  - `hooks.Env` gains `TmuxPane string`, `Agent string`, `PID int` (0 means use `ClaudePID`).
  - `hooks.ParseArgs(args []string) (agent string, pid int)` — exported for the test; bad or missing pid returns 0.
- CLI contract used by the plugin in Task 6: `oversight hook <Event> --agent opencode --pid <N>`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/hooks/hooks_test.go`:

```go
func TestSessionStartRecordsTmuxPaneAndAgent(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	var spawned []string
	env := testEnv(&spawned)
	env.TmuxPane = "%12"
	env.Agent = registry.AgentOpenCode
	env.PID = 777
	must(t, Handle("SessionStart", Input{SessionID: "oc1", CWD: "/x/api"}, env))
	s, err := registry.LoadSession("oc1")
	if err != nil {
		t.Fatal(err)
	}
	if s.TmuxPane != "%12" || s.ITermSessionID != "w0t3p0:UUID-A" || s.Agent != registry.AgentOpenCode || s.PID != 777 {
		t.Fatalf("bad session: %+v", s)
	}
}

func TestSessionStartDefaultsToClaudeAndAncestorPID(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	var spawned []string
	env := testEnv(&spawned) // no Agent, no PID, ClaudePID returns 4242
	must(t, Handle("SessionStart", Input{SessionID: "c1", CWD: "/x"}, env))
	s, _ := registry.LoadSession("c1")
	if s.Agent != registry.AgentClaude || s.PID != 4242 {
		t.Fatalf("defaults wrong: %+v", s)
	}
}

func TestSessionStartReplacesSameTmuxPane(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	var spawned []string
	env := testEnv(&spawned)
	env.ITermSessionID = "" // pure tmux on linux
	env.TmuxPane = "%4"
	must(t, Handle("SessionStart", Input{SessionID: "old", CWD: "/x"}, env))
	env.Agent = registry.AgentOpenCode
	must(t, Handle("SessionStart", Input{SessionID: "new", CWD: "/x"}, env))
	list, _ := registry.ListSessions()
	if len(list) != 1 || list[0].SessionID != "new" {
		t.Fatalf("want only new, got %+v", list)
	}
}

func TestOpenCodeStopDoesNotSpawnRecap(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	var spawned []string
	env := testEnv(&spawned)
	env.Agent = registry.AgentOpenCode
	must(t, Handle("SessionStart", Input{SessionID: "oc1", CWD: "/x"}, env))
	must(t, Handle("Stop", Input{SessionID: "oc1"}, env))
	s, _ := registry.LoadSession("oc1")
	if s.Status != registry.StatusWaiting {
		t.Fatalf("stop should still mark waiting: %s", s.Status)
	}
	if len(spawned) != 0 {
		t.Fatalf("opencode Stop spawned a recap: %v", spawned)
	}
}

func TestRunParsesAgentAndPIDFlags(t *testing.T) {
	cases := []struct {
		args  []string
		agent string
		pid   int
	}{
		{nil, "", 0},
		{[]string{"--agent", "opencode", "--pid", "123"}, "opencode", 123},
		{[]string{"--agent=opencode", "--pid=9"}, "opencode", 9},
		{[]string{"--pid", "not-a-number"}, "", 0},
		{[]string{"--bogus"}, "", 0},
	}
	for _, c := range cases {
		agent, pid := ParseArgs(c.args)
		if agent != c.agent || pid != c.pid {
			t.Errorf("ParseArgs(%v) = %q,%d want %q,%d", c.args, agent, pid, c.agent, c.pid)
		}
	}
}
```

Add to `internal/recap/recap_test.go` (look at the existing tests in that file for the `Summarizer` stub style; this one needs no summarizer call):

```go
func TestGenerateSkipsOpenCodeSessions(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	_ = registry.SaveSession(&registry.Session{SessionID: "oc", Agent: registry.AgentOpenCode})
	called := false
	err := Generate("oc", true, func(context.Context, string, string) (string, error) {
		called = true
		return "nope", nil
	})
	if !errors.Is(err, errSkipped) || called {
		t.Fatalf("want errSkipped without calling the summarizer, got %v called=%v", err, called)
	}
}
```

(Add `context` and `errors` to that test file's imports if missing.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/hooks/ ./internal/recap/`
Expected: compile errors, `env.TmuxPane undefined`, `undefined: ParseArgs`.

- [ ] **Step 3: Implement the hooks changes**

In `internal/hooks/hooks.go`:

Add `"flag"` and `"strconv"` to the imports (strconv only if you choose it; the FlagSet approach below does not need it).

Extend `Env`:

```go
// Env is what Handle needs from the environment, injectable for tests.
type Env struct {
	ITermSessionID string
	TmuxPane       string
	Agent          string // registry.AgentClaude when empty
	PID            int    // 0 means ask ClaudePID
	ClaudePID      func() int
	Branch         func(cwd string) string
	SpawnRecap     func(sessionID string)
	Now            func() time.Time
	Meta           func(transcriptPath string) transcript.Meta
}
```

Extend `DefaultEnv` to read `TMUX_PANE`:

```go
func DefaultEnv() Env {
	return Env{
		ITermSessionID: os.Getenv("ITERM_SESSION_ID"),
		TmuxPane:       os.Getenv("TMUX_PANE"),
		ClaudePID:      procs.ClaudePID,
		Branch:         GitBranch,
		SpawnRecap:     func(id string) { SpawnRecap(id) },
		Now:            time.Now,
		Meta:           transcript.ReadMeta,
	}
}
```

Replace `Run` and add `ParseArgs`:

```go
// Run is the CLI entry point. args are the flags after the event name
// (--agent NAME, --pid N), used by the OpenCode plugin. It always returns 0.
func Run(event string, args []string, stdin io.Reader) int {
	if os.Getenv(WorkerEnv) != "" {
		return 0
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		logx.Errorf("hook %s: read stdin: %v", event, err)
		return 0
	}
	var in Input
	if err := json.Unmarshal(data, &in); err != nil {
		logx.Errorf("hook %s: bad json: %v", event, err)
		return 0
	}
	env := DefaultEnv()
	env.Agent, env.PID = ParseArgs(args)
	if err := Handle(event, in, env); err != nil {
		logx.Errorf("hook %s (%s): %v", event, in.SessionID, err)
	}
	return 0
}

// ParseArgs reads --agent and --pid. Anything unparseable yields the zero
// value so a misbehaving caller still gets its event recorded.
func ParseArgs(args []string) (agent string, pid int) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&agent, "agent", "", "")
	fs.IntVar(&pid, "pid", 0, "")
	if err := fs.Parse(args); err != nil {
		// flag stops at the first error; keep whatever was parsed before it
		// only if it is sane, otherwise fall back to defaults.
		return "", 0
	}
	if pid < 0 {
		pid = 0
	}
	return agent, pid
}
```

Note on the `--pid not-a-number` case: `flag` returns an error and leaves `pid` at 0 and `agent` at `""`, which is what the test expects. For `--bogus`, same.

In `sessionStart`, replace the pane/pid block:

```go
	if env.ITermSessionID != "" {
		s.ITermSessionID = env.ITermSessionID
	}
	if env.TmuxPane != "" {
		s.TmuxPane = env.TmuxPane
	}
	s.Agent = env.Agent
	if s.Agent == "" {
		s.Agent = registry.AgentClaude
	}
	switch {
	case env.PID > 0:
		s.PID = env.PID
	case env.ClaudePID != nil:
		s.PID = env.ClaudePID()
	}
```

and keep the `DeleteOthersWithPane` call from Task 1 at the end.

In `Handle`, change the `Stop` case so the recap only spawns for Claude:

```go
	case "Stop":
		agent := ""
		err := update(in.SessionID, func(s *registry.Session) bool {
			s.SetStatus(registry.StatusWaiting, now)
			s.LastEvent = event
			if in.TranscriptPath != "" {
				s.TranscriptPath = in.TranscriptPath
			}
			refreshTitle(s, in, env)
			agent = s.AgentName()
			return true
		})
		if err == nil && env.SpawnRecap != nil && agent == registry.AgentClaude {
			env.SpawnRecap(in.SessionID)
		}
		return err
```

- [ ] **Step 4: Update main.go**

In `main.go`, the `hook` case becomes:

```go
	case "hook":
		if len(args) < 1 {
			os.Exit(0) // never fail a hook, even a misconfigured one
		}
		os.Exit(hooks.Run(args[0], args[1:], os.Stdin))
```

And in `usage()` change the hook line to:

```
  oversight hook <Event> [--agent NAME --pid N]   (called by Claude Code / the OpenCode plugin)
```

- [ ] **Step 5: Skip OpenCode sessions in the recap worker**

In `internal/recap/recap.go`, in `Generate`, right after the `LoadSession` error handling and before the `TranscriptPath` check:

```go
	if sess.AgentName() != registry.AgentClaude {
		return errSkipped // no transcript to summarize for other agents
	}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/hooks/ ./internal/recap/ && go build ./...`
Expected: PASS, build clean.

- [ ] **Step 7: Commit**

```bash
gofmt -l .
git add internal/hooks internal/recap main.go
git commit -m "Accept agent and pid flags on hook events"
```

---

### Task 3: The host package with iTerm and tmux backends

**Files:**
- Create: `internal/host/host.go`
- Create: `internal/host/iterm.go`
- Create: `internal/host/tmux.go`
- Test: `internal/host/tmux_test.go`, `internal/host/host_test.go`

**Interfaces:**
- Consumes: `iterm.Tabs`, `iterm.Focus`, `iterm.Notify`, `iterm.UUID`, `iterm.ErrMissing`, `registry.Session`.
- Produces:

```go
package host

type Location struct {
	Window, Tab int    // iTerm: window and tab index; tmux: window index and pane index
	Label       string // what the dashboard prints: "tab 3", "w2 tab 1", "dev:2.1"
}

var ErrMissing = errors.New("pane missing")

type Host interface {
	Name() string                                           // "iterm" or "tmux"
	Key(s *registry.Session) string                         // the stored id this host addresses, "" if none
	Locations(ctx context.Context) (map[string]Location, error)
	Focus(ctx context.Context, key string) error            // ErrMissing when the pane is gone
	Notify(title, body string)
}

func Detect(getenv func(string) string, goos string) Host  // tmux if TMUX set, iTerm on darwin, else nil
func ITerm() Host
func Tmux() Host
func NewTmux(run Runner) Host                                // for tests
type Runner func(ctx context.Context, args ...string) (string, error)
```

- [ ] **Step 1: Write the failing tests**

Create `internal/host/tmux_test.go`:

```go
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
```

Create `internal/host/host_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/host/`
Expected: `no Go files` or compile errors (`undefined: NewTmux`).

- [ ] **Step 3: Create host.go**

```go
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
```

- [ ] **Step 4: Create iterm.go**

```go
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
```

- [ ] **Step 5: Create tmux.go**

```go
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
	locs, err := h.Locations(ctx)
	if err != nil {
		return err
	}
	if _, ok := locs[key]; !ok {
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
```

Note for `TestTmuxFocusSequence`: the first recorded call is `list-panes -a -F <format>`; the test only checks the first and last argument of each call, so the full format string is fine.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/host/ -v`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
gofmt -l .
git add internal/host
git commit -m "Add host package with iTerm and tmux backends"
```

---

### Task 4: Dashboard on top of host, with agent tags

**Files:**
- Modify: `internal/dash/model.go`
- Modify: `internal/dash/model_test.go`
- Modify: `main.go:47-56` (dash case)

**Interfaces:**
- Consumes: `host.Host`, `host.Location`, `host.ErrMissing`, `host.Detect`, `host.ITerm`, `registry.Session.AgentName()`, `registry.Session.TmuxPane`.
- Produces: `dash.Options` gains `Host host.Host` and `OwnTmuxPane string`. `iterm` is no longer imported by `dash`.

- [ ] **Step 1: Update the seed and write the failing tests**

In `internal/dash/model_test.go`, replace the import of `internal/iterm` with `internal/host` and change the `seed` helper:

```go
func seed(t *testing.T) *model {
	t.Helper()
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	now := time.Now()
	me := 0 // our own pid is alive
	_ = registry.SaveSession(&registry.Session{SessionID: "a", Repo: "salonized", Branch: "pos-refunds", Title: "refunds fix", ITermSessionID: "w0t1p0:AAA", PID: pidSelf(me), Status: registry.StatusWorking, StatusSince: now})
	_ = registry.SaveSession(&registry.Session{SessionID: "b", Repo: "api", Branch: "fiskaly-retry", ITermSessionID: "w0t2p0:BBB", PID: pidSelf(me), Status: registry.StatusApproval, StatusSince: now.Add(-5 * time.Minute), LastPrompt: "seed the db"})
	_ = registry.SaveSession(&registry.Session{SessionID: "dead", Repo: "old", PID: 999999999, Status: registry.StatusWaiting, StatusSince: now})
	_ = registry.SaveSession(&registry.Session{SessionID: "self", Repo: "dash", ITermSessionID: "w0t0p0:ME", PID: pidSelf(me), Status: registry.StatusWaiting, StatusSince: now})
	_ = registry.SaveRecap(&registry.Recap{SessionID: "a", Recap: "Running the refund service tests."})
	m := newModel(Options{OwnITerm: "w0t0p0:ME", Host: host.ITerm()})
	m.width, m.height = 80, 24
	m.tabs = map[string]host.Location{"AAA": {Window: 1, Tab: 4, Label: "tab 4"}, "BBB": {Window: 1, Tab: 6, Label: "tab 6"}}
	m.reload()
	return m
}
```

Then add these tests at the end of the file:

```go
// fakeHost is a scriptable Host for dashboard tests.
type fakeHost struct {
	name    string
	locs    map[string]host.Location
	focused []string
	focusErr error
	notes   []string
}

func (f *fakeHost) Name() string { return f.name }
func (f *fakeHost) Key(s *registry.Session) string {
	if f.name == "tmux" {
		return s.TmuxPane
	}
	return s.ITermSessionID
}
func (f *fakeHost) Locations(context.Context) (map[string]host.Location, error) { return f.locs, nil }
func (f *fakeHost) Focus(_ context.Context, key string) error {
	f.focused = append(f.focused, key)
	return f.focusErr
}
func (f *fakeHost) Notify(title, body string) { f.notes = append(f.notes, title+": "+body) }

func seedTmux(t *testing.T, h host.Host) *model {
	t.Helper()
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	now := time.Now()
	_ = registry.SaveSession(&registry.Session{SessionID: "c", Repo: "api", Branch: "main", TmuxPane: "%12", Agent: registry.AgentClaude, PID: pidSelf(0), Status: registry.StatusWorking, StatusSince: now})
	_ = registry.SaveSession(&registry.Session{SessionID: "o", Repo: "web", Branch: "feat", TmuxPane: "%7", Agent: registry.AgentOpenCode, PID: pidSelf(0), Status: registry.StatusWaiting, StatusSince: now.Add(-time.Minute), LastPrompt: "fix the nav"})
	_ = registry.SaveSession(&registry.Session{SessionID: "legacy", Repo: "old", ITermSessionID: "w0t1p0:ZZZ", PID: pidSelf(0), Status: registry.StatusWaiting, StatusSince: now.Add(-2 * time.Minute)})
	_ = registry.SaveSession(&registry.Session{SessionID: "self", Repo: "dash", TmuxPane: "%0", PID: pidSelf(0), Status: registry.StatusWaiting, StatusSince: now})
	m := newModel(Options{OwnTmuxPane: "%0", Host: h})
	m.width, m.height = 90, 24
	if h != nil {
		m.tabs, _ = h.Locations(context.Background())
	}
	m.reload()
	return m
}

func TestTmuxHostLabelsAndAgentTags(t *testing.T) {
	h := &fakeHost{name: "tmux", locs: map[string]host.Location{"%12": {Label: "dev:2.1"}, "%7": {Label: "dev:3.0"}}}
	m := seedTmux(t, h)
	if len(m.rows) != 3 {
		t.Fatalf("own tmux pane should be hidden: %d rows", len(m.rows))
	}
	v := stripANSI(m.View())
	if !strings.Contains(v, " SESSIONS") {
		t.Fatalf("header should read SESSIONS:\n%s", v)
	}
	if !strings.Contains(v, "dev:2.1") || !strings.Contains(v, "dev:3.0") {
		t.Fatalf("tmux labels missing:\n%s", v)
	}
	if !strings.Contains(v, "opencode") || !strings.Contains(v, "claude") {
		t.Fatalf("agent tags missing:\n%s", v)
	}
	if !strings.Contains(v, "no tmux") {
		t.Fatalf("iTerm-only row should say no tmux under the tmux host:\n%s", v)
	}
}

func TestFilterMatchesAgent(t *testing.T) {
	m := seedTmux(t, &fakeHost{name: "tmux"})
	m.filter = "opencode"
	m.applyFilter()
	if len(m.rows) != 1 || m.rows[0].Session.SessionID != "o" {
		t.Fatalf("filter by agent: %+v", m.rows)
	}
}

func TestEnterFocusesThroughHost(t *testing.T) {
	h := &fakeHost{name: "tmux", locs: map[string]host.Location{"%7": {Label: "dev:3.0"}}}
	m := seedTmux(t, h)
	m.selected = "o"
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should return a focus command")
	}
	msg := cmd()
	m.Update(msg)
	if len(h.focused) != 1 || h.focused[0] != "%7" {
		t.Fatalf("focused %v", h.focused)
	}
	if m.flash != "" {
		t.Fatalf("unexpected flash %q", m.flash)
	}

	m.selected = "legacy"
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.flash != "not a tmux session" {
		t.Fatalf("iterm-only row under tmux host: cmd=%v flash=%q", cmd, m.flash)
	}
}

func TestEnterMissingPaneMarksStale(t *testing.T) {
	h := &fakeHost{name: "tmux", focusErr: host.ErrMissing}
	m := seedTmux(t, h)
	m.selected = "c"
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(cmd())
	if m.flash != "pane is gone, marked stale" {
		t.Fatalf("flash %q", m.flash)
	}
	if r := m.current(); r == nil || r.Status != registry.StatusStale {
		t.Fatalf("row should be stale: %+v", r)
	}
}

func TestNoHost(t *testing.T) {
	m := seedTmux(t, nil)
	v := stripANSI(m.View())
	if strings.Contains(v, "no tmux") || strings.Contains(v, "no iterm") {
		t.Fatalf("without a host rows should carry no placeholder:\n%s", v)
	}
	m.selected = "c"
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.flash != "no terminal host" {
		t.Fatalf("enter without host: cmd=%v flash=%q", cmd, m.flash)
	}
	if msg := fetchTabs(nil)(); msg.(tabsMsg).err != nil {
		t.Fatal("fetchTabs with nil host must not error")
	}
}

func TestApprovalNotifiesThroughHost(t *testing.T) {
	h := &fakeHost{name: "tmux"}
	m := seedTmux(t, h)
	m.opts.Notify = true
	// First reload seeded lastStatus; now flip "c" to approval.
	s, _ := registry.LoadSession("c")
	s.SetStatus(registry.StatusApproval, time.Now())
	_ = registry.SaveSession(s)
	m.reload()
	deadline := time.Now().Add(time.Second)
	for len(h.notes) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(h.notes) != 1 || !strings.Contains(h.notes[0], "api/main") {
		t.Fatalf("notification: %v", h.notes)
	}
}

func TestOncePlainShowsAgentAndLabel(t *testing.T) {
	h := &fakeHost{name: "tmux", locs: map[string]host.Location{"%7": {Label: "dev:3.0"}}}
	m := seedTmux(t, h)
	out := m.plain()
	if !strings.Contains(out, "opencode") || !strings.Contains(out, "dev:3.0") {
		t.Fatalf("plain output: %s", out)
	}
}
```

If `stripANSI` does not already exist in the test file, add it (check first; `TestSessionColorPaintsName` may already define a helper):

```go
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }
```

Add `"context"` and `"github.com/paaaco/oversight/internal/host"` to the test imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dash/`
Expected: compile errors: `undefined: host`, `unknown field Host in Options`, `fetchTabs` arity.

- [ ] **Step 3: Rewire model.go**

Imports: remove `internal/iterm`, add `internal/host`.

`Options`:

```go
// Options configure the dashboard.
type Options struct {
	Notify      bool
	OwnITerm    string    // this dashboard's $ITERM_SESSION_ID, hidden from the list
	OwnTmuxPane string    // this dashboard's $TMUX_PANE, hidden from the list
	Host        host.Host // terminal backend; nil means labels and Enter are unavailable
	IdleAfter   time.Duration
	Usage       bool // show the subscription rate-limit line
	Dividers    bool // draw a rule between rows, table style
}
```

`tabsMsg` and the model field:

```go
	tabsMsg struct {
		tabs map[string]host.Location
		err  error
	}
```

```go
	tabs       map[string]host.Location
```

`Init`: `fetchTabs(m.opts.Host)`. `Update`, `tickMsg` case: `tea.Batch(tick(), fetchTabs(m.opts.Host))`.

`fetchTabs`:

```go
func fetchTabs(h host.Host) tea.Cmd {
	return func() tea.Msg {
		if h == nil {
			return tabsMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		tabs, err := h.Locations(ctx)
		return tabsMsg{tabs: tabs, err: err}
	}
}
```

`reload`, the own-pane check and the notification:

```go
		if m.opts.OwnITerm != "" && s.ITermSessionID == m.opts.OwnITerm {
			continue
		}
		if m.opts.OwnTmuxPane != "" && s.TmuxPane == m.opts.OwnTmuxPane {
			continue
		}
```

```go
		if m.opts.Notify && m.opts.Host != nil && status == registry.StatusApproval && m.lastStatus[s.SessionID] != registry.StatusApproval && m.lastStatus[s.SessionID] != "" {
			go m.opts.Host.Notify(agentTitle(s)+" needs approval", repoBranch(s))
		}
```

and add near `repoBranch`:

```go
// agentTitle is the capitalised agent name for notifications.
func agentTitle(s *registry.Session) string {
	switch s.AgentName() {
	case registry.AgentOpenCode:
		return "OpenCode"
	default:
		return "Claude"
	}
}
```

`applyFilter` haystack:

```go
			hay := strings.ToLower(r.Session.AgentName() + " " + r.Session.Title + " " + repoBranch(r.Session) + " " + noteText(r) + " " + recapLine(r))
```

`Update`, `focusMsg` case: replace `iterm.ErrMissing` with `host.ErrMissing`.

`key`, the `enter` case:

```go
	case "enter":
		if r := m.current(); r != nil {
			h := m.opts.Host
			if h == nil {
				m.flash = "no terminal host"
				return m, nil
			}
			key := h.Key(r.Session)
			if key == "" {
				m.flash = "not a " + hostNoun(h) + " session"
				return m, nil
			}
			id := r.Session.SessionID
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				return focusMsg{id: id, err: h.Focus(ctx, key)}
			}
		}
```

with

```go
// hostNoun is how the host is named in messages: "iTerm" or "tmux".
func hostNoun(h host.Host) string {
	if h.Name() == "iterm" {
		return "iTerm"
	}
	return h.Name()
}
```

so the existing message `not an iTerm session` becomes `not a iTerm session`. Keep the article right:

```go
			if key == "" {
				if h.Name() == "iterm" {
					m.flash = "not an iTerm session"
				} else {
					m.flash = "not a " + h.Name() + " session"
				}
				return m, nil
			}
```

(Use this second form and drop `hostNoun`.)

`View` header: change `head := " CLAUDE SESSIONS"` to `head := " SESSIONS"`.

`View` footer: replace the `(iTerm unreachable)` line with:

```go
		if m.tabsErr != nil && m.opts.Host != nil {
			foot += styleDim.Render("   (" + m.opts.Host.Name() + " unreachable)")
		}
```

`renderRow`: insert the agent tag after the status and widen the layout math:

```go
func (m *model) renderRow(r registry.Row, selected bool, w int) string {
	st := styleStatus[r.Status]
	// " ! approval  opencode  " + name + "  5m  dev:2.1": the name gets whatever is left.
	nameW := w - 1 - 2 - 10 - 9 - 1 - 5 - 2 - 12
	if nameW < 20 {
		nameW = 20
	}
	if nameW > 60 {
		nameW = 60
	}
	line1 := fmt.Sprintf(" %s %-9s %s %s %5s  %s",
		st.Render(glyph[r.Status]),
		st.Render(fmt.Sprintf("%-9s", r.Status)),
		styleDim.Render(fmt.Sprintf("%-8s", r.Session.AgentName())),
		nameColumn(r.Session, nameW),
		since(r.Session.StatusSince),
		m.tabLabel(r.Session))
	line2 := "   " + styleDim.Render(clip(recapLine(r), max(w-4, 20)))
	if n := noteText(r); n != "" {
		line2 = "   " + clip("✎ "+n, max(w-4, 20))
	}
	if selected {
		line1 = styleSel.Render(padRight(line1, w))
	}
	return line1 + "\n" + line2 + "\n"
}
```

`plain`:

```go
		fmt.Fprintf(&b, "%-9s %-8s %-40s %5s  %s\n", r.Status, r.Session.AgentName(), name, since(r.Session.StatusSince), m.tabLabel(r.Session))
```

`tabLabel`:

```go
func (m *model) tabLabel(s *registry.Session) string {
	h := m.opts.Host
	if h == nil {
		return ""
	}
	key := h.Key(s)
	if key == "" {
		return styleDim.Render("no " + h.Name())
	}
	loc, ok := m.tabs[key]
	if !ok {
		return ""
	}
	return clip(loc.Label, 12)
}
```

Check the existing tests that assert on `tab 4` / `tab 6` still hold: the seed now supplies those labels through `host.Location.Label`.

- [ ] **Step 4: Wire main.go**

In the `dash` case:

```go
		opts := dash.Options{
			Notify:      *notify,
			OwnITerm:    os.Getenv("ITERM_SESSION_ID"),
			OwnTmuxPane: os.Getenv("TMUX_PANE"),
			Host:        host.Detect(os.Getenv, runtime.GOOS),
			IdleAfter:   *idle,
			Usage:       *showUsage,
			Dividers:    *dividers,
		}
```

Add imports `"runtime"` and `"github.com/paaaco/oversight/internal/host"`. Change the notify flag help text to `"send a desktop notification when a session needs approval"`.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/dash/ -v 2>&1 | grep -E '^(=== RUN|--- (PASS|FAIL)|FAIL|ok)' ; go build ./...`
Expected: every test PASS, including the pre-existing `TestViewAndSelection` and `TestDividersBetweenRowsOnly`. If `TestViewAndSelection` fails on a width assertion, the agent column shifted it: re-read the assertion and the new `nameW` math, do not loosen the test.

- [ ] **Step 6: Commit**

```bash
gofmt -l .
git add internal/dash main.go
git commit -m "Drive the dashboard through a terminal host"
```

---

### Task 5: Token and API key sources off macOS

**Files:**
- Modify: `internal/usage/usage.go:1-48`
- Modify: `internal/recap/recap.go:150-163`
- Test: `internal/usage/usage_test.go`

**Interfaces:**
- Produces:
  - `usage.CredentialsPath() string` — `~/.claude/.credentials.json`
  - `usage.ReadTokenFile(path string) (Token, error)` — `ErrNoToken` when missing or unparsable
  - `usage.ReadToken(ctx)` tries Keychain on darwin, then the file.

- [ ] **Step 1: Write the failing tests**

Add to `internal/usage/usage_test.go`:

```go
func TestReadTokenFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".credentials.json")
	if _, err := ReadTokenFile(p); err != ErrNoToken {
		t.Fatalf("missing file: want ErrNoToken, got %v", err)
	}
	_ = os.WriteFile(p, []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat-x","expiresAt":1800000000000}}`), 0o600)
	tok, err := ReadTokenFile(p)
	if err != nil || tok.Access != "sk-ant-oat-x" || tok.ExpiresAt.UnixMilli() != 1800000000000 {
		t.Fatalf("got %+v %v", tok, err)
	}
	_ = os.WriteFile(p, []byte(`not json`), 0o600)
	if _, err := ReadTokenFile(p); err != ErrNoToken {
		t.Fatalf("garbage file: want ErrNoToken, got %v", err)
	}
}

func TestCredentialsPathUnderHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	if got := CredentialsPath(); got != "/tmp/fakehome/.claude/.credentials.json" {
		t.Fatal(got)
	}
}
```

Add `"os"` and `"path/filepath"` to that file's imports if missing.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/usage/ -run 'ReadTokenFile|CredentialsPath'`
Expected: `undefined: ReadTokenFile`.

- [ ] **Step 3: Implement**

In `internal/usage/usage.go`, update the package comment's first lines:

```go
// Package usage reads the subscription rate-limit view that Claude Code's
// /usage shows, using the OAuth token Claude Code keeps in the macOS Keychain
// or, elsewhere, in ~/.claude/.credentials.json.
```

Add imports `"os"`, `"path/filepath"`, `"runtime"`. Replace `ReadToken`:

```go
// ReadToken loads the token: the Keychain on macOS, then the credentials file.
func ReadToken(ctx context.Context) (Token, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", keychainItem, "-w").Output()
		if err == nil {
			if tok, err := ParseCredentials(out); err == nil {
				return tok, nil
			}
		}
	}
	return ReadTokenFile(CredentialsPath())
}

// CredentialsPath is where Claude Code stores its login outside the Keychain.
func CredentialsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", ".credentials.json")
}

// ReadTokenFile parses a credentials file. Missing or unreadable is ErrNoToken.
func ReadTokenFile(path string) (Token, error) {
	if path == "" {
		return Token{}, ErrNoToken
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Token{}, ErrNoToken
	}
	return ParseCredentials(raw)
}
```

Update `ErrNoToken`'s message to `"no Claude Code login found"`.

In `internal/recap/recap.go`, `APIKey`: wrap the `security` call in `if runtime.GOOS == "darwin" { ... }` and return `""` otherwise; add `"runtime"` to imports. Update its doc comment to say "then, on macOS, the Keychain".

- [ ] **Step 4: Run the tests and a Linux cross-build**

Run: `go test ./internal/usage/ ./internal/recap/ && GOOS=linux go build -o /dev/null . && echo linux-ok`
Expected: PASS and `linux-ok`.

- [ ] **Step 5: Commit**

```bash
gofmt -l .
git add internal/usage internal/recap
git commit -m "Read the Claude Code login from the credentials file off macOS"
```

---

### Task 6: OpenCode plugin written by install

**Files:**
- Create: `internal/install/oversight.plugin.ts`
- Create: `internal/install/opencode.go`
- Create: `internal/install/opencode_test.go`
- Modify: `main.go` (install case and usage text)

**Interfaces:**
- Consumes: the hook CLI contract from Task 2: `oversight hook <Event> --agent opencode --pid <N>` with Claude-shaped JSON on stdin.
- Produces:
  - `install.OpenCodeConfigDir() (string, error)` — `~/.config/opencode`
  - `install.PluginPath(dir string) string` — `<dir>/plugins/oversight.ts`
  - `install.WritePlugin(dir, command string) (changed bool, err error)` — writes when missing or when the embedded command differs.
  - `install.Plugin(command string) string` — the rendered file contents.

- [ ] **Step 1: Write the failing tests**

Create `internal/install/opencode_test.go`:

```go
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
	for _, ev := range []string{"session.created", "message.updated", "permission.asked", "permission.replied", "session.idle", "session.deleted"} {
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/install/ -run Plugin`
Expected: `undefined: WritePlugin`.

- [ ] **Step 3: Create the plugin template**

Create `internal/install/oversight.plugin.ts`:

```ts
// Written by `oversight install`. Reports OpenCode session events to the
// oversight dashboard by calling the oversight binary the same way Claude
// Code's hooks do. Re-run `oversight install` after moving the binary; this
// file is rewritten whenever the embedded path changes.
const OVERSIGHT = __OVERSIGHT__;

export const OversightPlugin = async ({ directory }) => {
  // Sub-sessions (OpenCode's subagents) are ignored, like Claude's agent_id.
  const subSessions = new Set();

  const send = (event, payload) => {
    try {
      const proc = Bun.spawn(
        [OVERSIGHT, "hook", event, "--agent", "opencode", "--pid", String(process.pid)],
        { stdin: Buffer.from(JSON.stringify(payload)), stdout: "ignore", stderr: "ignore" },
      );
      proc.unref?.();
    } catch {
      // never let dashboard bookkeeping break the agent
    }
  };

  return {
    event: async ({ event }) => {
      const p = event?.properties ?? {};
      const info = p.info ?? {};
      switch (event?.type) {
        case "session.created": {
          if (info.parentID) {
            subSessions.add(info.id);
            return;
          }
          send("SessionStart", { session_id: info.id, cwd: directory });
          return;
        }
        case "message.updated": {
          if (info.role !== "user" || subSessions.has(info.sessionID)) return;
          // The prompt text is not reliably in this event; oversight keeps
          // the previous prompt when this is empty.
          send("UserPromptSubmit", { session_id: info.sessionID, prompt: "" });
          return;
        }
        case "permission.asked": {
          const sid = p.sessionID ?? info.sessionID;
          if (subSessions.has(sid)) return;
          send("Notification", { session_id: sid, notification_type: "permission_prompt" });
          return;
        }
        case "permission.replied": {
          const sid = p.sessionID ?? info.sessionID;
          if (subSessions.has(sid)) return;
          send("PostToolUse", { session_id: sid });
          return;
        }
        case "session.idle": {
          const sid = p.sessionID ?? info.id;
          if (subSessions.has(sid)) return;
          send("Stop", { session_id: sid });
          return;
        }
        case "session.deleted": {
          const sid = info.id ?? p.sessionID;
          subSessions.delete(sid);
          send("SessionEnd", { session_id: sid });
          return;
        }
      }
    },
  };
};
```

The `__OVERSIGHT__` placeholder is replaced with a JSON string literal, so the path is quoted and escaped by Go.

- [ ] **Step 4: Create opencode.go**

```go
package install

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

//go:embed oversight.plugin.ts
var pluginTemplate string

const pluginPlaceholder = "__OVERSIGHT__"

// OpenCodeConfigDir returns ~/.config/opencode, honouring XDG_CONFIG_HOME.
func OpenCodeConfigDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode"), nil
}

// PluginPath is where the plugin lives under an OpenCode config dir.
func PluginPath(dir string) string { return filepath.Join(dir, "plugins", "oversight.ts") }

// Plugin renders the plugin source for the given oversight command.
func Plugin(command string) string {
	quoted, _ := json.Marshal(command) // a JS string literal with escapes
	return strings.Replace(pluginTemplate, pluginPlaceholder, string(quoted), 1)
}

// WritePlugin writes the plugin under dir unless an identical one is there.
func WritePlugin(dir, command string) (changed bool, err error) {
	path := PluginPath(dir)
	want := Plugin(command)
	if have, err := os.ReadFile(path); err == nil && string(have) == want {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(want), 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}
```

- [ ] **Step 5: Wire the install command**

In `main.go`, the `install` case gains two flags and a second step. Replace the case body with:

```go
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		settings := fs.String("settings", "", "settings file (default ~/.claude/settings.json)")
		command := fs.String("command", "", "hook command to register (default: this binary's absolute path)")
		opencode := fs.Bool("opencode", true, "also write the OpenCode plugin when ~/.config/opencode exists")
		opencodeDir := fs.String("opencode-dir", "", "OpenCode config dir to write the plugin into (forces --opencode)")
		_ = fs.Parse(args)
		path := *settings
		if path == "" {
			p, err := install.SettingsPath()
			if err != nil {
				fmt.Fprintln(os.Stderr, "oversight:", err)
				os.Exit(1)
			}
			path = p
		}
		c := *command
		if c == "" {
			exe, err := os.Executable()
			if err != nil {
				fmt.Fprintln(os.Stderr, "oversight:", err)
				os.Exit(1)
			}
			c = exe
		}
		changed, err := install.Run(path, c)
		if err != nil {
			fmt.Fprintln(os.Stderr, "oversight:", err)
			os.Exit(1)
		}
		if changed {
			fmt.Printf("claude: hooks added to %s (backup written next to it)\n", path)
		} else {
			fmt.Printf("claude: hooks already present in %s\n", path)
		}
		if *opencode || *opencodeDir != "" {
			dir := *opencodeDir
			if dir == "" {
				d, err := install.OpenCodeConfigDir()
				if err == nil {
					if _, statErr := os.Stat(d); statErr == nil {
						dir = d
					}
				}
			}
			switch {
			case dir == "":
				fmt.Println("opencode: no ~/.config/opencode, plugin not written (use --opencode-dir to force)")
			default:
				changed, err := install.WritePlugin(dir, c)
				if err != nil {
					fmt.Fprintln(os.Stderr, "oversight:", err)
					os.Exit(1)
				}
				if changed {
					fmt.Printf("opencode: plugin written to %s\n", install.PluginPath(dir))
				} else {
					fmt.Printf("opencode: plugin up to date at %s\n", install.PluginPath(dir))
				}
			}
		}
```

Update the `usage()` install line:

```
  oversight install [--settings PATH] [--command CMD] [--opencode=false] [--opencode-dir DIR]
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/install/ && go build ./... && go run . install --settings /dev/null --opencode-dir "$(mktemp -d)" --command /tmp/fake-oversight`
Expected: tests PASS; the `go run` prints a `claude:` line (settings at `/dev/null` means the hook merge is attempted against an empty file; if it errors on writing `/dev/null`, use `--settings "$(mktemp -d)/settings.json"` instead) and `opencode: plugin written to .../plugins/oversight.ts`. Open that file and confirm it is valid-looking TypeScript with the quoted path.

- [ ] **Step 7: Commit**

```bash
gofmt -l .
git add internal/install main.go
git commit -m "Write an OpenCode plugin from install"
```

---

### Task 7: README and manual verification

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Rewrite the intro and add the Linux / tmux / OpenCode sections**

Change the first paragraph of `README.md` to:

```markdown
A dashboard that lists every running Claude Code and OpenCode session on the
machine, its status, and a one-line recap. Enter jumps to that session's
iTerm tab or tmux pane.
```

Update the mockup block: header `SESSIONS`, an agent column, and one tmux label:

```
 SESSIONS                                            3 active
 5h ▓▓░░░░░░░░ 17%  resets 17:29    7d ▓▓░░░░░░░░ 19%  resets thu
 ─────────────────────────────────────────────────────────────
 ! approval  claude    api/fiskaly-retry         5m   dev:2.1
   Asking permission to run the DB seed script.
 ─────────────────────────────────────────────────────────────
 ● waiting   opencode  web/feat                  2m   dev:3.0
   > fix the nav
 ─────────────────────────────────────────────────────────────
 ◐ working   claude    salonized/pos-refunds     now  dev:1.0
   Running the refund service tests.
 ─────────────────────────────────────────────────────────────
 ↑↓ select   enter jump   / filter   n note   r recap   x drop   q quit
```

After the existing `## Install` section add:

```markdown
## Linux and tmux

```sh
go build -o ~/.local/bin/oversight .
oversight install       # Claude hooks, plus the OpenCode plugin if ~/.config/opencode exists
tmux new-window -n oversight oversight
```

Inside tmux the dashboard labels rows `session:window.pane` and Enter runs
`switch-client`, `select-window` and `select-pane` on that pane, so it works
across tmux sessions. Started outside tmux (and outside iTerm) there is no
way to jump, so rows carry no label and Enter says so.

Notifications on `approval` use `notify-send` when it is installed and are
skipped otherwise. There is no Automation prompt on Linux.

On macOS, running the dashboard inside tmux (even tmux inside iTerm) uses the
tmux path; running it in a plain iTerm tab uses the iTerm path. Hooks record
both ids, so the same session file works for either.

## OpenCode

`oversight install` writes `~/.config/opencode/plugins/oversight.ts` when
that config directory exists (`--opencode-dir DIR` to force a location,
`--opencode=false` to skip). The plugin calls
`oversight hook <Event> --agent opencode --pid N` on `session.created`,
user `message.updated`, `permission.asked`, `permission.replied`,
`session.idle` and `session.deleted`. Sub-sessions are ignored.

OpenCode rows show the last prompt and your note. There is no recap, no
`/rename` and no `/color` for them, because those come from the Claude Code
transcript. `permission.asked` also fires for auto-approved permissions, so
an OpenCode row can flash `approval` for a moment before `permission.replied`
flips it back.

The plugin fills `prompt` on `UserPromptSubmit` only when the event carries
the text; on versions where it does not, the row keeps its previous prompt.
```

Under `## Keys`, change the Enter row to `Focus that session's iTerm tab or tmux pane`. Under `## Flags`, add:

```markdown
- `install --opencode=false` skips the OpenCode plugin; `--opencode-dir DIR` writes it under a non-default config dir.
```

Under `## Subscription usage line`, add after the Keychain sentence:

```markdown
On Linux the same token is read from `~/.claude/.credentials.json`, with no
prompt.
```

Under `## Notes`, replace the VS Code bullet with:

```markdown
- Claude started outside both iTerm and tmux (VS Code terminal) is listed,
  but Enter says `not an iTerm session` or `not a tmux session`.
- A session that starts in a pane another session already occupies (same
  iTerm id or same tmux pane) replaces the older entry, so swapping Claude
  for OpenCode in a pane does not leave a ghost row.
```

- [ ] **Step 2: Manual verification on macOS**

Run inside tmux inside iTerm:

```bash
go build -o ~/.local/bin/oversight . && oversight install
tmux new-session -d -s ov 'oversight' && tmux attach -t ov
```

In another tmux window start `claude`, send a prompt, confirm the row appears with `claude` tag and a `ov:N.M` label, press Enter in the dashboard and confirm the pane switches. Quit tmux, run `oversight` in a plain iTerm tab, start `claude` in another tab, confirm `tab N` labels and Enter still work. Record what you saw in the commit message body.

- [ ] **Step 3: Manual verification with OpenCode (if installed)**

Start `opencode` in a tmux pane, send a prompt, trigger a permission prompt. Confirm the row shows `opencode`, goes `working`, then `approval`, then `waiting`. Check `~/.oversight/log` for errors from the hook (bad JSON, unknown event). If `message.updated` turns out to carry the prompt text under a field like `info.parts` or `p.part.text`, update the plugin's `UserPromptSubmit` payload to pass it and note the field in the README's last OpenCode paragraph.

- [ ] **Step 4: Full test run and Linux cross-build**

Run: `gofmt -l . ; go vet ./... && go test ./... && GOOS=linux go build -o /dev/null . && echo ALL-OK`
Expected: no gofmt output, all PASS, `ALL-OK`.

- [ ] **Step 5: Commit**

```bash
git add README.md internal/install/oversight.plugin.ts
git commit -m "Document tmux, Linux and OpenCode support"
```

---

## Self-review notes

- Spec coverage: hosts (Task 3, 4), both ids recorded (Task 2), host selection rule (Task 3 `Detect`), tmux label and focus sequence (Task 3), `ErrMissing` from listing (Task 3), notifications (Task 3 `Notify`, Task 4 wiring), `DeleteOthersWithPane` (Task 1), `agent` field and tag, header `SESSIONS`, filter (Task 4), hook flags and plugin (Tasks 2, 6), opencode recap skip (Task 2), usage token file and API key guard (Task 5), `--once` output (Task 4), install behaviour (Task 6), README Linux section (Task 7).
- Type consistency: `host.Location{Window, Tab, Label}`, `host.Host` methods `Name/Key/Locations/Focus/Notify`, `hooks.Run(event, args, stdin)`, `hooks.ParseArgs`, `install.WritePlugin(dir, command)`, `install.PluginPath(dir)`, `usage.ReadTokenFile(path)` are used with the same shapes in every task.
- Deviation from spec wording: the install flag is `--opencode-dir` rather than `--opencode-plugins`, because the plugin path is derived from the config dir. Same behaviour.
