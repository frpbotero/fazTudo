package workspace

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// instructionFiles are the agent instruction files read from the repository
// root, in priority order. Each CLI natively reads only its own (Claude Code
// reads CLAUDE.md, Codex reads AGENTS.md), so they are shared with every
// agent explicitly.
var instructionFiles = []string{
	"AGENTS.md", "CLAUDE.md", ".github/copilot-instructions.md", ".cursorrules",
}

const (
	maxInstructionFile  = 8 << 10
	maxInstructionTotal = 16 << 10
)

// ReadInstructions returns the repository's agent instructions, one section
// per file, capped in size, followed by its Spec Kit artifacts. It returns ""
// when there are none.
func ReadInstructions(root string) string {
	var b strings.Builder
	for _, name := range instructionFiles {
		f, err := os.Open(filepath.Join(root, name))
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(f, maxInstructionFile+1))
		f.Close()
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		if len(data) > maxInstructionFile {
			text = strings.ToValidUTF8(text[:maxInstructionFile], "") + "\n[… truncado]"
		}
		section := fmt.Sprintf("### %s\n\n%s\n\n", name, text)
		if b.Len()+len(section) > maxInstructionTotal {
			break
		}
		b.WriteString(section)
	}
	b.WriteString(specKitSection(root))
	return strings.TrimSpace(b.String())
}
