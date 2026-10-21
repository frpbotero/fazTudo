package quality

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// skipDirs are never scanned: dependencies, build output and test reports.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, ".venv": true, "venv": true, "__pycache__": true, ".next": true,
	".nuxt": true, "coverage": true, ".idea": true, ".vscode": true, "bin": true, "obj": true,
	"test-results": true, "playwright-report": true, ".pytest_cache": true, ".turbo": true,
}

const (
	maxWalkFiles = 20_000
	maxReadBytes = 2 << 20
)

// walk calls fn with the slash-separated relative path of every regular file
// outside skipDirs, up to maxWalkFiles.
func walk(root string, fn func(rel string, info fs.FileInfo)) {
	n := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if n++; n > maxWalkFiles {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		fn(filepath.ToSlash(rel), info)
		return nil
	})
}

// ChangedSince lists files modified at or after t, sorted.
func ChangedSince(root string, t time.Time) []string {
	var out []string
	walk(root, func(rel string, info fs.FileInfo) {
		if !info.ModTime().Before(t) {
			out = append(out, rel)
		}
	})
	slices.Sort(out)
	return out
}

func readFile(root, rel string) (string, bool) {
	info, err := os.Stat(filepath.Join(root, rel))
	if err != nil || info.Size() > maxReadBytes {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return "", false
	}
	return string(data), true
}

func ext(rel string) string { return strings.ToLower(filepath.Ext(rel)) }
