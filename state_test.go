package main

import "testing"

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
