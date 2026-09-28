// Package install merges the oversight hook entries into ~/.claude/settings.json.
package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Events are the hook events the registry is fed by.
var Events = []string{"SessionStart", "UserPromptSubmit", "Notification", "PostToolUse", "Stop", "SessionEnd"}

const marker = "oversight hook "

// SettingsPath returns ~/.claude/settings.json.
func SettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// Run merges hooks into the settings file at path, backing it up first.
// command is the executable to invoke, e.g. an absolute path to oversight.
func Run(path, command string) (changed bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	changed = Merge(settings, command)
	if !changed {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if len(raw) > 2 {
		backup := path + ".bak-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return false, fmt.Errorf("backup: %w", err)
		}
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// Merge adds one oversight hook per event to settings, in place. It is
// idempotent: an existing entry whose command ends in "oversight hook <Event>"
// is left alone (and repointed if its command differs).
func Merge(settings map[string]any, command string) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	changed := false
	for _, ev := range Events {
		want := command + " hook " + ev
		groups, _ := hooks[ev].([]any)
		if repoint(groups, ev, want) {
			changed = true
			continue
		}
		if hasHook(groups, ev) {
			continue
		}
		entry := map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": want}},
		}
		if ev == "PostToolUse" {
			entry["matcher"] = "*"
		}
		hooks[ev] = append(groups, entry)
		changed = true
	}
	return changed
}

func eachHook(groups []any, fn func(h map[string]any) bool) bool {
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if hm != nil && fn(hm) {
				return true
			}
		}
	}
	return false
}

func isOurs(h map[string]any, ev string) bool {
	cmd, _ := h["command"].(string)
	return strings.HasSuffix(cmd, marker+ev)
}

func hasHook(groups []any, ev string) bool {
	return eachHook(groups, func(h map[string]any) bool { return isOurs(h, ev) })
}

// repoint updates an existing entry's command when the binary moved.
func repoint(groups []any, ev, want string) bool {
	return eachHook(groups, func(h map[string]any) bool {
		if isOurs(h, ev) && h["command"] != want {
			h["command"] = want
			return true
		}
		return false
	})
}
