# herdr-resurrect

Bring back the commands running in your [herdr](https://herdr.dev) panes after
a server restart. Herdr restores workspaces, tabs, splits, directories, and
supported agent conversations. A server or editor you started by hand normally
comes back as an empty shell. This plugin records and relaunches those commands.

## Install

Requires herdr **0.7.5+** (tested with 0.9.3), Git, curl, and tar.
Linux and macOS are supported on Intel/AMD and ARM64.

```bash
herdr plugin install PascalKraupner/herdr-resurrect
```

Installation downloads a prebuilt binary and checks its SHA-256 checksum.
If no matching release asset is available, it builds with Go 1.26+.

On the next server start, the startup hook restores the last snapshot and starts
capturing every 60 seconds. Installing into a running server does not run the
startup hook. Save explicitly before a planned restart to capture recent changes.

## Use

```bash
herdr plugin action invoke status --plugin resurrect
herdr plugin action invoke save --plugin resurrect
herdr plugin action invoke restore --plugin resurrect
```

An optional save binding in `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+alt+s"
type = "shell"
command = "herdr plugin action invoke save --plugin resurrect"
```

Run `herdr server reload-config` after adding it.

## Configuration

Find the config directory with `herdr plugin config-dir resurrect`.
Create `config.json` there to override these defaults:

```json
{
  "deny": ["ssh", "claude", "codex", "opencode", "pi", "caffeinate", "node"],
  "interval_seconds": 60,
  "keep_shell": true,
  "shell": ""
}
```

- `deny` matches executable basenames. Setting it replaces the default list.
  Agents are excluded because herdr resumes them itself. `node` and `caffeinate`
  are excluded as common agent helpers; remove `node` to restore Node servers.
- `interval_seconds` sets the time between captures.
- `keep_shell` leaves a shell under a restored command. Quitting an editor then
  returns to a prompt instead of closing the pane.
- `shell` overrides the fallback shell. The recorded shell is preferred;
  otherwise the plugin uses this setting, `$SHELL`, or fish.

An idle shell is saved with its directory but no command. Shell scripts with
operands are treated as running commands.

## What gets restored

The plugin reads `layout.export` and `pane.process_info`, then saves the layout,
directories, and foreground argv in `$HERDR_PLUGIN_STATE_DIR/snapshot.json`.
Restore uses `layout.apply` to replace matching tabs and recreates missing
workspaces. All-idle tabs that herdr already restored are left alone.

This relaunches commands; it does not resume process memory, shell variables,
unsaved editor buffers, or pipelines. Manual restore replaces matching tabs,
so use it before starting new work in those tabs.

Writes are atomic. If a capture loses more than half its tabs, the previous
capture is kept as `snapshot.prev.json`. Startup restore errors are recorded in
`last-restore-error.log` in the state directory.

Herdr 0.9.3 adds shutdown protection and layout backups, but still does not
restore arbitrary running commands. This plugin complements those features.

## Update, remove, and develop

Re-run the install command to update. To remove it:

```bash
herdr plugin uninstall resurrect
```

For a local checkout:

```bash
go test ./...
go build -o bin/herdr-resurrect .
herdr plugin link .
```

## License

MIT
