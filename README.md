# oversight

A dashboard for iTerm tab 1 that lists every running Claude Code session on
the machine, its status, and a one-line recap. Enter jumps to that session's
tab.

```
 CLAUDE SESSIONS                                     3 active
 5h ▓▓░░░░░░░░ 17%  resets 17:29    7d ▓▓░░░░░░░░ 19%  resets thu
 ─────────────────────────────────────────────────────────────
 ! approval  api/fiskaly-retry              5m    tab 6
   Asking permission to run the DB seed script.
 ● waiting   receipts · salonized/fiscal-receipts  2m  tab 3
   Rounding fix done, wants you to confirm the migration.
 ◐ working   salonized/pos-refunds          now   tab 4
   Running the refund service tests.
 ─────────────────────────────────────────────────────────────
 ↑↓ select   enter jump   / filter   r recap   x drop   q quit
```

Your workflow stays the same: one `claude` per tab, started by hand. The
dashboard only watches.

## Install

```sh
go build -o ~/.local/bin/oversight .
oversight install       # adds hooks to ~/.claude/settings.json (backup kept)
oversight               # run this in tab 1
```

`install` registers the binary by absolute path, so rebuild in place or run
`install` again after moving it. Sessions started before the hooks were
installed do not show up until they are restarted.

The first `Enter` triggers a macOS Automation prompt so the terminal can
control iTerm2. Allow it once.

## How it works

One binary, three roles, sharing state through JSON files in
`~/.oversight/sessions/`:

- `oversight hook <Event>` is called by Claude Code on `SessionStart`,
  `UserPromptSubmit`, `Notification`, `PostToolUse`, `Stop` and `SessionEnd`.
  It updates that session's file and exits in a few milliseconds. It never
  prints and never exits non-zero; errors go to `~/.oversight/log`.
- `oversight recap <id>` is spawned detached on every `Stop`. It sends the
  last 40 user/assistant turns to Haiku and writes back one line.
- `oversight dash` watches the directory with fsnotify and redraws.

Statuses `working`, `approval` and `waiting` are stored. `idle` (waiting for
over 30 minutes, tune with `--idle-after`) and `stale` (process gone) are
derived at read time. Stale rows are deleted after 10 minutes, or right away
with `x`.

## Recaps and auth

The recap worker uses the Messages API with `ANTHROPIC_API_KEY` from the
environment or the macOS Keychain (`security add-generic-password -s
ANTHROPIC_API_KEY -a "$USER" -w <key>`). Without a key it runs
`claude -p --model haiku`, which bills your Claude subscription. Recaps are
skipped when fewer than 4 transcript lines changed since the last one; `r`
forces one.

## Subscription usage line

The line under the header is the same rate-limit view as `/usage` inside
Claude Code: the 5-hour window, the 7-day window, and any per-model weekly
window, each with a reset time. It comes from the OAuth login Claude Code
keeps in the macOS Keychain (`Claude Code-credentials`); the first read
triggers a Keychain "allow access" prompt for `oversight`. The token is
only held in memory and polled once a minute.

The endpoint is internal to Claude Code and undocumented, so the line reads
"usage unavailable" rather than breaking anything when it changes. Turn it
off with `--usage=false`.

## Keys

| Key | Action |
|-----|--------|
| `↑` `↓` / `k` `j` | Move selection |
| `Enter` | Focus that session's iTerm tab |
| `/` | Filter by repo, branch or recap text |
| `r` | Force a recap for the selected session |
| `x` | Delete the selected entry |
| `q` | Quit |

## Flags

- `dash --once` prints the list and exits.
- `dash --notify=false` turns off the macOS notification on `approval`.
- `dash --usage=false` hides the subscription usage line.
- `dash --idle-after 1h` changes the idle threshold.
- `install --settings PATH --command CMD` for non-default locations.

## Session names and colors

A session renamed with `/rename` shows its name before the repo and branch,
and the auto-generated title Claude Code gives unnamed sessions is used
otherwise. A color set with `/color` paints the name. Both are read from the
transcript on the next prompt or turn end after the command, so they can lag
by one turn.

## Notes

- Claude started outside iTerm (VS Code terminal) is listed, but Enter says
  "not an iTerm session".
- `/clear` and `--resume` mint a new session id; the older entry for the same
  pane is removed on `SessionStart`.
- The transcript JSONL format is internal to Claude Code. The parser skips
  anything it does not understand and a parse failure only means no recap.
