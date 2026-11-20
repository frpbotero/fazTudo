package workspace

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// instructionFiles are the agent instruction files read from the repository
// root, in priority order. Each CLI natively reads only its own (Claude Code
// reads CLAUDE.md, Codex reads AGENTS.md), and the orchestrator turns that
// native loading off, so they are shared with every agent explicitly.
var instructionFiles = []string{
	"AGENTS.md", "CLAUDE.md", ".claude/CLAUDE.md", ".github/copilot-instructions.md", ".cursorrules",
}

// folderInstructionFiles are the per-folder instruction files Claude Code and
// Codex would pick up natively when working inside that folder.
var folderInstructionFiles = map[string]bool{"AGENTS.md": true, "CLAUDE.md": true}

const (
	maxInstructionFile  = 8 << 10
	maxInstructionTotal = 16 << 10
	// maxImportDepth bounds @path imports; Claude Code follows up to 5 hops.
	maxImportDepth    = 4
	maxFolderFiles    = 30
	maxInstructionDir = maxScanDepth
)

// importRef matches a Claude Code import: @path at the start of a line or
// after whitespace, so e-mail addresses are not taken for imports.
var importRef = regexp.MustCompile("(?:^|\\s)@([^\\s`]+)")

// ReadInstructions returns the repository's agent instructions, one section
// per file, capped in size: the root files with the files they import, the
// paths of per-folder instruction files and the Spec Kit artifacts. It
// returns "" when there are none.
func ReadInstructions(root string) string {
	r := instructionReader{root: root, seen: map[string]bool{}}
	for _, name := range instructionFiles {
		r.add(name, 0)
	}
	if len(r.omitted) > 0 {
		fmt.Fprintf(&r.b, "### Instruções fora do limite de tamanho\n\nLeia antes de agir: %s.\n\n",
			strings.Join(quoted(r.omitted), ", "))
	}
	r.b.WriteString(folderSection(root))
	r.b.WriteString(specKitSection(root))
	return strings.TrimSpace(r.b.String())
}

type instructionReader struct {
	root    string
	b       strings.Builder
	seen    map[string]bool // real paths already read: CLAUDE.md is often a link to AGENTS.md
	omitted []string        // files that did not fit, listed so agents still read them
}

// add writes the file at rel (slash-separated, relative to root) and then the
// files it imports.
func (r *instructionReader) add(rel string, depth int) {
	path := filepath.Join(r.root, filepath.FromSlash(rel))
	real, err := filepath.EvalSymlinks(path)
	if err != nil || r.seen[real] || !fileExists(real) {
		return
	}
	r.seen[real] = true
	text, ok := readCapped(path)
	if !ok || text == "" {
		return
	}
	section := fmt.Sprintf("### %s\n\n%s\n\n", rel, text)
	if r.b.Len()+len(section) > maxInstructionTotal {
		r.omitted = append(r.omitted, rel)
		return
	}
	r.b.WriteString(section)
	if depth < maxImportDepth {
		for _, imp := range r.imports(rel, text) {
			r.add(imp, depth+1)
		}
	}
}

// imports returns the files text imports, relative to root. Imports outside
// the repository are dropped: a repository must not pull the user's private
// files (~/.ssh, other projects) into prompts sent to a provider.
func (r *instructionReader) imports(rel, text string) []string {
	rootReal, err := filepath.EvalSymlinks(r.root)
	if err != nil {
		return nil
	}
	var out []string
	for _, ref := range importRefs(text) {
		if filepath.IsAbs(ref) || strings.HasPrefix(ref, "~") {
			continue
		}
		target := filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(rel)), filepath.FromSlash(ref)))
		if target == ".." || strings.HasPrefix(target, ".."+string(filepath.Separator)) {
			continue
		}
		real, err := filepath.EvalSymlinks(filepath.Join(r.root, target))
		if err != nil || !within(rootReal, real) {
			continue
		}
		out = append(out, filepath.ToSlash(target))
	}
	return out
}

// importRefs lists the @path references of text, skipping code blocks and
// inline code, where Claude Code does not resolve them either.
func importRefs(text string) []string {
	var refs []string
	fenced := false
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		for _, m := range importRef.FindAllStringSubmatch(stripInlineCode(line), -1) {
			if ref := strings.TrimRight(m[1], ".,;:)]"); ref != "" {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

func stripInlineCode(line string) string {
	parts := strings.Split(line, "`")
	for i := 1; i < len(parts); i += 2 {
		parts[i] = ""
	}
	return strings.Join(parts, " ")
}

// folderSection points agents at the instruction files of subfolders. Only
// the paths go in the prompt: an agent reads the ones of the folders it
// touches. It returns "" when there are none.
func folderSection(root string) string {
	var paths []string
	files := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") ||
				strings.Count(rel, string(filepath.Separator)) >= maxInstructionDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if files++; files > maxScanFiles {
			return filepath.SkipAll
		}
		if filepath.Dir(rel) != "." && folderInstructionFiles[d.Name()] {
			paths = append(paths, filepath.ToSlash(rel))
			if len(paths) == maxFolderFiles {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if len(paths) == 0 {
		return ""
	}
	return "### Instruções por pasta\n\nAo criar ou alterar arquivos numa destas pastas, leia antes as instruções dela " +
		"(valem junto com as de cima):\n- " + strings.Join(quoted(paths), "\n- ") + "\n\n"
}

// readCapped returns the trimmed text of path, cut at maxInstructionFile.
func readCapped(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, maxInstructionFile+1))
	text := strings.TrimSpace(string(data))
	if len(data) > maxInstructionFile {
		text = strings.ToValidUTF8(text[:min(len(text), maxInstructionFile)], "") + "\n[… truncado]"
	}
	return text, true
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func quoted(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "`" + s + "`"
	}
	return out
}
