package workspace

import (
	"os"
	"path/filepath"
)

// skillDirs are where agent skills are installed for the user: Claude Code
// reads ~/.claude/skills, Codex and other agents ~/.agents/skills.
func skillDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".agents", "skills")}
}

// InstalledSkills keeps the names of the skills installed for the user, in
// order, so prompts never point an agent at a skill it cannot load.
func InstalledSkills(names []string) []string {
	var out []string
	for _, name := range names {
		for _, dir := range skillDirs() {
			if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err == nil {
				out = append(out, name)
				break
			}
		}
	}
	return out
}
