package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanDetectsStacksPerDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module x\n\nrequire github.com/go-chi/chi/v5 v5.0.0\n")
	write(t, root, "web/package.json", `{"dependencies":{"react":"19"},"devDependencies":{"typescript":"5","@playwright/test":"1"}}`)
	write(t, root, "ml/requirements.txt", "fastapi==0.1\npandas\n")
	write(t, root, "Dockerfile", "FROM scratch")
	write(t, root, ".github/workflows/ci.yml", "on: push")
	write(t, root, "web/node_modules/vue/package.json", `{"dependencies":{"vue":"3"}}`) // must be skipped

	stacks, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	got := FormatStacks(stacks)
	for _, want := range []string{
		"(raiz): Chi, Docker, GitHub Actions, Go",
		"ml/: FastAPI, Python, pandas",
		"web/: Node.js, Playwright, React, TypeScript",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Vue") {
		t.Errorf("node_modules must be skipped:\n%s", got)
	}
}

func TestManagerCachesDiscoveryPerRepository(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	m, err := NewManager(a)
	if err != nil {
		t.Fatal(err)
	}

	m.Store(Report{Path: a, Summary: "ok", CreatedAt: time.Now()})
	if m.Discovery() == nil {
		t.Fatal("report should be cached")
	}

	if _, err := m.SetPath(a + "/"); err != nil {
		t.Fatal(err)
	}
	if m.Discovery() == nil {
		t.Error("same repository must keep the cached discovery")
	}

	if _, err := m.SetPath(b); err != nil {
		t.Fatal(err)
	}
	if m.Discovery() != nil {
		t.Error("changing repository must drop the cached discovery")
	}

	m.Store(Report{Path: a}) // stale report for the previous repository
	if m.Discovery() != nil {
		t.Error("reports for another repository must be ignored")
	}

	m.Store(Report{Path: b})
	m.Invalidate()
	if m.Discovery() != nil {
		t.Error("Invalidate must drop the cache")
	}
}

func TestResolveRejectsInvalidPaths(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	write(t, filepath.Dir(file), "f", "x")

	for _, p := range []string{"", "  ", "/definitely/not/here", file} {
		if _, err := Resolve(p); err == nil {
			t.Errorf("Resolve(%q) should fail", p)
		}
	}
}

func TestReadInstructions(t *testing.T) {
	root := t.TempDir()
	if ReadInstructions(root) != "" {
		t.Fatal("no instruction files must yield empty text")
	}
	write(t, root, "AGENTS.md", "Use tabs.")
	write(t, root, ".github/copilot-instructions.md", "Sem dependências novas.")
	got := ReadInstructions(root)
	if !strings.Contains(got, "### AGENTS.md\n\nUse tabs.") || !strings.Contains(got, "Sem dependências novas.") {
		t.Fatalf("instructions = %q", got)
	}
}

func TestPolicyBaseBranches(t *testing.T) {
	p := DefaultPolicy()
	p.BaseBranches = map[string]string{".": " develop ", "api": "", " front ": "release/2.0"}
	p = p.Normalize()
	if len(p.BaseBranches) != 2 || p.BaseBranches["."] != "develop" || p.BaseBranches["front"] != "release/2.0" {
		t.Fatalf("normalized = %v", p.BaseBranches)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"-f", "dev elop", "a..b:c", "x\ny"} {
		p.BaseBranches = map[string]string{".": bad}
		if p.Validate() == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	p.BaseBranches = map[string]string{"api": ""}
	if p = p.Normalize(); p.BaseBranches != nil {
		t.Errorf("empty choices must clear the map: %v", p.BaseBranches)
	}
}
