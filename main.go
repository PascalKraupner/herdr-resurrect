// Command herdr-resurrect brings back what was actually running in herdr panes
// after a server restart.
//
// herdr already restores the session shape: workspaces, tabs, panes, layout,
// focus and each pane's cwd. What it does not restore is arbitrary processes,
// so a pane that was running `sail artisan serve` comes back as a bare shell.
// Declarative layout tools cover commands you wrote down in advance; this
// covers the ones you typed by hand.
//
//	herdr-resurrect save      capture what is running now
//	herdr-resurrect restore   replay the last capture
//	herdr-resurrect daemon    capture on an interval
//	herdr-resurrect boot      restore, then start the daemon (used by [[startup]])
//	herdr-resurrect status    show what would be restored
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	var err error
	switch cmd {
	case "save":
		err = save()
	case "restore":
		err = restore()
	case "daemon":
		err = daemon()
	case "boot":
		err = boot()
	case "status":
		err = status()
	default:
		fmt.Fprintln(os.Stderr, "usage: herdr-resurrect <save|restore|daemon|boot|status>")
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "herdr-resurrect: %v\n", err)
		os.Exit(1)
	}
}

// ── capture ─────────────────────────────────────────────────────

type workspaceInfo struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type tabInfo struct {
	TabID string `json:"tab_id"`
	Label string `json:"label"`
}

type processInfo struct {
	ProcessInfo struct {
		ForegroundProcesses []struct {
			Argv []string `json:"argv"`
			Name string   `json:"name"`
			Cwd  string   `json:"cwd"`
		} `json:"foreground_processes"`
	} `json:"process_info"`
}

func save() error {
	client := NewClient()
	cfg := LoadConfig()

	var wsList struct {
		Workspaces []workspaceInfo `json:"workspaces"`
	}
	if err := client.Call("workspace.list", nil, &wsList); err != nil {
		return err
	}

	snapshot := Snapshot{Version: 1}

	for _, ws := range wsList.Workspaces {
		var tabList struct {
			Tabs []tabInfo `json:"tabs"`
		}
		if err := client.Call("tab.list", map[string]any{"workspace_id": ws.WorkspaceID}, &tabList); err != nil {
			return err
		}

		for _, tab := range tabList.Tabs {
			var export struct {
				Layout struct {
					Root *Node `json:"root"`
				} `json:"layout"`
			}
			if err := client.Call("layout.export", map[string]any{"tab_id": tab.TabID}, &export); err != nil {
				return err
			}
			if export.Layout.Root == nil {
				continue
			}

			root := export.Layout.Root
			if !enrich(client, cfg, root) {
				// Every pane in this tab was idle, so there is nothing worth
				// restoring beyond the shape herdr already restores itself.
				continue
			}

			snapshot.Tabs = append(snapshot.Tabs, Tab{
				WorkspaceID:    ws.WorkspaceID,
				WorkspaceLabel: ws.Label,
				TabID:          tab.TabID,
				TabLabel:       tab.Label,
				Root:           root,
			})
		}
	}

	return SaveSnapshot(snapshot)
}

// enrich walks the layout tree and attaches each pane's running command.
// Returns true if any pane in the tree had one worth restoring.
func enrich(client *Client, cfg Config, node *Node) bool {
	if node == nil {
		return false
	}

	if node.Type == "split" {
		first := enrich(client, cfg, node.First)
		second := enrich(client, cfg, node.Second)
		return first || second
	}

	if node.PaneID == "" {
		return false
	}

	var info processInfo
	if err := client.Call("pane.process_info", map[string]any{"pane_id": node.PaneID}, &info); err != nil {
		return false
	}

	for _, proc := range info.ProcessInfo.ForegroundProcesses {
		argv := proc.Argv
		if len(argv) == 0 {
			continue
		}
		// An idle pane's foreground process is its shell; that means "nothing
		// running", not "restore a shell".
		if IsShell(argv[0]) || IsShell(proc.Name) {
			continue
		}
		if cfg.Denied(argv) {
			continue
		}
		node.Command = argv
		if proc.Cwd != "" {
			node.Cwd = proc.Cwd
		}
		// Panes are recreated on restore, so a stale id would be meaningless.
		node.PaneID = ""
		return true
	}

	node.PaneID = ""
	return false
}

// ── restore ─────────────────────────────────────────────────────

func restore() error {
	snapshot, err := LoadSnapshot()
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing captured yet
		}
		return err
	}

	client := NewClient()
	var failures []string

	for _, tab := range snapshot.Tabs {
		// layout.apply takes tab_id OR workspace_id, never both. With tab_id it
		// replaces that tab in place (the tab keeps its label but is assigned a
		// fresh id); with workspace_id it appends a new tab. Prefer replacing,
		// so a restore does not duplicate the tabs herdr already brought back,
		// and fall back to appending if that tab no longer exists.
		params := map[string]any{"root": tab.Root}
		if tab.TabLabel != "" {
			params["tab_label"] = tab.TabLabel
		}

		params["tab_id"] = tab.TabID
		err := client.Call("layout.apply", params, nil)
		if err == nil {
			continue
		}

		delete(params, "tab_id")
		params["workspace_id"] = tab.WorkspaceID
		if err := client.Call("layout.apply", params, nil); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", tab.TabID, err))
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("restored with errors:\n  %s", strings.Join(failures, "\n  "))
	}
	return nil
}

// ── daemon ──────────────────────────────────────────────────────

func daemon() error {
	cfg := LoadConfig()
	ticker := time.NewTicker(time.Duration(cfg.IntervalSeconds) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if err := save(); err != nil {
			// A failed capture must not kill the daemon; the server may simply
			// be restarting.
			fmt.Fprintf(os.Stderr, "herdr-resurrect: save failed: %v\n", err)
		}
	}
	return nil
}

// boot runs from the [[startup]] hook: replay the last capture, then leave a
// detached daemon behind. It must exit promptly so herdr's startup is not held
// open.
func boot() error {
	if err := restore(); err != nil {
		fmt.Fprintf(os.Stderr, "herdr-resurrect: restore: %v\n", err)
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	child := exec.Command(self, "daemon")
	child.Stdout = nil
	child.Stderr = nil
	if err := child.Start(); err != nil {
		return err
	}
	return child.Process.Release()
}

// ── status ──────────────────────────────────────────────────────

func status() error {
	snapshot, err := LoadSnapshot()
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("no snapshot yet")
			return nil
		}
		return err
	}

	if len(snapshot.Tabs) == 0 {
		fmt.Println("snapshot is empty: nothing was running")
		return nil
	}

	for _, tab := range snapshot.Tabs {
		label := tab.TabLabel
		if label == "" {
			label = tab.TabID
		}
		fmt.Printf("%s / %s\n", tab.WorkspaceLabel, label)
		printCommands(tab.Root, "  ")
	}
	return nil
}

func printCommands(node *Node, indent string) {
	if node == nil {
		return
	}
	if node.Type == "split" {
		printCommands(node.First, indent)
		printCommands(node.Second, indent)
		return
	}
	if len(node.Command) > 0 {
		fmt.Printf("%s%s\n", indent, strings.Join(node.Command, " "))
	}
}
