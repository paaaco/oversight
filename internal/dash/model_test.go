package dash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/paaaco/oversight/internal/host"
	"github.com/paaaco/oversight/internal/registry"
	"github.com/paaaco/oversight/internal/usage"
)

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

func TestViewAndSelection(t *testing.T) {
	m := seed(t)
	if len(m.rows) != 3 {
		t.Fatalf("own row should be hidden: %d rows", len(m.rows))
	}
	if m.rows[0].Session.SessionID != "b" || m.selected != "b" {
		t.Fatalf("approval row should be first and selected: %s / %s", m.rows[0].Session.SessionID, m.selected)
	}
	v := m.View()
	for _, want := range []string{"SESSIONS", "2 active", "api/fiskaly-retry", "tab 6", "refunds fix", "· salonized/pos-refunds", "> seed the db", "Running the refund service tests.", "stale", "enter jump"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "dash") {
		t.Fatal("dashboard's own session rendered")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.selected != "a" {
		t.Fatalf("down: %s", m.selected)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	for _, r := range "refunds fix" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(m.rows) != 1 || m.rows[0].Session.SessionID != "a" {
		t.Fatalf("filter: %d rows", len(m.rows))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.filter != "" || len(m.rows) != 3 {
		t.Fatal("esc should clear the filter")
	}
	// Selection survives a re-sort: b jumps to working, a stays selected.
	b, _ := registry.LoadSession("b")
	b.SetStatus(registry.StatusWorking, time.Now())
	_ = registry.SaveSession(b)
	m.reload()
	if m.selected != "a" {
		t.Fatalf("selection moved to %s", m.selected)
	}
	// x drops the stale row.
	m.selected = "dead"
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if len(m.rows) != 2 {
		t.Fatalf("x did not drop: %d rows", len(m.rows))
	}
}

func TestOncePlain(t *testing.T) {
	m := seed(t)
	out := m.plain()
	if !strings.HasPrefix(out, "3 session(s)") || !strings.Contains(out, "approval  claude   api/fiskaly-retry") || !strings.Contains(out, "refunds fix · salonized/pos-refunds") {
		t.Fatalf("plain:\n%s", out)
	}
}

func TestUsageLine(t *testing.T) {
	m := seed(t)
	m.opts.Usage = true
	if !strings.Contains(m.View(), "usage …") {
		t.Fatal("pending usage not shown")
	}
	m.Update(usageMsg{err: usage.ErrNoToken})
	if !strings.Contains(m.View(), "usage unavailable") {
		t.Fatal("error not shown")
	}
	m.Update(usageMsg{snap: &usage.Snapshot{Limits: []usage.Limit{
		{Label: "5h", Percent: 62, ResetsAt: time.Now().Add(time.Hour)},
		{Label: "7d fable", Percent: 95},
	}}})
	v := m.View()
	for _, want := range []string{"5h ▓▓▓▓▓▓░░░░ 62%", "resets ", "7d fable ▓▓▓▓▓▓▓▓▓▓ 95%"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in:\n%s", want, v)
		}
	}
	m.width = 50
	if v := m.View(); !strings.Contains(v, "5h ▓▓▓▓░░ 62%  resets") {
		t.Fatalf("mid tier:\n%s", v)
	}
	m.width = 30
	if v := m.View(); !strings.Contains(v, "5h 62%  7d fable 95%") {
		t.Fatalf("narrow fallback:\n%s", v)
	}
}

func TestSessionColorPaintsName(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(prev)
	s := &registry.Session{Repo: "salonized", Branch: "master", Title: "splitwalletspr", Color: "green"}
	out := nameColumn(s, 40)
	if !strings.Contains(out, "\x1b[1;32m") && !strings.Contains(out, "\x1b[32;1m") {
		t.Fatalf("no green escape in %q", out)
	}
	s.Color = "no-such-color"
	if out := nameColumn(s, 40); strings.Contains(out, "\x1b[32") {
		t.Fatalf("unknown color should be plain: %q", out)
	}
}

func TestViewFillsTerminalAndPinsFooter(t *testing.T) {
	m := seed(t)
	m.width, m.height = 80, 24
	v := m.View()
	lines := strings.Split(v, "\n")
	if len(lines) != 24 {
		t.Fatalf("view has %d lines, want 24:\n%s", len(lines), v)
	}
	if !strings.Contains(lines[23], "enter jump") {
		t.Fatalf("footer not on the last line:\n%s", v)
	}
	if strings.Contains(v, "more") {
		t.Fatalf("no overflow hint expected when everything fits:\n%s", v)
	}
}

func TestViewScrollsToKeepSelectionVisible(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	now := time.Now()
	for i := 0; i < 12; i++ {
		id := string(rune('a' + i))
		_ = registry.SaveSession(&registry.Session{SessionID: id, Repo: "repo-" + id, PID: pidSelf(0), Status: registry.StatusWaiting, StatusSince: now.Add(-time.Duration(i) * time.Minute)})
	}
	m := newModel(Options{})
	m.width, m.height = 80, 12 // 4 fixed lines + 8 for rows = 4 rows visible
	m.reload()

	v := m.View()
	if lines := strings.Split(v, "\n"); len(lines) != 12 {
		t.Fatalf("view has %d lines, want 12:\n%s", len(lines), v)
	}
	if !strings.Contains(v, "repo-a") || strings.Contains(v, "repo-e") {
		t.Fatalf("first page should show rows a..d only:\n%s", v)
	}
	if !strings.Contains(v, "↓ 8 more") || hasUpHint(v) {
		t.Fatalf("want a bottom overflow hint only:\n%s", v)
	}

	for i := 0; i < 6; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	v = m.View()
	if m.selected != "g" {
		t.Fatalf("selected %s, want g", m.selected)
	}
	if !strings.Contains(v, "repo-g") || strings.Contains(v, "repo-c") || strings.Contains(v, "repo-h") {
		t.Fatalf("after moving to g the page should be d..g:\n%s", v)
	}
	if !hasUpHint(v) || !strings.Contains(v, "↑ 3 more") || !strings.Contains(v, "↓ 5 more") {
		t.Fatalf("want both overflow hints:\n%s", v)
	}

	for i := 0; i < 20; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	v = m.View()
	if m.selected != "a" || !strings.Contains(v, "repo-a") || hasUpHint(v) {
		t.Fatalf("moving back up should scroll to the top:\n%s", v)
	}

	// Shrinking the terminal keeps the selection on screen.
	m.selected = "l"
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 8}) // 2 rows visible
	v = m.View()
	if !strings.Contains(v, "repo-l") || !strings.Contains(v, "repo-k") || strings.Contains(v, "repo-j") {
		t.Fatalf("resize should keep the selection visible:\n%s", v)
	}
}

// hasUpHint reports an "↑ N more" overflow hint (the footer's "↑↓ select" does not count).
func hasUpHint(v string) bool {
	return regexp.MustCompile(`↑ \d+ more`).MatchString(v)
}

func TestNoteEditing(t *testing.T) {
	m := seed(t)
	m.selected = "a" // has an AI recap
	typeKeys := func(s string) {
		for _, r := range s {
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if !m.noting {
		t.Fatal("n should open the note editor")
	}
	if v := m.View(); !strings.Contains(v, "enter save") {
		t.Fatalf("editor footer missing:\n%s", v)
	}
	typeKeys("fix POS")
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	typeKeys("refunds")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v := m.View()
	if m.noting || !strings.Contains(v, "✎ fix POS refunds") || strings.Contains(v, "Running the refund") {
		t.Fatalf("note should replace the recap on line 2:\n%s", v)
	}
	if n, err := registry.LoadNote("a"); err != nil || n.Note != "fix POS refunds" {
		t.Fatalf("note not persisted: %v %+v", err, n)
	}
	if !strings.Contains(m.plain(), "✎ fix POS refunds") {
		t.Fatal("plain output should show the note")
	}

	// Reopening prefills; esc leaves the saved note alone.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.note != "fix POS refunds" {
		t.Fatalf("editor should prefill: %q", m.note)
	}
	typeKeys(" nope")
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if v := m.View(); m.noting || strings.Contains(v, "nope") || !strings.Contains(v, "✎ fix POS refunds") {
		t.Fatalf("esc should cancel:\n%s", v)
	}

	// Filter matches the note.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	typeKeys("pos refunds")
	if len(m.rows) != 1 || m.rows[0].Session.SessionID != "a" {
		t.Fatalf("filter on note: %d rows", len(m.rows))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Saving an empty note clears it and the recap comes back.
	m.selected = "a"
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	for range "fix POS refunds" {
		m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := m.View(); strings.Contains(v, "✎") || !strings.Contains(v, "Running the refund service tests.") {
		t.Fatalf("empty note should fall back to the recap:\n%s", v)
	}
	if _, err := registry.LoadNote("a"); err == nil {
		t.Fatal("empty note should delete the file")
	}
	if !strings.Contains(m.View(), "n note") {
		t.Fatal("footer should advertise the n key")
	}
}

func TestDividersBetweenRowsOnly(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	now := time.Now()
	for i := 0; i < 12; i++ {
		id := string(rune('a' + i))
		_ = registry.SaveSession(&registry.Session{SessionID: id, Repo: "repo-" + id, PID: pidSelf(0), Status: registry.StatusWaiting, StatusSince: now.Add(-time.Duration(i) * time.Minute)})
	}
	m := newModel(Options{Dividers: true})
	m.width, m.height = 80, 13 // 4 fixed lines + 9 for rows = 3 rows of 3 lines
	m.reload()

	v := m.View()
	lines := strings.Split(v, "\n")
	if len(lines) != 13 {
		t.Fatalf("view has %d lines, want 13:\n%s", len(lines), v)
	}
	if !strings.Contains(v, "repo-c") || strings.Contains(v, "repo-d") {
		t.Fatalf("three rows should fit with dividers:\n%s", v)
	}
	// Layout: title, rule, a, a, ─, b, b, ─, c, c, pad, rule, footer.
	isRule := func(l string) bool { return strings.Contains(l, "──") }
	if !isRule(lines[4]) || !isRule(lines[7]) {
		t.Fatalf("want dividers after rows a and b:\n%s", v)
	}
	if isRule(lines[10]) {
		t.Fatalf("no divider after the last visible row:\n%s", v)
	}
	if !strings.Contains(v, "↓ 9 more") {
		t.Fatalf("want overflow hint for the 9 hidden rows:\n%s", v)
	}

	m.selected = "l"
	m.scrollToSelection()
	v = m.View()
	if !strings.Contains(v, "repo-l") || len(strings.Split(v, "\n")) != 13 {
		t.Fatalf("scrolling with dividers should keep the height stable:\n%s", v)
	}
}

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// fakeHost is a scriptable Host for dashboard tests.
type fakeHost struct {
	name     string
	locs     map[string]host.Location
	mu       sync.Mutex
	focused  []string
	focusErr error
	locsErr  error
	notes    []string
}

func (f *fakeHost) noteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.notes)
}

func (f *fakeHost) notesCopy() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.notes...)
}

func (f *fakeHost) Name() string { return f.name }
func (f *fakeHost) Key(s *registry.Session) string {
	if f.name == "tmux" {
		return s.TmuxPane
	}
	return s.ITermSessionID
}
func (f *fakeHost) Locations(context.Context) (map[string]host.Location, error) {
	return f.locs, f.locsErr
}
func (f *fakeHost) Focus(_ context.Context, key string) error {
	f.mu.Lock()
	f.focused = append(f.focused, key)
	f.mu.Unlock()
	return f.focusErr
}
func (f *fakeHost) Notify(title, body string) {
	f.mu.Lock()
	f.notes = append(f.notes, title+": "+body)
	f.mu.Unlock()
}

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

// A tmux server started from iTerm hands its one $ITERM_SESSION_ID to every
// pane, the dashboard's included. That shared id must not hide the rows.
func TestSharedITermIDInsideTmuxShowsRows(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	now := time.Now()
	shared := "w0t1p0:SHARED"
	_ = registry.SaveSession(&registry.Session{SessionID: "c", Repo: "api", ITermSessionID: shared, TmuxPane: "%12", PID: pidSelf(0), Status: registry.StatusWorking, StatusSince: now})
	_ = registry.SaveSession(&registry.Session{SessionID: "o", Repo: "web", ITermSessionID: shared, TmuxPane: "%7", PID: pidSelf(0), Status: registry.StatusWaiting, StatusSince: now})
	_ = registry.SaveSession(&registry.Session{SessionID: "self", Repo: "dash", ITermSessionID: shared, TmuxPane: "%0", PID: pidSelf(0), Status: registry.StatusWaiting, StatusSince: now})
	m := newModel(Options{OwnITerm: shared, OwnTmuxPane: "%0", Host: &fakeHost{name: "tmux"}})
	m.reload()
	if len(m.rows) != 2 {
		t.Fatalf("want the two other panes shown, got %d rows", len(m.rows))
	}
	for _, r := range m.rows {
		if r.Session.SessionID == "self" {
			t.Fatal("own tmux pane should still be hidden")
		}
	}
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

func TestTmuxLabelKeepsWindowAndPane(t *testing.T) {
	h := &fakeHost{name: "tmux", locs: map[string]host.Location{
		"%12": {Window: 2, Tab: 1, Label: "salonized-backend:2.1"},
		"%7":  {Window: 3, Tab: 0, Label: "web:3.0"},
	}}
	m := seedTmux(t, h)
	if got := m.tabLabel(&registry.Session{TmuxPane: "%12"}); got != "saloniz…:2.1" {
		t.Fatalf("long session name should be clipped, not the pane suffix: %q", got)
	}
	if got := m.tabLabel(&registry.Session{TmuxPane: "%7"}); got != "web:3.0" {
		t.Fatalf("short label changed: %q", got)
	}
}

func TestITermLabelPlainClip(t *testing.T) {
	m := seed(t)
	m.tabs["AAA"] = host.Location{Window: 2, Tab: 10, Label: "w2 tab 10"}
	if got := m.tabLabel(&registry.Session{ITermSessionID: "w0t1p0:AAA"}); got != "w2 tab 10" {
		t.Fatalf("iTerm label: %q", got)
	}
}

func TestEnterWrappedMissingPaneMarksStale(t *testing.T) {
	h := &fakeHost{name: "tmux", focusErr: fmt.Errorf("focus %%12: %w", host.ErrMissing)}
	m := seedTmux(t, h)
	m.selected = "c"
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(cmd())
	if m.flash != "pane is gone, marked stale" {
		t.Fatalf("wrapped ErrMissing not recognised: flash %q", m.flash)
	}
}

func TestOnceReportsUnreachableHost(t *testing.T) {
	h := &fakeHost{name: "tmux", locsErr: errors.New("no server running")}
	seedTmux(t, h)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	onceModel(Options{OwnTmuxPane: "%0", Host: h})
	os.Stderr = orig
	w.Close()
	out, _ := io.ReadAll(r)
	if got := string(out); got != "oversight: tmux unreachable: no server running\n" {
		t.Fatalf("stderr: %q", got)
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
	for h.noteCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if notes := h.notesCopy(); len(notes) != 1 || !strings.Contains(notes[0], "api/main") {
		t.Fatalf("notification: %v", notes)
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

func TestOnceFetchesHostLabels(t *testing.T) {
	h := &fakeHost{name: "tmux", locs: map[string]host.Location{"%7": {Label: "dev:3.0"}}}
	seedTmux(t, h) // seeds the registry; the model it returns is not used
	m := onceModel(Options{OwnTmuxPane: "%0", Host: h})
	if out := m.plain(); !strings.Contains(out, "dev:3.0") {
		t.Fatalf("once should fetch host locations:\n%s", out)
	}
}
