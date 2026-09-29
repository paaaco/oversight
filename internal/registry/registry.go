// Package registry stores one JSON file pair per live Claude Code session.
//
// <session_id>.json is owned by the hook handler, <session_id>.recap.json by
// the recap worker and <session_id>.note.json by the dashboard. Every write
// is atomic (temp file + rename).
package registry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/paaaco/oversight/internal/logx"
)

const Version = 1

// Stored statuses, written by the hook handler.
const (
	StatusWorking  = "working"
	StatusApproval = "approval"
	StatusWaiting  = "waiting"
)

// Derived statuses, computed by the dashboard at read time and never stored.
const (
	StatusIdle  = "idle"
	StatusStale = "stale"
)

// IdleAfter is how long a waiting session sits before it is shown as idle.
var IdleAfter = 30 * time.Minute

type Session struct {
	Version        int       `json:"version"`
	SessionID      string    `json:"session_id"`
	ITermSessionID string    `json:"iterm_session_id"`
	PID            int       `json:"pid"`
	CWD            string    `json:"cwd"`
	Repo           string    `json:"repo"`
	Branch         string    `json:"branch"`
	Title          string    `json:"title,omitempty"` // /rename name, or Claude's auto title
	Color          string    `json:"color,omitempty"` // /color name, e.g. "green"
	TranscriptPath string    `json:"transcript_path"`
	Status         string    `json:"status"`
	StatusSince    time.Time `json:"status_since"`
	LastEvent      string    `json:"last_event"`
	LastPrompt     string    `json:"last_prompt"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Recap struct {
	SessionID       string    `json:"session_id"`
	Recap           string    `json:"recap"`
	GeneratedAt     time.Time `json:"generated_at"`
	TranscriptLines int       `json:"transcript_lines"`
}

// Note is a hand-written label for a session, set from the dashboard.
type Note struct {
	SessionID string    `json:"session_id"`
	Note      string    `json:"note"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SetStatus changes the status and resets status_since only on a real change.
func (s *Session) SetStatus(status string, now time.Time) {
	if s.Status != status {
		s.Status = status
		s.StatusSince = now
	}
	s.UpdatedAt = now
}

// Dir returns the sessions directory, creating it if needed.
func Dir() string {
	d := filepath.Join(logx.Dir(), "sessions")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func SessionPath(id string) string { return filepath.Join(Dir(), id+".json") }
func RecapPath(id string) string   { return filepath.Join(Dir(), id+".recap.json") }
func LockPath(id string) string    { return filepath.Join(Dir(), id+".recap.lock") }
func NotePath(id string) string    { return filepath.Join(Dir(), id+".note.json") }

// WriteAtomic writes v as JSON to path via a temp file and rename.
func WriteAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func LoadSession(id string) (*Session, error) {
	data, err := os.ReadFile(SessionPath(id))
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func SaveSession(s *Session) error { return WriteAtomic(SessionPath(s.SessionID), s) }

func LoadRecap(id string) (*Recap, error) {
	data, err := os.ReadFile(RecapPath(id))
	if err != nil {
		return nil, err
	}
	var r Recap
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func SaveRecap(r *Recap) error { return WriteAtomic(RecapPath(r.SessionID), r) }

func LoadNote(id string) (*Note, error) {
	data, err := os.ReadFile(NotePath(id))
	if err != nil {
		return nil, err
	}
	var n Note
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// SaveNote stamps UpdatedAt and writes the note file.
func SaveNote(n *Note) error {
	n.UpdatedAt = time.Now().UTC()
	return WriteAtomic(NotePath(n.SessionID), n)
}

// DeleteNote removes the note file. A missing file is fine.
func DeleteNote(id string) {
	if err := os.Remove(NotePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		logx.Errorf("delete note %s: %v", id, err)
	}
}

// Delete removes every file belonging to a session. Missing files are fine.
func Delete(id string) {
	for _, p := range []string{SessionPath(id), RecapPath(id), LockPath(id), NotePath(id)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			logx.Errorf("delete %s: %v", p, err)
		}
	}
}

// ListSessions loads every session file. Unreadable files are logged and skipped.
func ListSessions() ([]*Session, error) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return nil, err
	}
	var out []*Session
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".recap.json") || strings.HasSuffix(name, ".note.json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		s, err := LoadSession(id)
		if err != nil {
			logx.Errorf("load %s: %v", name, err)
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// DeleteOthersWithITerm removes sessions sharing an iTerm pane, except keepID.
// This covers /clear and --resume in the same tab, which mint a new session_id.
func DeleteOthersWithITerm(itermID, keepID string) {
	if itermID == "" {
		return
	}
	sessions, err := ListSessions()
	if err != nil {
		return
	}
	for _, s := range sessions {
		if s.SessionID != keepID && s.ITermSessionID == itermID {
			Delete(s.SessionID)
		}
	}
}

// Row is a session as the dashboard sees it: stored data plus derived status.
type Row struct {
	Session *Session
	Recap   *Recap
	Note    *Note
	Status  string // stored status, or idle / stale when derived
	Alive   bool
}

// Derive computes the displayed status from the stored one.
func Derive(s *Session, alive bool, now time.Time) string {
	if !alive {
		return StatusStale
	}
	if s.Status == StatusWaiting && now.Sub(s.StatusSince) > IdleAfter {
		return StatusIdle
	}
	return s.Status
}

var statusRank = map[string]int{
	StatusApproval: 0,
	StatusWaiting:  1,
	StatusWorking:  2,
	StatusIdle:     3,
	StatusStale:    4,
}

// Sort orders rows: approval, waiting, working, idle, stale; newest status_since first.
func Sort(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := statusRank[rows[i].Status], statusRank[rows[j].Status]
		if ri != rj {
			return ri < rj
		}
		return rows[i].Session.StatusSince.After(rows[j].Session.StatusSince)
	})
}
