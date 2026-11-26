package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Fingerprint identifies the files a discovery depends on: manifests,
// lockfiles, agent instructions, API contracts and architecture documents.
// A discovery stays valid while none of them changes; code changes alone do
// not alter the stacks, conventions and architecture it describes.
type Fingerprint struct {
	// Commit is the repository's HEAD when the fingerprint was taken, for
	// reference; validity is decided by the files.
	Commit string `json:"commit,omitempty"`
	// Files maps each relevant path (slash-separated, relative to the root)
	// to the SHA-256 of its content.
	Files map[string]string `json:"files"`
	// Truncated marks a walk cut at maxFingerprintFiles: some files were not
	// considered, so the fingerprint cannot vouch for them.
	Truncated bool `json:"truncated,omitempty"`
}

const maxFingerprintFiles = 2_000

var (
	manifestNames = []string{
		"go.mod", "package.json", "pyproject.toml", "setup.py", "setup.cfg", "pipfile", "cargo.toml", "pom.xml",
		"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "gemfile", "composer.json",
		"pubspec.yaml", "angular.json", "tsconfig.json", "nx.json", "turbo.json", "pnpm-workspace.yaml",
		"dockerfile", "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml", "makefile",
		"playwright.config.ts", "playwright.config.js",
	}
	lockNames = []string{
		"go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "poetry.lock", "pipfile.lock", "cargo.lock",
		"gemfile.lock", "composer.lock", "pubspec.lock", "bun.lockb",
	}
	instructionNames = []string{"agents.md", "claude.md", "gemini.md", "contributing.md", "readme.md", "architecture.md", "constitution.md"}
	contractExts     = []string{".proto", ".graphql", ".gql", ".prisma"}
)

// relevantFile reports whether a discovery depends on the file at rel.
func relevantFile(rel string) bool {
	name := strings.ToLower(path.Base(rel))
	ext := path.Ext(name)
	switch {
	case slices.Contains(manifestNames, name), slices.Contains(lockNames, name), slices.Contains(instructionNames, name):
		return true
	case strings.HasPrefix(name, "requirements") && ext == ".txt":
		return true
	case ext == ".csproj" || ext == ".sln":
		return true
	case slices.Contains(contractExts, ext):
		return true
	case (strings.HasPrefix(name, "openapi") || strings.HasPrefix(name, "swagger")) && slices.Contains([]string{".yaml", ".yml", ".json"}, ext):
		return true
	}
	dir := strings.ToLower(path.Dir(rel))
	// Architecture decisions and guides.
	for _, part := range strings.Split(dir, "/") {
		if part == "adr" || part == "adrs" || part == "decisions" || part == "architecture" {
			return ext == ".md"
		}
	}
	return false
}

// TakeFingerprint hashes the relevant files under root, walking as deep as
// Scan does and skipping the same directories.
func TakeFingerprint(root string) Fingerprint {
	fp := Fingerprint{Files: map[string]string{}}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p != root && (skipDirs[d.Name()] || strings.Count(rel, "/") >= maxScanDepth) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !relevantFile(rel) {
			return nil
		}
		if len(fp.Files) >= maxFingerprintFiles {
			fp.Truncated = true
			return filepath.SkipAll
		}
		if sum, ok := hashFile(p); ok {
			fp.Files[rel] = sum
		}
		return nil
	})
	return fp
}

func hashFile(p string) (string, bool) {
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// FileChange is a relevant file that differs between two fingerprints.
type FileChange struct {
	Path   string `json:"path"`
	Change string `json:"change"` // "alterado", "novo" or "removido"
}

// Changes lists the relevant files that differ from cur, sorted by path.
func (f Fingerprint) Changes(cur Fingerprint) []FileChange {
	var out []FileChange
	for _, p := range slices.Sorted(maps.Keys(cur.Files)) {
		old, ok := f.Files[p]
		switch {
		case !ok:
			out = append(out, FileChange{Path: p, Change: "novo"})
		case old != cur.Files[p]:
			out = append(out, FileChange{Path: p, Change: "alterado"})
		}
	}
	for _, p := range slices.Sorted(maps.Keys(f.Files)) {
		if _, ok := cur.Files[p]; !ok {
			out = append(out, FileChange{Path: p, Change: "removido"})
		}
	}
	slices.SortStableFunc(out, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	return out
}
