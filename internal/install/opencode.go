package install

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

//go:embed oversight.plugin.ts
var pluginTemplate string

const pluginPlaceholder = "__OVERSIGHT__"

// OpenCodeConfigDir returns ~/.config/opencode, honouring XDG_CONFIG_HOME.
func OpenCodeConfigDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode"), nil
}

// PluginPath is where the plugin lives under an OpenCode config dir.
func PluginPath(dir string) string { return filepath.Join(dir, "plugins", "oversight.ts") }

// Plugin renders the plugin source for the given oversight command.
func Plugin(command string) string {
	quoted, _ := json.Marshal(command) // a JS string literal with escapes
	return strings.Replace(pluginTemplate, pluginPlaceholder, string(quoted), 1)
}

// WritePlugin writes the plugin under dir unless an identical one is there.
func WritePlugin(dir, command string) (changed bool, err error) {
	path := PluginPath(dir)
	want := Plugin(command)
	if have, err := os.ReadFile(path); err == nil && string(have) == want {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(want), 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}
