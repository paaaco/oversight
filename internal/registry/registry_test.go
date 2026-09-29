package registry

import (
	"testing"
	"time"
)

func TestSessionRoundTripAndDelete(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	s := &Session{Version: 1, SessionID: "abc", ITermSessionID: "w0t1p0:UUID", Status: StatusWorking, StatusSince: now}
	if err := SaveSession(s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSession("abc")
	if err != nil || got.ITermSessionID != "w0t1p0:UUID" {
		t.Fatalf("load: %v %+v", err, got)
	}
	if err := SaveRecap(&Recap{SessionID: "abc", Recap: "hi", TranscriptLines: 3}); err != nil {
		t.Fatal(err)
	}
	list, _ := ListSessions()
	if len(list) != 1 {
		t.Fatalf("want 1 session, got %d (recap file must not count)", len(list))
	}
	Delete("abc")
	if _, err := LoadSession("abc"); err == nil {
		t.Fatal("session should be gone")
	}
	if _, err := LoadRecap("abc"); err == nil {
		t.Fatal("recap should be gone")
	}
}

func TestDeleteOthersWithITerm(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	_ = SaveSession(&Session{SessionID: "old", ITermSessionID: "w0t1p0:X"})
	_ = SaveSession(&Session{SessionID: "other", ITermSessionID: "w0t2p0:Y"})
	_ = SaveSession(&Session{SessionID: "new", ITermSessionID: "w0t1p0:X"})
	DeleteOthersWithITerm("w0t1p0:X", "new")
	list, _ := ListSessions()
	ids := map[string]bool{}
	for _, s := range list {
		ids[s.SessionID] = true
	}
	if ids["old"] || !ids["new"] || !ids["other"] {
		t.Fatalf("unexpected survivors: %v", ids)
	}
}

func TestSetStatusKeepsSinceWhenUnchanged(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	s := &Session{}
	s.SetStatus(StatusWorking, t0)
	s.SetStatus(StatusWorking, t0.Add(time.Minute))
	if !s.StatusSince.Equal(t0) {
		t.Fatalf("status_since moved on same status: %v", s.StatusSince)
	}
	s.SetStatus(StatusWaiting, t0.Add(2*time.Minute))
	if !s.StatusSince.Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("status_since not reset on change")
	}
}

func TestDeriveAndSort(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	mk := func(id, status string, ago time.Duration) Row {
		s := &Session{SessionID: id, Status: status, StatusSince: now.Add(-ago)}
		return Row{Session: s, Status: Derive(s, true, now), Alive: true}
	}
	rows := []Row{
		mk("idle", StatusWaiting, 45*time.Minute),
		mk("work", StatusWorking, time.Minute),
		mk("wait-old", StatusWaiting, 5*time.Minute),
		mk("appr", StatusApproval, 10*time.Minute),
		mk("wait-new", StatusWaiting, time.Minute),
	}
	dead := &Session{SessionID: "dead", Status: StatusWorking, StatusSince: now}
	rows = append(rows, Row{Session: dead, Status: Derive(dead, false, now)})
	Sort(rows)
	want := []string{"appr", "wait-new", "wait-old", "work", "idle", "dead"}
	for i, w := range want {
		if rows[i].Session.SessionID != w {
			t.Fatalf("pos %d: want %s got %s", i, w, rows[i].Session.SessionID)
		}
	}
	if rows[4].Status != StatusIdle || rows[5].Status != StatusStale {
		t.Fatalf("derived statuses wrong: %s %s", rows[4].Status, rows[5].Status)
	}
}

func TestNoteRoundTripAndDelete(t *testing.T) {
	t.Setenv("OVERSIGHT_DIR", t.TempDir())
	_ = SaveSession(&Session{SessionID: "abc", Status: StatusWorking})
	if err := SaveNote(&Note{SessionID: "abc", Note: "fix POS refunds"}); err != nil {
		t.Fatal(err)
	}
	n, err := LoadNote("abc")
	if err != nil || n.Note != "fix POS refunds" || n.UpdatedAt.IsZero() {
		t.Fatalf("load: %v %+v", err, n)
	}
	if list, _ := ListSessions(); len(list) != 1 {
		t.Fatalf("note file must not count as a session: %d", len(list))
	}
	DeleteNote("abc")
	if _, err := LoadNote("abc"); err == nil {
		t.Fatal("note should be gone")
	}
	DeleteNote("abc") // idempotent
	_ = SaveNote(&Note{SessionID: "abc", Note: "again"})
	Delete("abc")
	if _, err := LoadNote("abc"); err == nil {
		t.Fatal("Delete should remove the note too")
	}
}
