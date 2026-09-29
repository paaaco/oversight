package dash

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/paaaco/oversight/internal/iterm"
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
	m := newModel(Options{OwnITerm: "w0t0p0:ME"})
	m.width, m.height = 80, 24
	m.tabs = map[string]iterm.Location{"AAA": {Window: 1, Tab: 4}, "BBB": {Window: 1, Tab: 6}}
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
	for _, want := range []string{"CLAUDE SESSIONS", "2 active", "api/fiskaly-retry", "tab 6", "refunds fix", "· salonized/pos-refunds", "> seed the db", "Running the refund service tests.", "stale", "enter jump"} {
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
	if !strings.HasPrefix(out, "3 session(s)") || !strings.Contains(out, "approval  api/fiskaly-retry") || !strings.Contains(out, "refunds fix · salonized/pos-refunds") {
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
