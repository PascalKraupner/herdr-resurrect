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
