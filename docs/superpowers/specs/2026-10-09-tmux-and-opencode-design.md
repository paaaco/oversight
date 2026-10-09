# tmux and OpenCode support

Date: 2026-10-09

## Goal

Run oversight on Linux inside tmux, tracking both Claude Code and OpenCode
sessions, with Enter jumping to the right tmux pane. The existing macOS +
iTerm2 mode keeps working unchanged. One binary, one state directory, one
dashboard for both agents.

## Non-goals

- Recaps for OpenCode sessions. Its transcript store moves between versions
  (flat JSON under `~/.local/share/opencode/storage/` in older builds, a
  SQLite database in newer ones). OpenCode rows show the last prompt and the
  hand-written note only. Revisit once we know which version is in use.
- Usage line for anything other than the Claude subscription.
- `/rename` and `/color` for OpenCode; those are read from the Claude
  transcript and stay Claude-only.
- Multiple tmux servers (non-default sockets). One server per machine.
- Windows.

## Terminal hosts

A small `internal/host` package replaces direct use of `internal/iterm`
from the dashboard. Two implementations: `iterm` (the existing package,
unchanged behind the interface) and `tmux` (new).

```go
type Location struct{ Window, Tab int; Label string }

type Host interface {
    Name() string                                        // "iterm" | "tmux"
    Locations(ctx) (map[string]Location, error)          // pane id -> where it is
    Focus(ctx, paneID string) error                      // ErrMissing when gone
    Notify(title, body string)
}
```

### Which host, where

- **Hook side** records whatever is in the environment: `ITERM_SESSION_ID`
  into `iterm_session_id` (as today) and `TMUX_PANE` into a new `tmux_pane`
  field. Both can be set at once (tmux inside iTerm on macOS); both are
  stored.
- **Dash side** picks one host at startup: tmux when `$TMUX` is set,
  otherwise iTerm on darwin, otherwise none. The chosen host decides which
  stored id a row is addressed by, which label is shown, and how Enter
  focuses. A row with no id for the chosen host shows `no tmux` / `no iterm`
  and Enter flashes `not a tmux session` / `not an iTerm session`.
- The dashboard hides its own pane via `OwnITerm` (existing) and a new
  `OwnTmuxPane` read from `TMUX_PANE`.

### tmux backend

- Pane id: `TMUX_PANE`, e.g. `%12`. Stable for the pane's lifetime, unique
  per server.
- `Locations`: one call,
  `tmux list-panes -a -F '#{pane_id}\t#{session_name}\t#{window_index}\t#{pane_index}'`.
  Label is `session:window.pane`, e.g. `dev:2.1`. Window and Tab in
  `Location` carry window index and pane index for the existing layout code.
- `Focus`: `tmux switch-client -t <pane>` then `tmux select-window -t <pane>`
  then `tmux select-pane -t <pane>`. `switch-client` fails harmlessly when
  the dashboard is not itself inside tmux; the other two still move whatever
  client is attached. A pane that no longer exists returns `ErrMissing`
  (detected from `list-panes` output before calling focus, so the error text
  is ours, not tmux's).
- `Notify`: `notify-send <title> <body>` when `notify-send` is on `PATH`,
  otherwise nothing. On darwin inside tmux, keep the osascript notification.
- 4 second timeout on every tmux call, same as the iTerm scripts.

### Session cleanup across restarts

`DeleteOthersWithITerm` becomes `DeleteOthersWithPane(itermID, tmuxPane, keepID)`:
a new session that shares a tmux pane with an existing one replaces it. The
iTerm id only counts as a pane identity when neither session has a tmux
pane, because every pane of a tmux server inherits the one
`ITERM_SESSION_ID` the server was started with. The same rule applies to
hiding the dashboard's own pane. This covers `/clear`, `--resume`, and
quitting one agent and starting the other in the same pane.

## Agents

The registry gains `agent` (`"claude"` | `"opencode"`, empty means claude
for files written before this change). The dashboard shows it as a short
tag in the row, the header reads `SESSIONS`, and the filter matches on it.
Everything else (status, note, recap slot, drop, jump) is agent-agnostic.

### How events arrive

`oversight hook <Event>` gains two flags, used only by OpenCode:
`--agent opencode` and `--pid N`. Without them behaviour is exactly as now
(agent claude, pid from the ancestor walk). The JSON on stdin keeps Claude
Code's field names; the OpenCode plugin translates into them.

### OpenCode plugin

`oversight install` writes `~/.config/opencode/plugins/oversight.ts`
(path overridable with `--opencode-plugins DIR`, skipped with
`--opencode=false`). It is a template with the binary's absolute path baked
in, and `install` overwrites it only when the embedded path differs, so a
rebuild in place needs no reinstall and a moved binary does.

The plugin subscribes to the `event` hook and shells out through `$`:

| OpenCode event                                  | oversight hook      | payload                                  |
|-------------------------------------------------|---------------------|------------------------------------------|
| `session.created`                               | `SessionStart`      | `session_id`, `cwd` (plugin `directory`) |
| `message.updated` with `info.role == "user"`    | `UserPromptSubmit`  | `session_id`, `prompt` (best effort)     |
| `permission.asked`                              | `Notification`      | `notification_type: "permission_prompt"` |
| `permission.replied`                            | `PostToolUse`       | `session_id`                             |
| `session.idle`                                  | `Stop`              | `session_id`                             |
| `session.deleted`                               | `SessionEnd`        | `session_id`                             |

Every call passes `--agent opencode --pid ${process.pid}`. `TMUX_PANE` and
`ITERM_SESSION_ID` are inherited by the child process, so the hook side
needs no change to pick them up.

Caveats carried into implementation:

- `permission.asked` also fires for auto-approved permissions; the
  immediate `permission.replied` flips the row back to working, so an
  auto-approval shows as approval for well under a second. Acceptable.
- The prompt text for `message.updated` may not be in the event payload.
  The plugin fills `prompt` when it can see text parts and leaves it empty
  otherwise; the hook already treats an empty prompt as "keep the old one".
  Verify against the installed OpenCode version during implementation and
  note the result in the README.
- Sub-sessions (OpenCode's equivalent of subagents) carry a `parentID`;
  events for those are ignored, mirroring the `agent_id` check for Claude.
- `Stop` spawns a recap worker today. The worker exits at once for
  `agent == opencode` since there is no transcript path.

## Platform details

- **Usage token:** `usage.ReadToken` tries the Keychain on darwin and
  `~/.claude/.credentials.json` everywhere (also as a darwin fallback). Same
  `ParseCredentials`. Missing file means `ErrNoToken`, shown as
  `usage unavailable`.
- **Recap API key:** environment first, Keychain on darwin only. Linux falls
  back to `claude -p` as today.
- **Process inspection:** `ps -o ppid=,comm=` and `kill(pid, 0)` already
  work on Linux. No change.
- **Build:** HEAD cross-compiles for linux today. `osascript` and `security`
  calls stay behind `runtime.GOOS == "darwin"` checks rather than build
  tags, so one binary per OS and no darwin-only files.

## Dashboard changes

- `fetchTabs` calls `host.Locations`; `tabLabel` prints `Location.Label`
  for tmux and the existing `tab N` / `wN tab N` for iTerm.
- Footer hint `(iTerm unreachable)` becomes `(<host> unreachable)`.
- Row line 1 gains the agent tag after the status, dimmed:
  ` ! approval  opencode  api/fiskaly-retry   5m   dev:2.1`.
  Claude rows show `claude` so the column stays aligned.
- `--once` output includes the agent and the host label.

## Install

`oversight install`:

1. Merges Claude hooks into `~/.claude/settings.json` (unchanged).
2. Writes the OpenCode plugin file if OpenCode's config dir exists or
   `--opencode` is passed explicitly.
3. Prints what it did for each.

README gets a Linux section: build, `install`, run inside a tmux pane,
`notify-send` optional, no Automation prompt.

## Testing

- `host/tmux`: parse `list-panes` output (tabs, odd session names with
  spaces or colons), `ErrMissing` when the pane is not in the listing,
  focus command sequence built from a pane id (exec is injected).
- `hooks`: `--agent`/`--pid` flags set the fields; `TMUX_PANE` is recorded;
  `DeleteOthersWithPane` replaces on tmux pane, and on iTerm id only when
  neither side has a tmux pane; opencode `Stop` does not
  spawn a recap.
- `install`: plugin file written with the right path, rewritten only when
  the path changes, skipped with `--opencode=false`.
- `usage`: file-based token source, missing file is `ErrNoToken`.
- `dash`: host selection from env; labels and `no tmux` placeholder; agent
  tag rendering; own pane hidden by tmux id.
- Manual: real tmux on macOS (inside iTerm) and on a Linux box with both
  agents running; Enter from a different tmux session; notification fires.

## Open questions

None blocking. The OpenCode event payload shapes are checked against the
installed version at implementation time, as noted above.
