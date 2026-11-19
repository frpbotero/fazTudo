// Package skills ships the agent playbooks the pipeline stages follow. They
// are embedded in the binary and written to a cache directory that agents
// read, so every machine runs the same pinned versions and no agent depends
// on what the user installed (Claude runs with --safe-mode, which loads no
// user skill).
package skills

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed bundle
var bundle embed.FS

const root = "bundle"

// version identifies the embedded content; a new bundle gets a new directory
// instead of overwriting one a running agent may be reading.
var version = func() string {
	h := sha256.New()
	_ = fs.WalkDir(bundle, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := bundle.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(data))
		h.Write(data)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

// Has reports whether a skill is bundled. A name is one path element.
func Has(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return false
	}
	_, err := fs.Stat(bundle, path.Join(root, "skills", name, "SKILL.md"))
	return err == nil
}

// File is where a bundled skill's playbook lives inside dir.
func File(dir, name string) string {
	return filepath.Join(dir, "skills", name, "SKILL.md")
}

var mu sync.Mutex

// Dir returns the directory holding the bundled skills under the user cache
// directory, writing every file whose content differs from the embedded copy.
// Rechecking on each call restores a playbook an agent or user changed.
func Dir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return Install(filepath.Join(base, "faztudo", "skills"))
}

// Install writes the bundle to base/<version> and returns that directory.
func Install(base string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	dir := filepath.Join(base, version)
	err := fs.WalkDir(bundle, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, filepath.FromSlash(p))
		if err != nil {
			return err
		}
		return writeIfChanged(bundle, p, filepath.Join(dir, rel))
	})
	if err != nil {
		return "", fmt.Errorf("instalar skills em %s: %w", dir, err)
	}
	return dir, nil
}

// writeIfChanged writes the embedded file p to dst unless dst already matches it.
func writeIfChanged(fsys embed.FS, p, dst string) error {
	want, err := fsys.ReadFile(p)
	if err != nil {
		return err
	}
	if have, err := os.ReadFile(dst); err == nil && bytes.Equal(have, want) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, want, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
