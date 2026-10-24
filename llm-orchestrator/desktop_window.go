//go:build desktop

package main

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
)

// appMenu adds File › Nova janela (⌘N / Ctrl+N) to the platform menus. Wails
// v2 drives one window per process, so each window is its own FazTudo: its own
// run, while a lock per repository keeps two windows from running in the same one.
func appMenu(logger *slog.Logger) *menu.Menu {
	m := menu.NewMenu()
	if runtime.GOOS == "darwin" {
		m.Append(menu.AppMenu())
	}
	file := m.AddSubmenu("Arquivo")
	file.AddText("Nova janela", keys.CmdOrCtrl("n"), func(*menu.CallbackData) {
		if err := openNewWindow(); err != nil {
			logger.Error("nova janela", "err", err)
		}
	})
	if runtime.GOOS == "darwin" {
		m.Append(menu.EditMenu()) // copy, paste and undo in the text fields
		m.Append(menu.WindowMenu())
	}
	return m
}

// openNewWindow starts another instance of the app. On macOS a running app is
// only brought to front when opened again, so the bundle is opened with -n.
func openNewWindow() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if bundle, ok := macBundleOf(exe); ok {
		return exec.Command("open", "-n", bundle).Start()
	}
	return exec.Command(exe).Start()
}

// macBundleOf returns the .app holding exe (App.app/Contents/MacOS/exe).
func macBundleOf(exe string) (string, bool) {
	if runtime.GOOS != "darwin" {
		return "", false
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	return bundle, strings.HasSuffix(bundle, ".app")
}
