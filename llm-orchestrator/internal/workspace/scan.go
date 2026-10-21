package workspace

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Stack is a technology detected in a directory of the repository.
type Stack struct {
	Dir      string `json:"dir"`
	Name     string `json:"name"`
	Evidence string `json:"evidence"`
}

const (
	maxScanDepth  = 4
	maxScanFiles  = 20_000
	maxManifestSz = 512 << 10
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, ".venv": true, "venv": true, "__pycache__": true, ".next": true,
	".nuxt": true, "coverage": true, ".idea": true, ".vscode": true, "bin": true, "obj": true,
}

// Dependency names (substring match on the manifest) mapped to stack labels.
var (
	goLibs = [][2]string{
		{"github.com/gin-gonic/gin", "Gin"}, {"github.com/labstack/echo", "Echo"},
		{"github.com/gofiber/fiber", "Fiber"}, {"github.com/go-chi/chi", "Chi"}, {"gorm.io/gorm", "GORM"},
	}
	pythonLibs = [][2]string{
		{"django", "Django"}, {"fastapi", "FastAPI"}, {"flask", "Flask"}, {"pytest", "pytest"},
		{"sqlalchemy", "SQLAlchemy"}, {"pandas", "pandas"}, {"scikit-learn", "scikit-learn"}, {"torch", "PyTorch"},
	}
	jvmLibs  = [][2]string{{"spring-boot", "Spring Boot"}, {"kotlin", "Kotlin"}}
	nodeLibs = map[string]string{
		"react": "React", "next": "Next.js", "vue": "Vue", "nuxt": "Nuxt", "@angular/core": "Angular",
		"svelte": "Svelte", "@nestjs/core": "NestJS", "express": "Express", "fastify": "Fastify",
		"typescript": "TypeScript", "vite": "Vite", "jest": "Jest", "vitest": "Vitest",
		"@playwright/test": "Playwright", "cypress": "Cypress", "tailwindcss": "Tailwind CSS",
		"@prisma/client": "Prisma", "typeorm": "TypeORM",
	}
)

// Scan walks the repository (up to a fixed depth) and detects stacks from
// manifest files. It is deterministic and does not call any LLM.
func Scan(root string) ([]Stack, error) {
	seen := map[string]bool{}
	var stacks []Stack
	add := func(dir, name, evidence string) {
		if key := dir + "\x00" + name; !seen[key] {
			seen[key] = true
			stacks = append(stacks, Stack{Dir: dir, Name: name, Evidence: evidence})
		}
	}

	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || strings.Count(rel, string(filepath.Separator)) >= maxScanDepth) {
				return filepath.SkipDir
			}
			return nil
		}
		if files++; files > maxScanFiles {
			return filepath.SkipAll
		}

		dir := filepath.ToSlash(filepath.Dir(rel))
		name := d.Name()
		lower := strings.ToLower(name)
		switch {
		case name == "go.mod":
			add(dir, "Go", name)
			matchLibs(path, goLibs, func(l string) { add(dir, l, name) })
		case name == "package.json":
			add(dir, "Node.js", name)
			for _, l := range nodeDeps(path) {
				add(dir, l, name)
			}
		case name == "pyproject.toml" || name == "requirements.txt" || name == "setup.py" || name == "Pipfile":
			add(dir, "Python", name)
			matchLibs(path, pythonLibs, func(l string) { add(dir, l, name) })
		case name == "Cargo.toml":
			add(dir, "Rust", name)
		case name == "pom.xml":
			add(dir, "Java (Maven)", name)
			matchLibs(path, jvmLibs, func(l string) { add(dir, l, name) })
		case name == "build.gradle" || name == "build.gradle.kts":
			add(dir, "JVM (Gradle)", name)
			matchLibs(path, jvmLibs, func(l string) { add(dir, l, name) })
		case name == "Gemfile":
			add(dir, "Ruby", name)
			matchLibs(path, [][2]string{{"rails", "Rails"}}, func(l string) { add(dir, l, name) })
		case name == "composer.json":
			add(dir, "PHP", name)
			matchLibs(path, [][2]string{{"laravel/framework", "Laravel"}}, func(l string) { add(dir, l, name) })
		case name == "pubspec.yaml":
			add(dir, "Dart/Flutter", name)
		case strings.HasSuffix(lower, ".csproj") || strings.HasSuffix(lower, ".sln"):
			add(dir, ".NET", name)
		case name == "angular.json":
			add(dir, "Angular", name)
		case strings.HasPrefix(lower, "playwright.config."):
			add(dir, "Playwright", name)
		case name == "Dockerfile" || strings.HasSuffix(lower, ".dockerfile"):
			add(dir, "Docker", name)
		case slices.Contains([]string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}, lower):
			add(dir, "Docker Compose", name)
		case strings.HasSuffix(lower, ".tf"):
			add(dir, "Terraform", name)
		case strings.HasSuffix(dir, ".github/workflows") && (strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")):
			add(".", "GitHub Actions", ".github/workflows")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	slices.SortFunc(stacks, func(a, b Stack) int {
		if c := strings.Compare(a.Dir, b.Dir); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return stacks, nil
}

// FormatStacks renders stacks grouped by directory, one line per directory.
func FormatStacks(stacks []Stack) string {
	if len(stacks) == 0 {
		return "- Nenhum manifesto reconhecido."
	}
	var b strings.Builder
	var dir string
	var names []string
	flush := func() {
		if len(names) > 0 {
			fmt.Fprintf(&b, "- %s: %s\n", dirLabel(dir), strings.Join(names, ", "))
		}
	}
	for _, s := range stacks {
		if s.Dir != dir {
			flush()
			dir, names = s.Dir, nil
		}
		names = append(names, s.Name)
	}
	flush()
	return strings.TrimRight(b.String(), "\n")
}

func dirLabel(dir string) string {
	if dir == "." {
		return "(raiz)"
	}
	return dir + "/"
}

func readManifest(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, maxManifestSz))
	return string(data)
}

func matchLibs(path string, libs [][2]string, found func(string)) {
	content := strings.ToLower(readManifest(path))
	for _, l := range libs {
		if strings.Contains(content, l[0]) {
			found(l[1])
		}
	}
}

func nodeDeps(path string) []string {
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal([]byte(readManifest(path)), &pkg) != nil {
		return nil
	}
	var out []string
	for dep, label := range nodeLibs {
		_, a := pkg.Dependencies[dep]
		_, b := pkg.DevDependencies[dep]
		if a || b {
			out = append(out, label)
		}
	}
	return out
}
