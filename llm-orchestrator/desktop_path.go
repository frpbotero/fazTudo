//go:build desktop

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// loadShellPath merges the login shell's PATH into the process environment.
// Apps launched from Finder, the Dock or a Linux desktop menu inherit a
// minimal PATH, so `claude`, `codex`, `node` and `npx` would not be found.
// Windows GUI apps already receive the user's PATH from the registry.
func loadShellPath() {
	if runtime.GOOS == "windows" {
		return
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
		if runtime.GOOS == "linux" {
			shell = "/bin/bash"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const marker = "__FAZTUDO_PATH__"
	cmd := exec.CommandContext(ctx, shell, "-ilc", `printf '`+marker+`%s`+marker+`' "$PATH"`)
	cmd.Stdin = nil
	// Interactive shells may leave background children holding stdout open.
	cmd.WaitDelay = time.Second
	// Output may still hold the PATH when WaitDelay cuts the pipe; parse it anyway.
	out, _ := cmd.Output()
	var shellPath string
	if parts := strings.Split(string(out), marker); len(parts) >= 3 {
		shellPath = parts[1]
	}

	home, _ := os.UserHomeDir()
	extra := []string{
		"/opt/homebrew/bin", "/usr/local/bin",
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".claude", "local"),
	}
	os.Setenv("PATH", mergePath(shellPath, os.Getenv("PATH"), strings.Join(extra, string(os.PathListSeparator))))
}

// mergePath joins PATH lists in order, dropping duplicates and empty entries.
func mergePath(lists ...string) string {
	seen := map[string]bool{}
	var dirs []string
	for _, list := range lists {
		for _, dir := range filepath.SplitList(list) {
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}
