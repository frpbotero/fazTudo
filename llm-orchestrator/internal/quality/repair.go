package quality

import (
	"slices"
	"strings"
)

// Fixed lists of what an agent needs to reproduce one failure: a single test,
// a type check, a filtered log. They are no wider than the project's own
// commands, whose scripts agents can already edit.
var (
	outputFilters = []string{"tail", "head", "grep"}
	nodeTools     = []string{
		"npx jest", "npx vitest", "npx tsc", "npx ng test", "npx eslint",
		"npm test", "npm run test", "npm run build", "npm run lint",
	}
	goTools     = []string{"go test", "go build", "go vet"}
	pythonTools = []string{"pytest", "python -m pytest"}
)

// RepairCommands lists the shell commands writing agents may run: the project
// commands, minus those that start a stack, which only the orchestrator runs
// so they respect the machine's slot, plus the narrow tools of each runner, in
// each directory the commands use.
func RepairCommands(checks []Command) []string {
	var out []string
	add := func(commands ...string) {
		for _, c := range commands {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	add(outputFilters...)
	for _, c := range checks {
		if !Heavy(c) {
			add(c.Run)
		}
		dir := CommandDir(c)
		if dir != "" {
			add("cd " + dir)
		}
		for _, tool := range runnerTools(c) {
			add(tool, "CI=1 "+tool)
			if dir != "" {
				add("cd "+dir+" && "+tool, "cd "+dir+" && CI=1 "+tool)
			}
		}
	}
	return out
}

func runnerTools(c Command) []string {
	switch run := strings.ToLower(c.Run); {
	case strings.Contains(run, "npm ") || strings.Contains(run, "npx ") || strings.Contains(run, "yarn ") || strings.Contains(run, "pnpm "):
		return nodeTools
	case strings.Contains(run, "go "):
		return goTools
	case strings.Contains(run, "pytest") || strings.Contains(run, "python"):
		return pythonTools
	}
	return nil
}
