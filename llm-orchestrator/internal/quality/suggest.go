package quality

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// npmScripts are package.json scripts worth running as checks, in run order:
// cheap static checks first, then unit tests, then browser tests.
var npmScripts = []string{
	"lint", "typecheck", "type-check", "check",
	"test", "test:unit", "test:integration", "test:e2e", "e2e",
}

// Suggest proposes check commands from the manifests at the repository root
// and in its first-level directories. It never runs anything.
func Suggest(root string) []Command {
	var out []Command
	dirs := []string{"."}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !skipDirs[e.Name()] {
				dirs = append(dirs, e.Name())
			}
		}
	}
	for _, dir := range dirs {
		out = append(out, suggestDir(root, dir)...)
	}
	return out
}

func suggestDir(root, dir string) []Command {
	abs := filepath.Join(root, dir)
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(abs, name))
		return err == nil
	}
	var cmds []Command
	add := func(name, run string) {
		if dir != "." {
			name = dir + ": " + name
			run = "cd " + shellQuote(dir) + " && " + run
		}
		cmds = append(cmds, Command{Name: name, Run: run})
	}

	if has("package.json") {
		runner := "npm run"
		switch {
		case has("pnpm-lock.yaml"):
			runner = "pnpm run"
		case has("yarn.lock"):
			runner = "yarn run"
		case has("bun.lockb") || has("bun.lock"):
			runner = "bun run"
		}
		scripts := packageScripts(filepath.Join(abs, "package.json"))
		hasE2E := false
		for _, name := range npmScripts {
			body, ok := scripts[name]
			if !ok || isPlaceholderTest(body) {
				continue
			}
			// "test" usually aggregates the specific suites; skip it when they exist.
			if name == "test" && (scripts["test:unit"] != "" || scripts["test:e2e"] != "") {
				continue
			}
			if strings.Contains(name, "e2e") || strings.Contains(body, "playwright") {
				hasE2E = true
			}
			add(name, runner+" "+name)
		}
		if !hasE2E && hasPlaywrightConfig(abs) {
			add("e2e", "npx playwright test")
		}
	}
	if has("go.mod") {
		add("go vet", "go vet ./...")
		add("go test", "go test ./...")
	}
	if has("Cargo.toml") {
		add("cargo test", "cargo test")
	}
	if has("pyproject.toml") || has("requirements.txt") || has("setup.py") || has("pytest.ini") {
		if has("pytest.ini") || has("tests") || has("conftest.py") {
			add("pytest", "python -m pytest -q")
		}
	}
	switch {
	case has("pom.xml"):
		add("maven test", "mvn -q test")
	case has("gradlew"):
		add("gradle test", "./gradlew test")
	}
	return cmds
}

func packageScripts(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return nil
	}
	return pkg.Scripts
}

// isPlaceholderTest matches the "test" script npm init writes.
func isPlaceholderTest(body string) bool {
	return strings.Contains(body, "no test specified")
}

func hasPlaywrightConfig(dir string) bool {
	return slices.ContainsFunc([]string{"ts", "js", "mjs", "cjs", "mts"}, func(ext string) bool {
		_, err := os.Stat(filepath.Join(dir, "playwright.config."+ext))
		return err == nil
	})
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
