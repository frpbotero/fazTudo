package quality

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultProtected are the input documents agents must never edit.
var DefaultProtected = []string{"US*.md", "EDD*.md", "*user-story*.md", "*user_story*.md"}

const maxProtectedFiles = 50

// Guard remembers the content of input files so changes can be detected and
// undone after each agent step.
type Guard struct {
	root  string
	files map[string][]byte
}

// NewGuard snapshots the files whose path or base name matches one of the
// glob patterns, plus extra relative paths.
func NewGuard(root string, patterns []string, extra ...string) *Guard {
	g := &Guard{root: root, files: map[string][]byte{}}
	add := func(rel string) {
		if len(g.files) >= maxProtectedFiles {
			return
		}
		if data, ok := readFile(root, rel); ok {
			g.files[rel] = []byte(data)
		}
	}
	walk(root, func(rel string, _ fs.FileInfo) {
		if matchAny(patterns, rel) {
			add(rel)
		}
	})
	for _, rel := range extra {
		add(rel)
	}
	return g
}

func matchAny(patterns []string, rel string) bool {
	base := path.Base(rel)
	return slices.ContainsFunc(patterns, func(p string) bool {
		p = strings.TrimSpace(p)
		if p == "" {
			return false
		}
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
		ok, _ := path.Match(p, base)
		return ok && !strings.Contains(p, "/")
	})
}

// Files lists the protected paths, sorted.
func (g *Guard) Files() []string {
	if g == nil {
		return nil
	}
	out := make([]string, 0, len(g.files))
	for rel := range g.files {
		out = append(out, rel)
	}
	slices.Sort(out)
	return out
}

// Check returns the protected files that were modified or deleted.
func (g *Guard) Check() []string {
	var changed []string
	for _, rel := range g.Files() {
		data, err := os.ReadFile(filepath.Join(g.root, rel))
		if err != nil || !bytes.Equal(data, g.files[rel]) {
			changed = append(changed, rel)
		}
	}
	return changed
}

// Restore writes back the original content of the given files.
func (g *Guard) Restore(files []string) error {
	for _, rel := range files {
		orig, ok := g.files[rel]
		if !ok {
			continue
		}
		p := filepath.Join(g.root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, orig, 0o644); err != nil {
			return err
		}
	}
	return nil
}
