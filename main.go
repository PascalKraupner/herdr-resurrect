// Command herdr-resurrect brings back what was actually running in herdr panes
// after a server restart.
//
// herdr already restores the session shape: workspaces, tabs, panes, layout,
// focus and each pane's cwd. What it does not restore is arbitrary processes,
// so a pane that was running `sail artisan serve` comes back as a bare shell.
// Declarative layout tools cover commands you wrote down in advance; this
// covers the ones you typed by hand.
//
// herdr's own restore is also not guaranteed: an OS shutdown SIGHUPs every
// pane before the server exits, which herdr reads as the user closing
// everything and clears its session file. The snapshot therefore keeps every
// tab with its cwds, not just the ones with commands, and restore recreates
// any workspace herdr failed to bring back.
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
	"path/filepath"
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
		ShellPID            int `json:"shell_pid"`
		ForegroundProcesses []struct {
			Argv []string `json:"argv"`
			Name string   `json:"name"`
			Cwd  string   `json:"cwd"`
		} `json:"foreground_processes"`
	} `json:"process_info"`
}

// resolveShell turns herdr's shell_pid into a stable path to that shell.
//
// `ps -o comm=` reports argv[0], which for a login shell is "-fish". lsof would
// give the real executable, but on Homebrew that is the versioned Cellar path,
// which stops existing at the next upgrade. So take the name and look it up on
// PATH to get the stable symlink instead.
func resolveShell(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return ""
	}
	name := strings.TrimPrefix(strings.TrimSpace(string(out)), "-")
	if name == "" || !IsShell(name) {
		return ""
	}
	if filepath.IsAbs(name) {
		return name
	}
	path, err := exec.LookPath(filepath.Base(name))
	if err != nil {
		return ""
	}
	return path
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

			// All-idle tabs stay in the snapshot too. herdr normally restores
			// their shape itself, and restore() skips them then, but when
			// herdr's session file was cleared they are the only record left.
			root := export.Layout.Root
			enrich(client, cfg, root)

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

// enrich walks the layout tree, records each pane's cwd, and attaches its
// running command when there is one worth restoring.
func enrich(client *Client, cfg Config, node *Node) {
	if node == nil {
		return
	}

	if node.Type == "split" {
		enrich(client, cfg, node.First)
		enrich(client, cfg, node.Second)
		return
	}

	if node.PaneID == "" {
		return
	}

	var info processInfo
	err := client.Call("pane.process_info", map[string]any{"pane_id": node.PaneID}, &info)

	// Panes are recreated on restore, so a stale id would be meaningless.
	node.PaneID = ""

	if err != nil {
		return
	}

	procs := info.ProcessInfo.ForegroundProcesses
	if len(procs) == 0 {
		return
	}

	// The cwd is recorded even for idle and denied panes: when restore has to
	// recreate a whole workspace, a bare shell in the right directory is what
	// stands in for the pane herdr could not bring back.
	if procs[0].Cwd != "" {
		node.Cwd = procs[0].Cwd
	}

	// Only the first entry is the pane's own command. The rest are its
	// children, and walking into them restores the wrong thing entirely: a
	// denied `claude` pane would otherwise come back as whatever MCP server it
	// happened to have spawned.
	// The deny list has to be checked against every foreground process, not just
	// the pane's own. An agent puts its children in the foreground too, so
	// procs[0] is frequently `node .../tmux-mcp` or `caffeinate` rather than
	// `claude` — and then a denied pane comes back as whatever it had spawned.
	for _, p := range procs {
		if len(p.Argv) > 0 && cfg.Denied(p.Argv) {
			return
		}
	}

	proc := procs[0]
	if len(proc.Argv) == 0 {
		return
	}

	// An idle pane's foreground process is its shell; that means "nothing
	// running", not "restore a shell". A shell with a script operand is a real
	// command, though, which is how `sail`/`just` wrappers reach us.
	if IsIdleShell(proc.Argv, proc.Name) {
		return
	}
	if cfg.Denied(proc.Argv) {
		return
	}

	node.Command = proc.Argv
	node.Shell = resolveShell(info.ProcessInfo.ShellPID)
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
	cfg := LoadConfig()

	// Which workspaces herdr itself brought back. When its session file
	// survived, all of them; when an OS shutdown cleared it, only the bare
	// startup workspace, and everything else has to be rebuilt from here.
	var wsList struct {
		Workspaces []workspaceInfo `json:"workspaces"`
	}
	if err := client.Call("workspace.list", nil, &wsList); err != nil {
		return err
	}
	existing := make(map[string]bool, len(wsList.Workspaces))
	for _, ws := range wsList.Workspaces {
		existing[ws.WorkspaceID] = true
	}

	// Workspaces recreated during this restore, old id → new id, and the
	// fresh initial tab each one came with, which the first restored tab
	// replaces so recreation does not leave an empty tab behind.
	created := map[string]string{}
	initialTab := map[string]string{}

	var failures []string
	fail := func(tab Tab, err error) {
		failures = append(failures, fmt.Sprintf("%s: %v", tab.TabID, err))
	}

	for _, tab := range snapshot.Tabs {
		// Wrapping happens here rather than in save() so the snapshot stays a
		// faithful record of what was running, and so snapshots taken before
		// this existed still restore correctly.
		wrapTree(tab.Root, cfg)

		params := map[string]any{"root": tab.Root}
		if tab.TabLabel != "" {
			params["tab_label"] = tab.TabLabel
		}

		if existing[tab.WorkspaceID] {
			// herdr restored the shape, so an all-idle tab needs nothing.
			if !treeHasCommands(tab.Root) {
				continue
			}

			// layout.apply takes tab_id OR workspace_id, never both. With
			// tab_id it replaces that tab in place (the tab keeps its label
			// but is assigned a fresh id); with workspace_id it appends a new
			// tab. Prefer replacing, so a restore does not duplicate the tabs
			// herdr already brought back, and fall back to appending if that
			// tab no longer exists.
			params["tab_id"] = tab.TabID
			if client.Call("layout.apply", params, nil) == nil {
				continue
			}
			delete(params, "tab_id")
			params["workspace_id"] = tab.WorkspaceID
			if err := client.Call("layout.apply", params, nil); err != nil {
				fail(tab, err)
			}
			continue
		}

		newWS := created[tab.WorkspaceID]
		if newWS == "" {
			var res struct {
				Workspace struct {
					WorkspaceID string `json:"workspace_id"`
					ActiveTabID string `json:"active_tab_id"`
				} `json:"workspace"`
			}
			createParams := map[string]any{"focus": false}
			if cwd := firstCwd(tab.Root); cwd != "" {
				createParams["cwd"] = cwd
			}
			if tab.WorkspaceLabel != "" {
				createParams["label"] = tab.WorkspaceLabel
			}
			if err := client.Call("workspace.create", createParams, &res); err != nil {
				fail(tab, err)
				continue
			}
			newWS = res.Workspace.WorkspaceID
			created[tab.WorkspaceID] = newWS
			initialTab[tab.WorkspaceID] = res.Workspace.ActiveTabID
		}

		if tid := initialTab[tab.WorkspaceID]; tid != "" {
			delete(initialTab, tab.WorkspaceID)
			params["tab_id"] = tid
			if client.Call("layout.apply", params, nil) == nil {
				continue
			}
			delete(params, "tab_id")
		}
		params["workspace_id"] = newWS
		if err := client.Call("layout.apply", params, nil); err != nil {
			fail(tab, err)
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("restored with errors:\n  %s", strings.Join(failures, "\n  "))
	}
	return nil
}

// treeHasCommands reports whether any pane in the tree carries a command,
// which is what decides if a tab herdr already restored needs replacing.
func treeHasCommands(node *Node) bool {
	if node == nil {
		return false
	}
	if node.Type == "split" {
		return treeHasCommands(node.First) || treeHasCommands(node.Second)
	}
	return len(node.Command) > 0
}

// firstCwd finds the first recorded cwd in the tree, used as the directory a
// recreated workspace identifies with.
func firstCwd(node *Node) string {
	if node == nil {
		return ""
	}
	if node.Type == "split" {
		if cwd := firstCwd(node.First); cwd != "" {
			return cwd
		}
		return firstCwd(node.Second)
	}
	return node.Cwd
}

// wrapTree rewrites every pane command in the tree so the pane keeps a shell
// underneath it.
func wrapTree(node *Node, cfg Config) {
	if node == nil {
		return
	}
	if node.Type == "split" {
		wrapTree(node.First, cfg)
		wrapTree(node.Second, cfg)
		return
	}

	shell := node.Shell
	// Always cleared: `shell` is our field, not part of herdr's LayoutNode, and
	// layout.apply gets this node verbatim.
	node.Shell = ""

	if !cfg.KeepShell || len(node.Command) == 0 {
		return
	}
	if shell == "" {
		shell = cfg.ShellPath()
	}
	node.Command = WrapCommand(node.Command, shell)
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
	// The startup hook's stderr is lost, so a failed restore also leaves a
	// note in the state dir where it can actually be found.
	errLog := filepath.Join(stateDir(), "last-restore-error.log")
	if err := restore(); err != nil {
		fmt.Fprintf(os.Stderr, "herdr-resurrect: restore: %v\n", err)
		_ = os.WriteFile(errLog, []byte(time.Now().Format(time.RFC3339)+" "+err.Error()+"\n"), 0o644)
	} else {
		_ = os.Remove(errLog)
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
		if _, err := os.Stat(prevSnapshotPath()); err == nil {
			fmt.Printf("a previous generation is kept at %s\n", prevSnapshotPath())
		}
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
