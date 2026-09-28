// Package usage reads the subscription rate-limit view that Claude Code's
// /usage shows, using the OAuth token Claude Code keeps in the macOS Keychain.
//
// The endpoint is internal to Claude Code and undocumented. Everything here
// degrades to "unavailable" rather than failing the dashboard, and the token
// is only ever held in memory.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	Endpoint     = "https://api.anthropic.com/api/oauth/usage"
	keychainItem = "Claude Code-credentials"
	betaHeader   = "oauth-2025-04-20"
)

// ErrNoToken means no usable Claude Code login was found in the Keychain.
var ErrNoToken = errors.New("no Claude Code login in keychain")

// ErrUnauthorized means the token was rejected; Claude Code refreshes it the
// next time a session runs.
var ErrUnauthorized = errors.New("token rejected")

// Token is the OAuth access token plus its expiry.
type Token struct {
	Access    string
	ExpiresAt time.Time
}

// ReadToken loads the token from the Keychain via the security CLI.
func ReadToken(ctx context.Context) (Token, error) {
	out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", keychainItem, "-w").Output()
	if err != nil {
		return Token{}, ErrNoToken
	}
	return ParseCredentials(out)
}

// ParseCredentials extracts the token from the keychain item's JSON.
func ParseCredentials(raw []byte) (Token, error) {
	var creds struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &creds); err != nil {
		return Token{}, ErrNoToken
	}
	if creds.OAuth.AccessToken == "" {
		return Token{}, ErrNoToken
	}
	return Token{
		Access:    creds.OAuth.AccessToken,
		ExpiresAt: time.UnixMilli(creds.OAuth.ExpiresAt),
	}, nil
}

// Limit is one rate-limit window.
type Limit struct {
	Label    string // "5h", "7d", "7d fable"
	Percent  int
	Severity string // normal, warning, critical, ... as the API says
	ResetsAt time.Time
}

// Snapshot is what the dashboard renders.
type Snapshot struct {
	Limits    []Limit
	FetchedAt time.Time
}

// Fetch calls the usage endpoint.
func Fetch(ctx context.Context, tok Token) (*Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Access)
	req.Header.Set("anthropic-beta", betaHeader)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("usage endpoint: HTTP %d", resp.StatusCode)
	}
	return Parse(body, time.Now())
}

type window struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type limitEntry struct {
	Kind     string  `json:"kind"`
	Percent  float64 `json:"percent"`
	Severity string  `json:"severity"`
	ResetsAt *string `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

// Parse builds a Snapshot from the endpoint's JSON. It prefers the `limits`
// array and falls back to the five_hour / seven_day objects.
func Parse(body []byte, now time.Time) (*Snapshot, error) {
	var raw struct {
		FiveHour *window      `json:"five_hour"`
		SevenDay *window      `json:"seven_day"`
		Limits   []limitEntry `json:"limits"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("usage endpoint: %w", err)
	}
	snap := &Snapshot{FetchedAt: now}
	for _, l := range raw.Limits {
		label := ""
		switch l.Kind {
		case "session":
			label = "5h"
		case "weekly_all":
			label = "7d"
		case "weekly_scoped":
			label = "7d"
			if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "" {
				label += " " + strings.ToLower(l.Scope.Model.DisplayName)
			}
		default:
			continue
		}
		snap.Limits = append(snap.Limits, Limit{
			Label: label, Percent: int(l.Percent + 0.5), Severity: l.Severity, ResetsAt: parseTime(l.ResetsAt),
		})
	}
	if len(snap.Limits) == 0 {
		if w := raw.FiveHour; w != nil && w.Utilization != nil {
			snap.Limits = append(snap.Limits, Limit{Label: "5h", Percent: int(*w.Utilization + 0.5), ResetsAt: parseTime(w.ResetsAt)})
		}
		if w := raw.SevenDay; w != nil && w.Utilization != nil {
			snap.Limits = append(snap.Limits, Limit{Label: "7d", Percent: int(*w.Utilization + 0.5), ResetsAt: parseTime(w.ResetsAt)})
		}
	}
	if len(snap.Limits) == 0 {
		return nil, errors.New("usage endpoint: no limits in response")
	}
	sort.SliceStable(snap.Limits, func(i, j int) bool { return rank(snap.Limits[i].Label) < rank(snap.Limits[j].Label) })
	return snap, nil
}

func rank(label string) int {
	switch {
	case label == "5h":
		return 0
	case label == "7d":
		return 1
	default:
		return 2
	}
}

func parseTime(s *string) time.Time {
	if s == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Bar renders a fixed-width utilization bar.
func Bar(percent, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := (percent*width + 50) / 100
	return strings.Repeat("▓", filled) + strings.Repeat("░", width-filled)
}

// ResetLabel says when a window resets: a clock time today, else a weekday.
func ResetLabel(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t, now = t.Local(), now.Local()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	if t.Sub(now) < 7*24*time.Hour {
		return strings.ToLower(t.Format("Mon"))
	}
	return t.Format("Jan 2")
}
