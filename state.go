package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Node mirrors herdr's LayoutNode. The pane variant carries the command we want
// back, which is the only field herdr's own snapshot restore does not keep.
type Node struct {
	Type string `json:"type"` // "pane" or "split"

	// pane
	PaneID string `json:"pane_id,omitempty"`
	Cwd    string `json:"cwd,omitempty"`

	// Shell the pane was running the command under, so a restore reproduces that
	// rather than guessing from $SHELL. Ours, not herdr's — restore strips it
	// before handing the node to layout.apply.
	Shell   string   `json:"shell,omitempty"`
	Command []string `json:"command,omitempty"`
	Label   string   `json:"label,omitempty"`

	// split
	Direction string  `json:"direction,omitempty"`
	Ratio     float64 `json:"ratio,omitempty"`
	First     *Node   `json:"first,omitempty"`
	Second    *Node   `json:"second,omitempty"`
}

type Tab struct {
	WorkspaceID    string `json:"workspace_id"`
	WorkspaceLabel string `json:"workspace_label,omitempty"`
	TabID          string `json:"tab_id"`
	TabLabel       string `json:"tab_label,omitempty"`
	Root           *Node  `json:"root"`
}

type Snapshot struct {
	Version int   `json:"version"`
	Tabs    []Tab `json:"tabs"`
}

type Config struct {
	// Commands never restored. Matched against argv[0]'s basename.
	// Deliberately small: herdr reports real argv, so unlike tmux-resurrect
	// there is no need to allow-list what may come back. Only list things you
	// actively do not want relaunched on boot.
	Deny []string `json:"deny"`

	// Seconds between captures.
	IntervalSeconds int `json:"interval_seconds"`

	// Shell that hosts restored commands. Empty falls back to $SHELL, then fish.
	Shell string `json:"shell"`

	// KeepShell restores a command with a shell underneath it instead of as the
	// pane's own process.
	//
	// Without it a restored pane differs from the one that was captured: the
	// command becomes PID 1 of the pane, so quitting it closes the pane, while
	// before the restart quitting it dropped back to a prompt. You notice this
	// the first time you :q a restored nvim and lose the pane.
	KeepShell bool `json:"keep_shell"`
}

func DefaultConfig() Config {
	return Config{
		// ssh would reconnect and possibly prompt; agents resume through
		// herdr's own session.resume_agents_on_restore instead.
		// caffeinate is a side effect of an agent running, never work in its own
		// right, so restoring it just holds the machine awake for nothing.
		Deny:            []string{"ssh", "claude", "codex", "opencode", "pi", "caffeinate", "node"},
		IntervalSeconds: 60,
		KeepShell:       true,
	}
}

// Shells are what an idle pane is running, so they mean "nothing to restore"
// rather than "restore a shell".
var shells = []string{"fish", "bash", "zsh", "sh", "dash", "nu", "ksh", "tcsh"}

func IsShell(name string) bool {
	return slices.Contains(shells, strings.TrimPrefix(filepath.Base(name), "-"))
}

// IsIdleShell distinguishes "this pane is sitting at a prompt" from "this pane
// is running a command that happens to be a shell script".
//
// Checking argv[0] alone is not enough, and getting this wrong silently drops
// work: `./vendor/bin/sail artisan horizon` is exec'd as
// `sh ./vendor/bin/sail artisan horizon`, so a bare IsShell(argv[0]) threw away
// every sail, just, npm-wrapper and similar long-running process. A shell only
// means "idle" when it was given nothing to do, so anything left after the
// leading flags makes this a real command worth restoring.
func IsIdleShell(argv []string, name string) bool {
	if len(argv) == 0 {
		return IsShell(name)
	}
	if !IsShell(argv[0]) && !IsShell(name) {
		return false
	}
	for _, arg := range argv[1:] {
		// A flag on its own (-l, -i, --login) still leaves the shell idle.
		// Anything else is a script path or the argument to -c.
		if !strings.HasPrefix(arg, "-") {
			return false
		}
	}
	return true
}

func (c Config) Denied(argv []string) bool {
	if len(argv) == 0 {
		return true
	}
	return slices.Contains(c.Deny, filepath.Base(argv[0]))
}

// ShellPath is the fallback shell, used only when the snapshot did not record
// which one the pane was using.
func (c Config) ShellPath() string {
	if c.Shell != "" {
		return c.Shell
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/opt/homebrew/bin/fish"
}

// quoteArgv renders argv as a single shell word-safe string.
func quoteArgv(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(quoted, " ")
}

// WrapCommand builds the argv for a restored pane so the pane survives the
// command exiting.
//
// fish -C runs the command inside an otherwise ordinary interactive session, so
// quitting it leaves a prompt. Other shells have no equivalent, hence the
// exec-the-shell-afterwards form.
func WrapCommand(argv []string, shell string) []string {
	if len(argv) == 0 {
		return argv
	}
	cmd := quoteArgv(argv)
	if IsShell(shell) && filepath.Base(shell) == "fish" {
		return []string{shell, "-C", cmd}
	}
	return []string{shell, "-c", cmd + "; exec " + shell + " -i"}
}

func configDir() string {
	if dir := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr-resurrect")
}

func stateDir() string {
	if dir := os.Getenv("HERDR_PLUGIN_STATE_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "herdr-resurrect")
}

func LoadConfig() Config {
	cfg := DefaultConfig()
	data, err := os.ReadFile(filepath.Join(configDir(), "config.json"))
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	if cfg.IntervalSeconds <= 0 {
		cfg.IntervalSeconds = DefaultConfig().IntervalSeconds
	}
	return cfg
}

func snapshotPath() string     { return filepath.Join(stateDir(), "snapshot.json") }
func prevSnapshotPath() string { return filepath.Join(stateDir(), "snapshot.prev.json") }

func SaveSnapshot(s Snapshot) error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	// A capture that lost more than half the tabs is kept as a generation
	// before being replaced. The case that destroyed real data: after a boot
	// where restore failed, the daemon's first capture of the near-empty
	// session overwrote the only record of everything that had been running.
	if cur, err := LoadSnapshot(); err == nil && len(cur.Tabs) > 0 && len(s.Tabs)*2 < len(cur.Tabs) {
		_ = os.Rename(snapshotPath(), prevSnapshotPath())
	}

	// Write and rename so a crash mid-save cannot leave a truncated snapshot
	// that would then restore a broken layout.
	tmp := snapshotPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, snapshotPath())
}

func LoadSnapshot() (Snapshot, error) {
	var s Snapshot
	data, err := os.ReadFile(snapshotPath())
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}
