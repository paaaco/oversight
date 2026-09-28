package usage

import (
	"testing"
	"time"
)

const fixture = `{
 "five_hour": {"utilization": 17.0, "resets_at": "2026-09-28T17:29:59.563378+00:00"},
 "seven_day": {"utilization": 19.0, "resets_at": "2026-10-01T08:59:59.563397+00:00"},
 "limits": [
  {"kind": "weekly_scoped", "group": "weekly", "percent": 37, "severity": "normal", "resets_at": "2026-10-01T08:59:59.563548+00:00", "scope": {"model": {"id": null, "display_name": "Fable"}}, "is_active": true},
  {"kind": "session", "group": "session", "percent": 17, "severity": "normal", "resets_at": "2026-09-28T17:29:59.563378+00:00", "scope": null},
  {"kind": "weekly_all", "group": "weekly", "percent": 19, "severity": "normal", "resets_at": "2026-10-01T08:59:59.563397+00:00", "scope": null},
  {"kind": "something_new", "percent": 99}
 ]
}`

func TestParseLimits(t *testing.T) {
	snap, err := Parse([]byte(fixture), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []Limit{{Label: "5h", Percent: 17}, {Label: "7d", Percent: 19}, {Label: "7d fable", Percent: 37}}
	if len(snap.Limits) != len(want) {
		t.Fatalf("got %+v", snap.Limits)
	}
	for i, w := range want {
		if snap.Limits[i].Label != w.Label || snap.Limits[i].Percent != w.Percent || snap.Limits[i].ResetsAt.IsZero() {
			t.Fatalf("limit %d: %+v", i, snap.Limits[i])
		}
	}
}

func TestParseFallsBackToWindows(t *testing.T) {
	snap, err := Parse([]byte(`{"five_hour":{"utilization":50.4,"resets_at":null},"seven_day":{"utilization":2.6}}`), time.Now())
	if err != nil || len(snap.Limits) != 2 || snap.Limits[0].Percent != 50 || snap.Limits[1].Percent != 3 {
		t.Fatalf("%v %+v", err, snap)
	}
	if _, err := Parse([]byte(`{}`), time.Now()); err == nil {
		t.Fatal("empty response should error")
	}
}

func TestParseCredentials(t *testing.T) {
	tok, err := ParseCredentials([]byte(`{"claudeAiOauth":{"accessToken":"sk-x","expiresAt":1790628208886,"refreshToken":"r"}}` + "\n"))
	if err != nil || tok.Access != "sk-x" || tok.ExpiresAt.Year() != 2026 {
		t.Fatalf("%v %+v", err, tok)
	}
	if _, err := ParseCredentials([]byte(`{"claudeAiOauth":{}}`)); err != ErrNoToken {
		t.Fatalf("want ErrNoToken, got %v", err)
	}
}

func TestBarAndReset(t *testing.T) {
	if got := Bar(62, 10); got != "▓▓▓▓▓▓░░░░" {
		t.Fatalf("bar: %q", got)
	}
	if got := Bar(0, 4); got != "░░░░" {
		t.Fatalf("bar0: %q", got)
	}
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.Local)
	if got := ResetLabel(now.Add(3*time.Hour), now); got != "13:00" {
		t.Fatalf("same day: %q", got)
	}
	if got := ResetLabel(now.Add(72*time.Hour), now); got != "thu" {
		t.Fatalf("weekday: %q", got)
	}
	if got := ResetLabel(time.Time{}, now); got != "" {
		t.Fatalf("zero: %q", got)
	}
}
