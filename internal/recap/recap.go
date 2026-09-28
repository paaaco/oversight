// Package recap implements `oversight recap <session-id>`: a detached worker
// that summarizes the transcript tail into one dashboard line.
package recap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/paaaco/oversight/internal/logx"
	"github.com/paaaco/oversight/internal/registry"
	"github.com/paaaco/oversight/internal/transcript"
)

const (
	Model        = "claude-haiku-4-5"
	MaxTokens    = 100
	Timeout      = 20 * time.Second
	MaxRecapLen  = 120
	MinNewLines  = 4
	MaxTurns     = 40
	MaxPromptLen = 8000
)

const SystemPrompt = `You summarize a coding session for a dashboard row.
Reply with one line, max 120 characters, no preamble.
Say what the session is doing now, and end with what it needs from the user, if anything.`

// Summarizer turns a transcript excerpt into one line.
type Summarizer func(ctx context.Context, system, body string) (string, error)

// Run is the CLI entry point. force skips the "enough new lines" check.
func Run(sessionID string, force bool) int {
	if err := Generate(sessionID, force, Default); err != nil {
		if !errors.Is(err, errSkipped) {
			logx.Errorf("recap %s: %v", sessionID, err)
		}
	}
	return 0
}

var errSkipped = errors.New("skipped")

// Generate runs the worker steps: lock, debounce, read tail, summarize, write.
func Generate(sessionID string, force bool, summarize Summarizer) error {
	sess, err := registry.LoadSession(sessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errSkipped // session started before the hooks were installed
		}
		return fmt.Errorf("load session: %w", err)
	}
	if sess.TranscriptPath == "" {
		return errors.New("session has no transcript_path")
	}

	unlock, err := lock(registry.LockPath(sessionID))
	if err != nil {
		return fmt.Errorf("%w: %v", errSkipped, err)
	}
	defer unlock()

	lines, err := transcript.CountLines(sess.TranscriptPath)
	if err != nil {
		return fmt.Errorf("count lines: %w", err)
	}
	prev, _ := registry.LoadRecap(sessionID)
	if !force && prev != nil && lines-prev.TranscriptLines < MinNewLines {
		return errSkipped
	}

	turns, err := transcript.Tail(sess.TranscriptPath, MaxTurns)
	if err != nil {
		return fmt.Errorf("read tail: %w", err)
	}
	body := transcript.Render(turns, MaxPromptLen)
	if strings.TrimSpace(body) == "" {
		return errSkipped
	}

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	text, err := summarize(ctx, SystemPrompt, body)
	if err != nil {
		return fmt.Errorf("summarize: %w", err)
	}
	text = Clean(text)
	if text == "" {
		return errors.New("empty summary")
	}
	return registry.SaveRecap(&registry.Recap{
		SessionID:       sessionID,
		Recap:           text,
		GeneratedAt:     time.Now().UTC(),
		TranscriptLines: lines,
	})
}

// Clean collapses the model reply to one line of at most MaxRecapLen runes.
func Clean(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = strings.Trim(s, "\"'`")
	r := []rune(s)
	if len(r) > MaxRecapLen {
		s = string(r[:MaxRecapLen-1]) + "…"
	}
	return s
}

// lock takes an exclusive, non-blocking flock on path.
func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another worker holds the lock")
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// Default picks the Messages API when a key is available, else `claude -p`.
func Default(ctx context.Context, system, body string) (string, error) {
	if key := APIKey(); key != "" {
		out, err := ViaAPI(ctx, key, system, body)
		if err == nil {
			return out, nil
		}
		logx.Errorf("api summarizer failed, falling back to claude -p: %v", err)
	}
	return ViaClaudeCLI(ctx, system, body)
}

// APIKey reads ANTHROPIC_API_KEY from the environment, then the macOS Keychain
// (generic password with service name ANTHROPIC_API_KEY).
func APIKey() string {
	if k := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); k != "" {
		return k
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "ANTHROPIC_API_KEY", "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ViaAPI calls the Messages API directly. The payload is small enough that the
// SDK would be more dependency than help.
func ViaAPI(ctx context.Context, key, system, body string) (string, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"model":      Model,
		"max_tokens": MaxTokens,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": body}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("api %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String(), nil
}

// ViaClaudeCLI runs `claude -p --model haiku`, billing the Claude subscription.
// CLAUDECODE is stripped so the nested run is allowed; OVERSIGHT_WORKER is
// set so our own hooks ignore the throwaway session it creates.
func ViaClaudeCLI(ctx context.Context, system, body string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", "haiku", "--system-prompt", system, "--no-session-persistence", "--setting-sources", "")
	cmd.Stdin = strings.NewReader(body)
	env := []string{"OVERSIGHT_WORKER=1"}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CLAUDECODE=") || strings.HasPrefix(kv, "OVERSIGHT_WORKER=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude -p: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}
