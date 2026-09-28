// Package hooks implements `oversight hook <event>`.
//
// Rules: read JSON from stdin, touch at most one session file, never print to
// stdout, never exit non-zero. Errors go to the log file.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/paaaco/oversight/internal/logx"
	"github.com/paaaco/oversight/internal/procs"
	"github.com/paaaco/oversight/internal/registry"
	"github.com/paaaco/oversight/internal/transcript"
)

// WorkerEnv marks processes spawned by oversight itself. The recap worker's
// `claude -p` fallback would otherwise register a phantom session via hooks.
const WorkerEnv = "OVERSIGHT_WORKER"

// Input is the union of hook payload fields we read.
type Input struct {
	SessionID        string `json:"session_id"`
	TranscriptPath   string `json:"transcript_path"`
	CWD              string `json:"cwd"`
	HookEventName    string `json:"hook_event_name"`
	Source           string `json:"source"`
	Prompt           string `json:"prompt"`
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
	Reason           string `json:"reason"`
	AgentID          string `json:"agent_id"`
}

// Env is what Handle needs from the environment, injectable for tests.
type Env struct {
	ITermSessionID string
	ClaudePID      func() int
	Branch         func(cwd string) string
	SpawnRecap     func(sessionID string)
	Now            func() time.Time
	Meta           func(transcriptPath string) transcript.Meta
}

// DefaultEnv reads the real environment.
func DefaultEnv() Env {
	return Env{
		ITermSessionID: os.Getenv("ITERM_SESSION_ID"),
		ClaudePID:      procs.ClaudePID,
		Branch:         GitBranch,
		SpawnRecap:     func(id string) { SpawnRecap(id) },
		Now:            time.Now,
		Meta:           transcript.ReadMeta,
	}
}

// Run is the CLI entry point. It always returns 0.
func Run(event string, stdin io.Reader) int {
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
	if err := Handle(event, in, DefaultEnv()); err != nil {
		logx.Errorf("hook %s (%s): %v", event, in.SessionID, err)
	}
	return 0
}

const maxPrompt = 120

// Handle applies one event to the registry.
func Handle(event string, in Input, env Env) error {
	if in.SessionID == "" {
		return errors.New("no session_id")
	}
	if in.AgentID != "" {
		return nil // subagent hooks belong to the parent session; ignore
	}
	now := env.Now().UTC()
	switch event {
	case "SessionStart":
		return sessionStart(in, env, now)
	case "UserPromptSubmit":
		return update(in.SessionID, func(s *registry.Session) bool {
			s.SetStatus(registry.StatusWorking, now)
			s.LastEvent = event
			if p := truncate(strings.TrimSpace(in.Prompt), maxPrompt); p != "" {
				s.LastPrompt = p
			}
			refreshTitle(s, in, env)
			return true
		})
	case "Notification":
		return update(in.SessionID, func(s *registry.Session) bool {
			switch classify(in) {
			case "permission":
				s.SetStatus(registry.StatusApproval, now)
			case "idle":
				s.SetStatus(registry.StatusWaiting, now)
			default:
				return false
			}
			s.LastEvent = event
			return true
		})
	case "PostToolUse":
		return update(in.SessionID, func(s *registry.Session) bool {
			if s.Status != registry.StatusApproval {
				return false
			}
			s.SetStatus(registry.StatusWorking, now)
			s.LastEvent = event
			return true
		})
	case "Stop":
		err := update(in.SessionID, func(s *registry.Session) bool {
			s.SetStatus(registry.StatusWaiting, now)
			s.LastEvent = event
			if in.TranscriptPath != "" {
				s.TranscriptPath = in.TranscriptPath
			}
			refreshTitle(s, in, env)
			return true
		})
		if err == nil && env.SpawnRecap != nil {
			env.SpawnRecap(in.SessionID)
		}
		return err
	case "SessionEnd":
		registry.Delete(in.SessionID)
		return nil
	default:
		return errors.New("unknown event " + event)
	}
}

func sessionStart(in Input, env Env, now time.Time) error {
	s, err := registry.LoadSession(in.SessionID)
	if err != nil {
		s = &registry.Session{
			Version:   registry.Version,
			SessionID: in.SessionID,
			StartedAt: now,
			Status:    registry.StatusWaiting,
		}
		s.StatusSince = now
	}
	s.LastEvent = "SessionStart"
	s.UpdatedAt = now
	s.CWD = in.CWD
	s.Repo = filepath.Base(in.CWD)
	if in.TranscriptPath != "" {
		s.TranscriptPath = in.TranscriptPath
	}
	if env.ITermSessionID != "" {
		s.ITermSessionID = env.ITermSessionID
	}
	if env.ClaudePID != nil {
		s.PID = env.ClaudePID()
	}
	if env.Branch != nil {
		s.Branch = env.Branch(in.CWD)
	}
	refreshTitle(s, in, env)
	if err := registry.SaveSession(s); err != nil {
		return err
	}
	registry.DeleteOthersWithITerm(s.ITermSessionID, s.SessionID)
	return nil
}

// refreshTitle picks up /rename, Claude's auto title and /color from the
// transcript. No hook event fires for those commands, so they are read on the
// events that carry a transcript path; the tail scan is well under a millisecond.
func refreshTitle(s *registry.Session, in Input, env Env) {
	if env.Meta == nil {
		return
	}
	path := in.TranscriptPath
	if path == "" {
		path = s.TranscriptPath
	}
	if path == "" {
		return
	}
	meta := env.Meta(path)
	if t := truncate(meta.Title, 60); t != "" {
		s.Title = t
	}
	if meta.Color != "" {
		s.Color = meta.Color
	}
}

// update loads, mutates and saves a session. A missing session is created
// minimally so an event arriving before SessionStart (or after a lost file)
// still shows up. When fn returns false nothing is written.
func update(id string, fn func(*registry.Session) bool) error {
	s, err := registry.LoadSession(id)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil // no file: this session is not ours to track (no SessionStart seen)
	}
	if !fn(s) {
		return nil
	}
	return registry.SaveSession(s)
}

// classify maps a Notification payload to permission, idle, or other.
// notification_type is authoritative; the message text is a fallback for
// older Claude Code versions that did not send it.
func classify(in Input) string {
	switch in.NotificationType {
	case "permission_prompt", "elicitation_dialog", "elicitation_url_dialog", "agent_needs_input":
		return "permission"
	case "idle_prompt":
		return "idle"
	case "":
		m := strings.ToLower(in.Message)
		switch {
		case strings.Contains(m, "permission"):
			return "permission"
		case strings.Contains(m, "waiting for your input"), strings.Contains(m, "idle"):
			return "idle"
		}
	}
	return "other"
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// GitBranch returns the current branch of cwd, or "" outside a repo or after 200 ms.
func GitBranch(cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		// A repo with no commits yet has an unborn HEAD; symbolic-ref still names it.
		out, err = exec.CommandContext(ctx, "git", "-C", cwd, "symbolic-ref", "--short", "-q", "HEAD").Output()
		if err != nil {
			return ""
		}
	}
	return strings.TrimSpace(string(out))
}

// SpawnRecap starts `oversight recap <id>` detached so the hook returns at once.
func SpawnRecap(sessionID string, extra ...string) {
	exe, err := os.Executable()
	if err != nil {
		logx.Errorf("spawn recap: %v", err)
		return
	}
	args := append([]string{"recap", sessionID}, extra...)
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), WorkerEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		logx.Errorf("spawn recap: %v", err)
		return
	}
	_ = cmd.Process.Release()
}
