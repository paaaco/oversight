// oversight shows every running Claude Code session in one iTerm tab.
//
// Subcommands:
//
//	oversight hook <event>       hook handler, called by Claude Code
//	oversight recap <session-id> detached recap worker
//	oversight dash [--once]      the dashboard (default)
//	oversight install            add the hooks to ~/.claude/settings.json
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/paaaco/oversight/internal/dash"
	"github.com/paaaco/oversight/internal/hooks"
	"github.com/paaaco/oversight/internal/host"
	"github.com/paaaco/oversight/internal/install"
	"github.com/paaaco/oversight/internal/recap"
)

func main() {
	args := os.Args[1:]
	cmd := "dash"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "hook":
		if len(args) < 1 {
			os.Exit(0) // never fail a hook, even a misconfigured one
		}
		os.Exit(hooks.Run(args[0], args[1:], os.Stdin))
	case "recap":
		fs := flag.NewFlagSet("recap", flag.ContinueOnError)
		force := fs.Bool("force", false, "regenerate even when the transcript barely changed")
		var id string
		if len(args) > 0 && args[0][0] != '-' {
			id, args = args[0], args[1:]
		}
		_ = fs.Parse(args)
		if id == "" {
			fmt.Fprintln(os.Stderr, "usage: oversight recap <session-id> [--force]")
			os.Exit(2)
		}
		os.Exit(recap.Run(id, *force))
	case "dash":
		fs := flag.NewFlagSet("dash", flag.ExitOnError)
		once := fs.Bool("once", false, "print the session list and exit")
		notify := fs.Bool("notify", true, "send a desktop notification when a session needs approval")
		idle := fs.Duration("idle-after", 30*time.Minute, "show waiting sessions as idle after this long")
		showUsage := fs.Bool("usage", true, "show the subscription rate-limit line (reads the Claude Code login from the keychain)")
		dividers := fs.Bool("dividers", true, "draw a rule between session rows")
		_ = fs.Parse(args)
		opts := dash.Options{
			Notify:      *notify,
			OwnITerm:    os.Getenv("ITERM_SESSION_ID"),
			OwnTmuxPane: os.Getenv("TMUX_PANE"),
			Host:        host.Detect(os.Getenv, runtime.GOOS),
			IdleAfter:   *idle,
			Usage:       *showUsage,
			Dividers:    *dividers,
		}
		var err error
		if *once {
			err = dash.Once(opts)
		} else {
			err = dash.Run(opts)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "oversight:", err)
			os.Exit(1)
		}
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		settings := fs.String("settings", "", "settings file (default ~/.claude/settings.json)")
		command := fs.String("command", "", "hook command to register (default: this binary's absolute path)")
		opencode := fs.Bool("opencode", true, "also write the OpenCode plugin when ~/.config/opencode exists")
		opencodeDir := fs.String("opencode-dir", "", "OpenCode config dir to write the plugin into (forces --opencode)")
		_ = fs.Parse(args)
		path := *settings
		if path == "" {
			p, err := install.SettingsPath()
			if err != nil {
				fmt.Fprintln(os.Stderr, "oversight:", err)
				os.Exit(1)
			}
			path = p
		}
		c := *command
		if c == "" {
			exe, err := os.Executable()
			if err != nil {
				fmt.Fprintln(os.Stderr, "oversight:", err)
				os.Exit(1)
			}
			c = exe
		}
		changed, err := install.Run(path, c)
		if err != nil {
			fmt.Fprintln(os.Stderr, "oversight:", err)
			os.Exit(1)
		}
		if changed {
			fmt.Printf("claude: hooks added to %s (backup written next to it)\n", path)
		} else {
			fmt.Printf("claude: hooks already present in %s\n", path)
		}
		if *opencode || *opencodeDir != "" {
			dir := *opencodeDir
			if dir == "" {
				d, err := install.OpenCodeConfigDir()
				if err == nil {
					if _, statErr := os.Stat(d); statErr == nil {
						dir = d
					}
				}
			}
			switch {
			case dir == "":
				fmt.Println("opencode: no ~/.config/opencode, plugin not written (use --opencode-dir to force)")
			default:
				changed, err := install.WritePlugin(dir, c)
				if err != nil {
					fmt.Fprintln(os.Stderr, "oversight:", err)
					os.Exit(1)
				}
				if changed {
					fmt.Printf("opencode: plugin written to %s\n", install.PluginPath(dir))
				} else {
					fmt.Printf("opencode: plugin up to date at %s\n", install.PluginPath(dir))
				}
			}
		}
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`usage:
  oversight                      run the dashboard
  oversight dash [--once] [--notify=false] [--usage=false] [--idle-after=30m] [--dividers=false]
  oversight install [--settings PATH] [--command CMD] [--opencode=false] [--opencode-dir DIR]
  oversight hook <Event> [--agent NAME --pid N]   (called by Claude Code / the OpenCode plugin)
  oversight recap <session-id> [--force]`)
}
