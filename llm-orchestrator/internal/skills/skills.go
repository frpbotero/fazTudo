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
	// A temporary file of its own: another process (a second window, a
	// parallel test binary) may be installing the same bundle.
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(want)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), dst)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

// Version identifies the embedded bundle's content.
func Version() string { return version }

// Names lists the bundled skills, sorted.
func Names() []string {
	entries, _ := fs.ReadDir(bundle, path.Join(root, "skills"))
	var names []string
	for _, e := range entries {
		if e.IsDir() && Has(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names
}

// Hash is the SHA-256 of every file of a bundled skill, or "" when the skill
// is not bundled: what a call followed, whatever the bundle's other content.
func Hash(name string) string {
	if !Has(name) {
		return ""
	}
	return hashTree(path.Join(root, "skills", name))
}

// HasProfile reports whether a stack profile is bundled.
func HasProfile(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return false
	}
	_, err := fs.Stat(bundle, path.Join(root, "profiles", name+".md"))
	return err == nil
}

// Profiles lists the bundled stack profiles, sorted.
func Profiles() []string {
	entries, _ := fs.ReadDir(bundle, path.Join(root, "profiles"))
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".md"); ok && !e.IsDir() {
			names = append(names, name)
		}
	}
	return names
}

// ProfileFile is where a bundled profile lives inside dir.
func ProfileFile(dir, name string) string {
	return filepath.Join(dir, "profiles", name+".md")
}

// ProfileHash is the SHA-256 of a bundled profile, or "".
func ProfileHash(name string) string {
	if !HasProfile(name) {
		return ""
	}
	return hashTree(path.Join(root, "profiles", name+".md"))
}

func hashTree(p string) string {
	h := sha256.New()
	_ = fs.WalkDir(bundle, p, func(f string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := bundle.ReadFile(f)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", strings.TrimPrefix(f, p), len(data))
		h.Write(data)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}
