// Package logx appends error lines to ~/.oversight/log. It never fails loudly:
// hook handlers must stay silent on stdout and always exit 0.
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Dir is the oversight state directory. Overridable via OVERSIGHT_DIR for tests.
func Dir() string {
	if d := os.Getenv("OVERSIGHT_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".oversight"
	}
	return filepath.Join(home, ".oversight")
}

// Errorf appends one line to the log file.
func Errorf(format string, args ...any) {
	dir := Dir()
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
