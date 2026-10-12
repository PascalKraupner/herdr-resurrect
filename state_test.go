package main

import (
	"encoding/json"
	"os"
	"testing"
)

// The case that shipped broken: `./vendor/bin/sail artisan horizon` is exec'd as
// `sh ./vendor/bin/sail artisan horizon`, so checking argv[0] alone classified
// every shell-script wrapper as an idle pane and silently dropped it from the
// snapshot.
func TestIsIdleShell(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		proc string
		idle bool
	}{
		{"bare fish", []string{"fish"}, "fish", true},
		{"login shell", []string{"-fish"}, "fish", true},
		{"absolute path shell", []string{"/run/current-system/sw/bin/fish"}, "fish", true},
		{"shell with flags only", []string{"bash", "-l", "-i"}, "bash", true},
		{"empty argv falls back to name", nil, "fish", true},

		{"sail wrapper", []string{"sh", "./vendor/bin/sail", "artisan", "horizon"}, "sh", false},
		{"sail kafka", []string{"sh", "./vendor/bin/sail", "artisan", "kafka:consume-all"}, "sh", false},
		{"just recipe", []string{"sh", "-c", "just shopify-tunnel"}, "sh", false},
		{"bash script", []string{"bash", "/home/pascal/bin/deploy"}, "bash", false},
		{"nvim", []string{"nvim", "."}, "nvim", false},
		{"lazygit", []string{"lazygit"}, "lazygit", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsIdleShell(c.argv, c.proc); got != c.idle {
				t.Errorf("IsIdleShell(%q, %q) = %v, want %v", c.argv, c.proc, got, c.idle)
			}
		})
	}
}

// A denied command must stay denied regardless of how it is spelled, since
// agents resume through herdr's own session.resume_agents_on_restore instead.
func TestDenied(t *testing.T) {
	cfg := DefaultConfig()
	denied := [][]string{
		{"claude"},
		{"/etc/profiles/per-user/pascal/bin/claude", "--dangerously-skip-permissions"},
		{"ssh", "prod01"},
		{},
	}
	for _, argv := range denied {
		if !cfg.Denied(argv) {
			t.Errorf("Denied(%q) = false, want true", argv)
		}
	}
	allowed := [][]string{
		{"nvim", "."},
		{"lazygit"},
		{"sh", "./vendor/bin/sail", "artisan", "horizon"},
	}
	for _, argv := range allowed {
		if cfg.Denied(argv) {
			t.Errorf("Denied(%q) = true, want false", argv)
		}
	}
}

// A restored command must keep a shell underneath it. Without that the command
// becomes the pane's own process, so quitting it closes the pane — unlike the
// pane that was captured, where quitting dropped back to a prompt.
func TestWrapCommand(t *testing.T) {
	fish := "/opt/homebrew/bin/fish"

	got := WrapCommand([]string{"nvim", "."}, fish)
	want := []string{fish, "-C", "'nvim' '.'"}
	if len(got) != len(want) {
		t.Fatalf("WrapCommand = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("WrapCommand = %q, want %q", got, want)
		}
	}

	// Non-fish shells have no -C, so the command must exec a shell afterwards.
	got = WrapCommand([]string{"lazygit"}, "/bin/zsh")
	if got[1] != "-c" || got[2] != "'lazygit'; exec /bin/zsh -i" {
		t.Errorf("zsh wrap = %q", got)
	}

	// Quoting has to survive arguments with spaces and quotes, or a restore
	// silently runs the wrong thing.
	got = WrapCommand([]string{"sh", "-c", "just it's fine"}, fish)
	if got[2] != `'sh' '-c' 'just it'\''s fine'` {
		t.Errorf("quoting = %q", got[2])
	}

	if r := WrapCommand(nil, fish); r != nil {
		t.Errorf("empty argv = %q, want nil", r)
	}
}

// An agent's children sit in the foreground alongside it, so procs[0] is often
// `node .../tmux-mcp` or `caffeinate` rather than `claude`. Checking only the
// first entry let a denied pane come back as whatever it had spawned.
func TestDeniedCoversAgentChildren(t *testing.T) {
	cfg := DefaultConfig()
	for _, argv := range [][]string{
		{"caffeinate", "-i", "-t", "300"},
		{"node", "/Users/pascal/.npm/_npx/x/node_modules/.bin/tmux-mcp", "--shell-type=fish"},
	} {
		if !cfg.Denied(argv) {
			t.Errorf("Denied(%q) = false, want true", argv)
		}
	}
}

// The snapshot records the shell each pane was using, so a restore reproduces
// that instead of guessing from $SHELL. wrapTree must also strip the field: it is
// ours, not part of herdr's LayoutNode, and the node goes to layout.apply as-is.
func TestWrapTreeUsesPaneShellAndStripsIt(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Shell = "/bin/bash" // fallback, deliberately not what the panes recorded

	root := &Node{
		Type:      "split",
		Direction: "horizontal",
		First:     &Node{Type: "pane", Command: []string{"nvim"}, Shell: "/opt/homebrew/bin/fish"},
		Second:    &Node{Type: "pane", Command: []string{"lazygit"}, Shell: "/bin/zsh"},
	}
	wrapTree(root, cfg)

	if got := root.First.Command; got[0] != "/opt/homebrew/bin/fish" || got[1] != "-C" {
		t.Errorf("fish pane = %q", got)
	}
	if got := root.Second.Command; got[0] != "/bin/zsh" || got[1] != "-c" {
		t.Errorf("zsh pane = %q", got)
	}
	if root.First.Shell != "" || root.Second.Shell != "" {
		t.Error("Shell must be cleared before layout.apply sees the node")
	}

	// No recorded shell falls back to config.
	n := &Node{Type: "pane", Command: []string{"htop"}}
	wrapTree(n, cfg)
	if n.Command[0] != "/bin/bash" {
		t.Errorf("fallback = %q", n.Command)
	}

	// keep_shell off leaves the command alone but still strips Shell.
	cfg.KeepShell = false
	n = &Node{Type: "pane", Command: []string{"htop"}, Shell: "/bin/zsh"}
	wrapTree(n, cfg)
	if len(n.Command) != 1 || n.Command[0] != "htop" || n.Shell != "" {
		t.Errorf("keep_shell=false = %q shell=%q", n.Command, n.Shell)
	}
}

// The disaster this guards against: restore fails after a reboot, then the
// daemon's first capture of the near-empty session overwrites the only record
// of everything that was running.
func TestSaveSnapshotKeepsGenerationOnBigShrink(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	tabs := func(n int) []Tab {
		out := make([]Tab, n)
		for i := range out {
			out[i] = Tab{WorkspaceID: "w2", TabID: "w2:t1", Root: &Node{Type: "pane", Cwd: "/tmp"}}
		}
		return out
	}
	load := func(path string) Snapshot {
		t.Helper()
		var s Snapshot
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if err := json.Unmarshal(data, &s); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		return s
	}

	if err := SaveSnapshot(Snapshot{Version: 1, Tabs: tabs(8)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(prevSnapshotPath()); !os.IsNotExist(err) {
		t.Fatal("no generation should exist after the first save")
	}

	// Losing 8 → 1 keeps the 8-tab snapshot as the previous generation.
	if err := SaveSnapshot(Snapshot{Version: 1, Tabs: tabs(1)}); err != nil {
		t.Fatal(err)
	}
	if got := len(load(prevSnapshotPath()).Tabs); got != 8 {
		t.Fatalf("previous generation has %d tabs, want 8", got)
	}
	if got := len(load(snapshotPath()).Tabs); got != 1 {
		t.Fatalf("current snapshot has %d tabs, want 1", got)
	}

	// A same-size capture must not churn the generation away.
	if err := SaveSnapshot(Snapshot{Version: 1, Tabs: tabs(1)}); err != nil {
		t.Fatal(err)
	}
	if got := len(load(prevSnapshotPath()).Tabs); got != 8 {
		t.Fatalf("generation churned to %d tabs, want 8", got)
	}

	// Ordinary closing of one tab among many does not rotate either.
	if err := SaveSnapshot(Snapshot{Version: 1, Tabs: tabs(8)}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSnapshot(Snapshot{Version: 1, Tabs: tabs(7)}); err != nil {
		t.Fatal(err)
	}
	if got := len(load(snapshotPath()).Tabs); got != 7 {
		t.Fatalf("current snapshot has %d tabs, want 7", got)
	}
	if got := len(load(prevSnapshotPath()).Tabs); got != 8 {
		t.Fatalf("generation should still hold 8 tabs, got %d", got)
	}
}

func TestTreeHasCommandsAndFirstCwd(t *testing.T) {
	tree := &Node{
		Type:  "split",
		First: &Node{Type: "pane", Cwd: "/a"},
		Second: &Node{
			Type:   "split",
			First:  &Node{Type: "pane"},
			Second: &Node{Type: "pane", Cwd: "/b", Command: []string{"just", "dev"}},
		},
	}
	if !treeHasCommands(tree) {
		t.Fatal("tree with a command reported none")
	}
	if treeHasCommands(&Node{Type: "pane", Cwd: "/a"}) {
		t.Fatal("idle pane reported a command")
	}
	if got := firstCwd(tree); got != "/a" {
		t.Fatalf("firstCwd = %q, want /a", got)
	}
	if got := firstCwd(&Node{Type: "split", First: &Node{Type: "pane"}, Second: &Node{Type: "pane", Cwd: "/b"}}); got != "/b" {
		t.Fatalf("firstCwd = %q, want /b", got)
	}
}

// The case that brought back workspaces closed weeks earlier: herdr restored
// its session fine, the snapshot was stale, and restore recreated every
// captured workspace herdr did not have.
func TestHerdrRestored(t *testing.T) {
	cases := []struct {
		name       string
		workspaces []workspaceInfo
		want       bool
	}{
		{"cleared session: one fresh workspace", []workspaceInfo{{WorkspaceID: "w1", TabCount: 1}}, false},
		{"nothing at all", nil, false},
		{"several workspaces", []workspaceInfo{{WorkspaceID: "w3", TabCount: 1}, {WorkspaceID: "w4", TabCount: 1}}, true},
		{"one workspace with several tabs", []workspaceInfo{{WorkspaceID: "w3", TabCount: 4}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := herdrRestored(c.workspaces); got != c.want {
				t.Errorf("herdrRestored = %v, want %v", got, c.want)
			}
		})
	}
}

// Focus events start a daemon whenever none is running, so a second one must
// not get the lock while the first holds it, and the lock must free up again.
func TestDaemonLockIsSingleton(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	if daemonRunning() {
		t.Fatal("no daemon should be running yet")
	}
	first, err := lockDaemon()
	if err != nil || first == nil {
		t.Fatalf("first lock: %v, %v", first, err)
	}
	if !daemonRunning() {
		t.Error("daemonRunning = false while the lock is held")
	}
	second, err := lockDaemon()
	if err != nil || second != nil {
		t.Fatalf("second lock = %v, %v; want nil, nil", second, err)
	}
	first.Close()
	if daemonRunning() {
		t.Error("daemonRunning = true after the holder released the lock")
	}
}
