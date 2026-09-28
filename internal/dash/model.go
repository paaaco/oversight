// Package dash is the Bubble Tea TUI that lives in tab 1.
package dash

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fsnotify/fsnotify"

	"github.com/paaaco/oversight/internal/hooks"
	"github.com/paaaco/oversight/internal/iterm"
	"github.com/paaaco/oversight/internal/logx"
	"github.com/paaaco/oversight/internal/procs"
	"github.com/paaaco/oversight/internal/registry"
	"github.com/paaaco/oversight/internal/usage"
)

const (
	tickEvery     = 5 * time.Second
	debounce      = 100 * time.Millisecond
	staleDeleteAf = 10 * time.Minute
	usageEvery    = 60 * time.Second
)

// Options configure the dashboard.
type Options struct {
	Notify    bool
	OwnITerm  string // this dashboard's $ITERM_SESSION_ID, hidden from the list
	IdleAfter time.Duration
	Usage     bool // show the subscription rate-limit line
}

type (
	registryMsg struct{}
	tickMsg     time.Time
	tabsMsg     struct {
		tabs map[string]iterm.Location
		err  error
	}
	focusMsg struct {
		id  string
		err error
	}
	flashMsg string
	usageMsg struct {
		snap *usage.Snapshot
		err  error
	}
)

type model struct {
	opts       Options
	rows       []registry.Row
	all        []registry.Row // before filtering
	selected   string
	filter     string
	filtering  bool
	tabs       map[string]iterm.Location
	tabsErr    error
	staleSince map[string]time.Time
	forceStale map[string]bool
	lastStatus map[string]string
	flash      string
	usage      *usage.Snapshot
	usageErr   error
	token      usage.Token
	width      int
	height     int
	offset     int // index of the first visible row
	changes    chan struct{}
}

// Run starts the interactive dashboard.
func Run(opts Options) error {
	if opts.IdleAfter > 0 {
		registry.IdleAfter = opts.IdleAfter
	}
	m := newModel(opts)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// Once prints the current rows as plain text and exits.
func Once(opts Options) error {
	if opts.IdleAfter > 0 {
		registry.IdleAfter = opts.IdleAfter
	}
	m := newModel(opts)
	m.reload()
	m.width = 100
	if opts.Usage {
		if msg, ok := m.fetchUsage()().(usageMsg); ok {
			m.usage, m.usageErr = msg.snap, msg.err
		}
		fmt.Println(strings.TrimSpace(m.usageLine(m.width)))
	}
	fmt.Print(m.plain())
	return nil
}

func newModel(opts Options) *model {
	return &model{
		opts:       opts,
		staleSince: map[string]time.Time{},
		forceStale: map[string]bool{},
		lastStatus: map[string]string{},
		changes:    make(chan struct{}, 1),
	}
}

func (m *model) Init() tea.Cmd {
	m.reload()
	go m.watch()
	cmds := []tea.Cmd{m.waitChange(), tick(), fetchTabs()}
	if m.opts.Usage {
		cmds = append(cmds, m.fetchUsage())
	}
	return tea.Batch(cmds...)
}

// fetchUsage reads the token (again when it expired or was rejected) and
// polls the usage endpoint. The token never leaves this process.
func (m *model) fetchUsage() tea.Cmd {
	tok := m.token
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if tok.Access == "" || time.Until(tok.ExpiresAt) < time.Minute {
			t, err := usage.ReadToken(ctx)
			if err != nil {
				return usageMsg{err: err}
			}
			tok = t
		}
		snap, err := usage.Fetch(ctx, tok)
		if err == usage.ErrUnauthorized {
			// Claude Code may have refreshed the token since we read it.
			if t, rerr := usage.ReadToken(ctx); rerr == nil && t.Access != tok.Access {
				tok = t
				snap, err = usage.Fetch(ctx, tok)
			}
		}
		if err != nil {
			return usageMsg{err: err}
		}
		return usageMsg{snap: snap}
	}
}

func usageTick() tea.Cmd {
	return tea.Tick(usageEvery, func(time.Time) tea.Msg { return usageMsg{} })
}

func tick() tea.Cmd {
	return tea.Tick(tickEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func fetchTabs() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		tabs, err := iterm.Tabs(ctx)
		return tabsMsg{tabs: tabs, err: err}
	}
}

func (m *model) waitChange() tea.Cmd {
	return func() tea.Msg {
		<-m.changes
		return registryMsg{}
	}
}

// watch runs fsnotify on the sessions directory, debounced by 100 ms.
func (m *model) watch() {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		logx.Errorf("dash: fsnotify: %v", err)
		return
	}
	defer w.Close()
	if err := w.Add(registry.Dir()); err != nil {
		logx.Errorf("dash: watch %s: %v", registry.Dir(), err)
		return
	}
	var timer *time.Timer
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if strings.Contains(ev.Name, "/.tmp-") {
				continue // temp files before rename
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(debounce, func() {
				select {
				case m.changes <- struct{}{}:
				default:
				}
			})
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			logx.Errorf("dash: watcher: %v", err)
		}
	}
}

// reload reads the registry, derives statuses, sorts and filters.
func (m *model) reload() {
	sessions, err := registry.ListSessions()
	if err != nil {
		logx.Errorf("dash: list: %v", err)
		return
	}
	now := time.Now()
	var rows []registry.Row
	for _, s := range sessions {
		if m.opts.OwnITerm != "" && s.ITermSessionID == m.opts.OwnITerm {
			continue
		}
		alive := procs.Alive(s.PID) && !m.forceStale[s.SessionID]
		status := registry.Derive(s, alive, now)
		if status == registry.StatusStale {
			first, seen := m.staleSince[s.SessionID]
			if !seen {
				m.staleSince[s.SessionID] = now
			} else if now.Sub(first) > staleDeleteAf {
				registry.Delete(s.SessionID)
				delete(m.staleSince, s.SessionID)
				continue
			}
		} else {
			delete(m.staleSince, s.SessionID)
		}
		if m.opts.Notify && status == registry.StatusApproval && m.lastStatus[s.SessionID] != registry.StatusApproval && m.lastStatus[s.SessionID] != "" {
			go iterm.Notify("Claude needs approval", repoBranch(s))
		}
		m.lastStatus[s.SessionID] = status
		recap, _ := registry.LoadRecap(s.SessionID)
		rows = append(rows, registry.Row{Session: s, Recap: recap, Status: status, Alive: alive})
	}
	registry.Sort(rows)
	m.all = rows
	m.applyFilter()
}

func (m *model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter))
	if q == "" {
		m.rows = m.all
	} else {
		m.rows = nil
		for _, r := range m.all {
			hay := strings.ToLower(r.Session.Title + " " + repoBranch(r.Session) + " " + recapLine(r))
			if strings.Contains(hay, q) {
				m.rows = append(m.rows, r)
			}
		}
	}
	// Keep the selection on the same session across re-sorts.
	if m.indexOf(m.selected) < 0 && len(m.rows) > 0 {
		m.selected = m.rows[0].Session.SessionID
	}
	if len(m.rows) == 0 {
		m.selected = ""
	}
	m.scrollToSelection()
}

func (m *model) indexOf(id string) int {
	for i, r := range m.rows {
		if r.Session.SessionID == id {
			return i
		}
	}
	return -1
}

func (m *model) current() *registry.Row {
	if i := m.indexOf(m.selected); i >= 0 {
		return &m.rows[i]
	}
	return nil
}

func (m *model) move(delta int) {
	if len(m.rows) == 0 {
		return
	}
	i := m.indexOf(m.selected) + delta
	if i < 0 {
		i = 0
	}
	if i >= len(m.rows) {
		i = len(m.rows) - 1
	}
	m.selected = m.rows[i].Session.SessionID
	m.scrollToSelection()
}

// rowLines is how many terminal lines one row takes in View.
const rowLines = 2

// fixedLines counts the header, rules and footer around the row area.
func (m *model) fixedLines() int {
	n := 4 // title, top rule, bottom rule, footer
	if m.opts.Usage {
		n++
	}
	return n
}

// pageRows is how many rows fit on screen. Zero means "no limit" (unknown height).
func (m *model) pageRows() int {
	if m.height <= 0 {
		return 0
	}
	n := (m.height - m.fixedLines()) / rowLines
	if n < 1 {
		n = 1
	}
	return n
}

// scrollToSelection nudges offset so the selected row is on screen and the
// window never hangs past the end of the list.
func (m *model) scrollToSelection() {
	per := m.pageRows()
	if per == 0 {
		m.offset = 0
		return
	}
	if i := m.indexOf(m.selected); i >= 0 {
		if i < m.offset {
			m.offset = i
		}
		if i >= m.offset+per {
			m.offset = i - per + 1
		}
	}
	if m.offset > len(m.rows)-per {
		m.offset = len(m.rows) - per
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.scrollToSelection()
	case registryMsg:
		m.reload()
		return m, m.waitChange()
	case tickMsg:
		m.reload()
		return m, tea.Batch(tick(), fetchTabs())
	case tabsMsg:
		m.tabs, m.tabsErr = msg.tabs, msg.err
	case focusMsg:
		if msg.err == iterm.ErrMissing {
			m.forceStale[msg.id] = true
			m.flash = "pane is gone, marked stale"
			m.reload()
		} else if msg.err != nil {
			m.flash = "focus failed: " + msg.err.Error()
			logx.Errorf("dash: focus: %v", msg.err)
		} else {
			m.flash = ""
		}
	case flashMsg:
		m.flash = string(msg)
	case usageMsg:
		if msg.snap == nil && msg.err == nil {
			return m, m.fetchUsage() // timer fired
		}
		if msg.snap != nil {
			m.usage, m.usageErr = msg.snap, nil
		} else {
			m.usageErr = msg.err
			logx.Errorf("dash: usage: %v", msg.err)
		}
		return m, usageTick()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		switch msg.Type {
		case tea.KeyEsc:
			m.filtering, m.filter = false, ""
			m.applyFilter()
		case tea.KeyEnter:
			m.filtering = false
		case tea.KeyBackspace:
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
			m.applyFilter()
		case tea.KeyRunes, tea.KeySpace:
			m.filter += string(msg.Runes)
			if msg.Type == tea.KeySpace {
				m.filter += " "
			}
			m.applyFilter()
		}
		return m, nil
	}
	m.flash = ""
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "/":
		m.filtering = true
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.applyFilter()
		}
	case "enter":
		if r := m.current(); r != nil {
			if r.Session.ITermSessionID == "" {
				m.flash = "not an iTerm session"
				return m, nil
			}
			id, uuid := r.Session.SessionID, iterm.UUID(r.Session.ITermSessionID)
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				return focusMsg{id: id, err: iterm.Focus(ctx, uuid)}
			}
		}
	case "r":
		if r := m.current(); r != nil {
			hooks.SpawnRecap(r.Session.SessionID, "--force")
			m.flash = "recap requested"
		}
	case "x":
		if r := m.current(); r != nil {
			registry.Delete(r.Session.SessionID)
			delete(m.forceStale, r.Session.SessionID)
			m.reload()
		}
	}
	return m, nil
}

// --- rendering ---

var (
	styleTitle  = lipgloss.NewStyle().Bold(true)
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleRule   = lipgloss.NewStyle().Faint(true)
	styleSel    = lipgloss.NewStyle().Reverse(true)
	styleStatus = map[string]lipgloss.Style{
		registry.StatusApproval: lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
		registry.StatusWaiting:  lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		registry.StatusWorking:  lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		registry.StatusIdle:     lipgloss.NewStyle().Faint(true),
		registry.StatusStale:    lipgloss.NewStyle().Faint(true),
	}
	glyph = map[string]string{
		registry.StatusApproval: "!",
		registry.StatusWaiting:  "●",
		registry.StatusWorking:  "◐",
		registry.StatusIdle:     "○",
		registry.StatusStale:    "×",
	}
)

func (m *model) View() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	var b strings.Builder
	active := 0
	for _, r := range m.all {
		if r.Status != registry.StatusStale {
			active++
		}
	}
	head := " CLAUDE SESSIONS"
	count := fmt.Sprintf("%d active ", active)
	pad := w - lipgloss.Width(head) - lipgloss.Width(count)
	if pad < 1 {
		pad = 1
	}
	b.WriteString(styleTitle.Render(head) + strings.Repeat(" ", pad) + styleDim.Render(count) + "\n")
	if m.opts.Usage {
		b.WriteString(m.usageLine(w) + "\n")
	}
	b.WriteString(styleRule.Render(" "+strings.Repeat("─", max(w-2, 10))) + "\n")

	if len(m.rows) == 0 {
		if len(m.all) == 0 {
			b.WriteString(styleDim.Render(" no sessions yet. run `oversight install`, then start claude in another tab.") + "\n")
		} else {
			b.WriteString(styleDim.Render(" nothing matches the filter") + "\n")
		}
	}
	per := m.pageRows()
	end := len(m.rows)
	if per > 0 && m.offset+per < end {
		end = m.offset + per
	}
	used := 0
	for _, r := range m.rows[m.offset:end] {
		sel := r.Session.SessionID == m.selected
		b.WriteString(m.renderRow(r, sel, w))
		used += rowLines
	}
	if len(m.rows) == 0 {
		used = 1
	}
	if m.height > 0 {
		for ; used < m.height-m.fixedLines(); used++ {
			b.WriteString("\n")
		}
	}

	rule := " " + strings.Repeat("─", max(w-2, 10))
	var hints []string
	if m.offset > 0 {
		hints = append(hints, fmt.Sprintf("↑ %d more", m.offset))
	}
	if end < len(m.rows) {
		hints = append(hints, fmt.Sprintf("↓ %d more", len(m.rows)-end))
	}
	if len(hints) > 0 {
		h := " " + strings.Join(hints, "  ") + " "
		if lipgloss.Width(h)+4 < w {
			rule = " ──" + h + strings.Repeat("─", max(w-3-lipgloss.Width(h)-1, 0))
		}
	}
	b.WriteString(styleRule.Render(rule) + "\n")
	switch {
	case m.filtering:
		b.WriteString(" / " + m.filter + "▏  " + styleDim.Render("enter done   esc clear"))
	case m.flash != "":
		b.WriteString(" " + m.flash)
	default:
		foot := " ↑↓ select   enter jump   / filter   r recap   x drop   q quit"
		if m.filter != "" {
			foot += styleDim.Render("   [filter: " + m.filter + "]")
		}
		if m.tabsErr != nil {
			foot += styleDim.Render("   (iTerm unreachable)")
		}
		b.WriteString(foot)
	}
	return b.String()
}

// usageLine renders the subscription windows, e.g.
// " 5h ▓▓▓▓▓▓░░░░ 62%  resets 17:40    7d ▓▓░░░░░░░░ 23%  resets thu".
// Narrower terminals get shorter bars, then percentages only.
func (m *model) usageLine(w int) string {
	if m.usage == nil {
		if m.usageErr != nil {
			return styleDim.Render(" usage unavailable")
		}
		return styleDim.Render(" usage …")
	}
	now := time.Now()
	for _, tier := range []struct {
		bar   int
		reset bool
		sep   string
	}{{10, true, "    "}, {6, true, "   "}, {4, false, "   "}, {0, false, "  "}} {
		var parts []string
		for _, l := range m.usage.Limits {
			p := fmt.Sprintf("%s %d%%", l.Label, l.Percent)
			if tier.bar > 0 {
				p = fmt.Sprintf("%s %s %d%%", l.Label, usage.Bar(l.Percent, tier.bar), l.Percent)
			}
			if r := usage.ResetLabel(l.ResetsAt, now); tier.reset && r != "" {
				p += "  resets " + r
			}
			parts = append(parts, usageStyle(l).Render(p))
		}
		line := " " + strings.Join(parts, tier.sep)
		if lipgloss.Width(line) <= w || tier.bar == 0 {
			return line
		}
	}
	return ""
}

func usageStyle(l usage.Limit) lipgloss.Style {
	switch {
	case l.Percent >= 90 || l.Severity == "critical":
		return styleStatus[registry.StatusApproval]
	case l.Percent >= 70 || l.Severity == "warning":
		return styleStatus[registry.StatusWorking]
	}
	return styleDim
}

func (m *model) renderRow(r registry.Row, selected bool, w int) string {
	st := styleStatus[r.Status]
	// " ! approval  " + name + "  5m  w1 tab 10": the name gets whatever is left.
	nameW := w - 1 - 2 - 10 - 1 - 5 - 2 - 10
	if nameW < 20 {
		nameW = 20
	}
	if nameW > 60 {
		nameW = 60
	}
	line1 := fmt.Sprintf(" %s %-9s %s %5s  %s",
		st.Render(glyph[r.Status]),
		st.Render(fmt.Sprintf("%-9s", r.Status)),
		nameColumn(r.Session, nameW),
		since(r.Session.StatusSince),
		m.tabLabel(r.Session))
	line2 := "   " + styleDim.Render(clip(recapLine(r), max(w-4, 20)))
	if selected {
		line1 = styleSel.Render(padRight(line1, w))
	}
	return line1 + "\n" + line2 + "\n"
}

// plain is the --once rendering: no styles, no selection.
func (m *model) plain() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d session(s)\n", len(m.rows))
	for _, r := range m.rows {
		name := repoBranch(r.Session)
		if r.Session.Title != "" {
			name = r.Session.Title + " · " + name
		}
		fmt.Fprintf(&b, "%-9s %-40s %5s  %s\n", r.Status, name, since(r.Session.StatusSince), m.tabLabel(r.Session))
		fmt.Fprintf(&b, "          %s\n", recapLine(r))
	}
	return b.String()
}

func (m *model) tabLabel(s *registry.Session) string {
	if s.ITermSessionID == "" {
		return styleDim.Render("no iterm")
	}
	loc, ok := m.tabs[iterm.UUID(s.ITermSessionID)]
	if !ok {
		return ""
	}
	if loc.Window > 1 {
		return fmt.Sprintf("w%d tab %d", loc.Window, loc.Tab)
	}
	return fmt.Sprintf("tab %d", loc.Tab)
}

// sessionColors maps /color names to terminal colors. Unknown names get no color.
var sessionColors = map[string]lipgloss.Color{
	"red":     "1",
	"green":   "2",
	"yellow":  "3",
	"blue":    "4",
	"magenta": "5",
	"purple":  "5",
	"pink":    "13",
	"cyan":    "6",
	"teal":    "6",
	"orange":  "208",
	"white":   "7",
	"gray":    "8",
	"grey":    "8",
}

func nameStyle(s *registry.Session) lipgloss.Style {
	if c, ok := sessionColors[s.Color]; ok {
		return lipgloss.NewStyle().Foreground(c).Bold(true)
	}
	return lipgloss.NewStyle()
}

// nameColumn is "title · repo/branch" when the session has a name, padded to
// width and painted with the session's /color. The branch is dropped before
// the title when space runs out.
func nameColumn(s *registry.Session, width int) string {
	rb := repoBranch(s)
	st := nameStyle(s)
	if s.Title == "" {
		return st.Render(padRight(clip(rb, width), width))
	}
	title := clip(s.Title, width)
	rest := width - lipgloss.Width(title)
	if rest >= 8 {
		return st.Render(title) + styleDim.Render(padRight(" · "+clip(rb, rest-3), rest))
	}
	return st.Render(padRight(title, width))
}

func repoBranch(s *registry.Session) string {
	if s.Branch == "" {
		return s.Repo
	}
	return s.Repo + "/" + s.Branch
}

func recapLine(r registry.Row) string {
	if r.Recap != nil && r.Recap.Recap != "" {
		return r.Recap.Recap
	}
	if r.Session.LastPrompt != "" {
		return "> " + r.Session.LastPrompt
	}
	return "(no activity yet)"
}

// since renders a relative duration the way the spec's mockup does.
func since(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
