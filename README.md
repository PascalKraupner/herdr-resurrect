# herdr-resurrect

Brings back what was actually *running* in your [herdr](https://herdr.dev) panes
after a server restart.

herdr already restores the session **shape** on a cold start: workspaces, tabs,
panes, layout, focus and each pane's cwd. Supported agents resume their
conversations. What it does not restore is arbitrary processes, so a pane that
was running `sail artisan serve` comes back as a bare shell.

Declarative layout tools cover the commands you wrote down in advance. This
covers the ones you typed by hand.

herdr's own restore is also not guaranteed: an OS shutdown SIGHUPs every pane
before the server exits, which herdr reads as the user closing everything and
it clears its session file. The snapshot therefore keeps **every** tab with its
per-pane cwds, and restore recreates any workspace herdr failed to bring back,
label, tabs, layout and all. Panes whose command is denied or was idle come
back as shells in the right directory.

## Install

```bash
herdr plugin install PascalKraupner/herdr-resurrect
```

The build step downloads a prebuilt static binary for your platform, so
installing needs only `curl` and `tar`. It falls back to `go build` if no
matching release asset exists.

Nothing else to configure. The `[[startup]]` hook restores on boot and starts
the capture daemon.

## Commands

```bash
herdr-resurrect status    # what would be restored
herdr-resurrect save      # capture now
herdr-resurrect restore   # replay the last capture
```

`save` and `restore` are also exposed as herdr actions, so you can bind them:

```toml
[[keys.command]]
key = "prefix+alt+s"
type = "shell"
command = "herdr plugin action invoke save --plugin resurrect"
```

## Configuration

Optional, at `$HERDR_PLUGIN_CONFIG_DIR/config.json`:

```json
{
  "deny": ["ssh", "claude", "codex", "opencode", "pi"],
  "interval_seconds": 60
}
```

**`deny`** is a *deny* list, not an allow list. tmux-resurrect needed
`@resurrect-processes` because its one-level `ps` could not reliably tell what a
pane was running, so you had to enumerate what was safe to bring back. herdr
reports real argv through `pane.process_info`, so the default is to restore
whatever was running and only name the things you do *not* want relaunched.

The defaults exist for specific reasons: `ssh` would reconnect and may prompt,
and the coding agents already come back through herdr's own
`session.resume_agents_on_restore`, so restoring them here would double up.

A pane sitting at a prompt has its shell as the foreground process, which means
"nothing running" rather than "restore a shell": its cwd is captured, but no
command. Commands that already finished are gone by definition, so one-shot
invocations never come back.

## How it works

| Step | API |
|---|---|
| enumerate | `workspace.list`, `tab.list` |
| capture shape | `layout.export` per tab |
| capture commands | `pane.process_info` per pane |
| replay | `layout.apply` with `command` on each pane node |

`layout.export` returns the split tree; each pane node is enriched with the
argv of its foreground process and the snapshot is written to
`$HERDR_PLUGIN_STATE_DIR/snapshot.json` (atomically, so a crash mid-save cannot
leave a truncated layout).

On restore, `layout.apply` takes `tab_id` **or** `workspace_id`, never both.
With `tab_id` it replaces that tab in place, which is what avoids duplicating
the tabs herdr already brought back; if that tab is gone, it falls back to
appending a new one in the workspace. When the whole workspace is gone,
`workspace.create` rebuilds it (label and cwd included), the first restored tab
replaces its fresh initial tab, and the rest are appended. Tabs herdr already
restored that carry no commands are skipped entirely.

A capture that lost more than half its tabs is kept as
`snapshot.prev.json` before being replaced, so a capture taken right after a
failed restore can never destroy the only good record. A restore that fails at
boot leaves the error in `$HERDR_PLUGIN_STATE_DIR/last-restore-error.log`.

`layout.export` and `layout.apply` have no CLI, so this talks to
`$HERDR_SOCKET_PATH` directly with newline-delimited JSON.

## License

MIT
