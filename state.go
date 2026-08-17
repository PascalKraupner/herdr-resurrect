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
	PaneID  string   `json:"pane_id,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
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
}

func DefaultConfig() Config {
	return Config{
		// ssh would reconnect and possibly prompt; agents resume through
		// herdr's own session.resume_agents_on_restore instead.
		Deny:            []string{"ssh", "claude", "codex", "opencode", "pi"},
		IntervalSeconds: 60,
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

func snapshotPath() string { return filepath.Join(stateDir(), "snapshot.json") }

func SaveSnapshot(s Snapshot) error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
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
