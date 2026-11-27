package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/quality"
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

func TestDiscoverySurvivesRestartWithCacheDir(t *testing.T) {
	repo, cache := t.TempDir(), t.TempDir()
	first, _ := NewManager(repo)
	first.SetCacheDir(cache)
	first.Store(Report{Path: repo, Summary: "mapa", CreatedAt: time.Now()})

	restarted, _ := NewManager(repo)
	restarted.SetCacheDir(cache)
	if r := restarted.Discovery(); r == nil || r.Summary != "mapa" {
		t.Fatalf("restart must reuse the saved discovery, got %+v", r)
	}

	other := t.TempDir()
	if _, err := restarted.SetPath(other); err != nil {
		t.Fatal(err)
	}
	if restarted.Discovery() != nil {
		t.Error("another repository has no saved discovery")
	}
	if _, err := restarted.SetPath(repo); err != nil {
		t.Fatal(err)
	}
	if restarted.Discovery() == nil {
		t.Error("switching back must load the repository's saved discovery")
	}

	restarted.Invalidate()
	again, _ := NewManager(repo)
	again.SetCacheDir(cache)
	if again.Discovery() != nil {
		t.Error("Refazer (Invalidate) must delete the saved discovery")
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

func TestReadInstructionsFollowsImports(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("chave privada"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, root, "AGENTS.md", "Regras gerais.")
	write(t, root, "CLAUDE.md", "@AGENTS.md\n\nVeja @docs/estilo.md, mande e-mail a dev@empresa.com.\n"+
		"Exemplo: `@docs/exemplo.md`\n```\n@docs/bloco.md\n```\n@../fora.md @"+outside+" @~/.ssh/config")
	write(t, root, "docs/estilo.md", "Use tabs. @nested/mais.md")
	write(t, root, "docs/nested/mais.md", "Nomes curtos.")
	write(t, root, "docs/exemplo.md", "NÃO INCLUIR exemplo")
	write(t, root, "docs/bloco.md", "NÃO INCLUIR bloco")
	write(t, root, ".claude/CLAUDE.md", "Rode make test.")

	got := ReadInstructions(root)
	for _, want := range []string{"### docs/estilo.md\n\nUse tabs.", "### docs/nested/mais.md\n\nNomes curtos.", "### .claude/CLAUDE.md\n\nRode make test."} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions miss %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "Regras gerais.") != 1 {
		t.Errorf("an imported file already read must not repeat:\n%s", got)
	}
	for _, bad := range []string{"NÃO INCLUIR", "chave privada"} {
		if strings.Contains(got, bad) {
			t.Errorf("instructions must not include %q:\n%s", bad, got)
		}
	}
}

func TestReadInstructionsSkipsLinkedDuplicate(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "Regra única.")
	if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Skip("symlinks unsupported")
	}
	if got := ReadInstructions(root); strings.Count(got, "Regra única.") != 1 {
		t.Errorf("CLAUDE.md linked to AGENTS.md must appear once:\n%s", got)
	}
}

func TestReadInstructionsPointsAtFolderFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "web/AGENTS.md", "CONTEÚDO DA PASTA")
	write(t, root, "api/CLAUDE.md", "x")
	write(t, root, "web/node_modules/lib/AGENTS.md", "x")
	write(t, root, ".git/AGENTS.md", "x")
	got := ReadInstructions(root)
	if !strings.Contains(got, "### Instruções por pasta") || !strings.Contains(got, "- `api/CLAUDE.md`\n- `web/AGENTS.md`") {
		t.Errorf("folder instruction files must be listed:\n%s", got)
	}
	if strings.Contains(got, "CONTEÚDO DA PASTA") || strings.Contains(got, "node_modules") || strings.Contains(got, ".git/") {
		t.Errorf("only paths outside skipped folders belong in the prompt:\n%s", got)
	}
}

func TestReadInstructionsListsWhatDoesNotFit(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", strings.Repeat("a", maxInstructionFile))
	write(t, root, "CLAUDE.md", strings.Repeat("b", maxInstructionFile))
	got := ReadInstructions(root)
	if !strings.Contains(got, "### AGENTS.md") || !strings.Contains(got, "Leia antes de agir: `CLAUDE.md`.") {
		t.Errorf("a file over the total limit must be named for the agent to read:\n%.200s…", got)
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

func TestPolicyEphemeralStacks(t *testing.T) {
	if got := (Policy{}).Normalize().EphemeralStacks; !slices.Equal(got, DefaultEphemeralStacks) {
		t.Errorf("saved policies get the default, got %v", got)
	}
	off := Policy{EphemeralStacks: []string{}}.Normalize().EphemeralStacks
	if off == nil || len(off) != 0 {
		t.Errorf("an empty list turns the cleanup off, got %#v", off)
	}
	trimmed := Policy{EphemeralStacks: []string{" a-e2e-* ", "a-e2e-*", ""}}.Normalize().EphemeralStacks
	if !slices.Equal(trimmed, []string{"a-e2e-*"}) {
		t.Errorf("trimmed = %v", trimmed)
	}

	m, _ := NewManager(t.TempDir())
	for _, pattern := range []string{"[", "*", "**", "?"} {
		p := m.Policy()
		p.EphemeralStacks = []string{pattern}
		if _, err := m.SetPolicy(p); err == nil {
			t.Errorf("pattern %q must be refused", pattern)
		}
	}
}

func TestPolicyDefaultsToParallelAndKeepsSequential(t *testing.T) {
	if DefaultPolicy().Sequential {
		t.Error("parallel is the default")
	}
	repo := t.TempDir()
	m, _ := NewManager(repo)
	p := m.Policy()
	p.Sequential = true
	saved, err := m.SetPolicy(p)
	if err != nil || !saved.Sequential || !m.Policy().Sequential {
		t.Fatalf("sequential not kept: %+v %v", saved, err)
	}
}

func TestPolicyFlowNormalization(t *testing.T) {
	p := DefaultPolicy()
	p.Flow, p.DisabledStages = FlowCustom, []string{"docs", "arch", "bogus", "docs"}
	p = p.Normalize()
	if len(p.DisabledStages) != 1 || p.DisabledStages[0] != "docs" {
		t.Fatalf("disabled = %v; the architect and unknown ids must be dropped", p.DisabledStages)
	}
	if !p.StageDisabled("docs") || p.StageDisabled("review") {
		t.Error("custom flow must disable only the chosen stages")
	}
	if !(Policy{Flow: FlowFast}).StageDisabled("review") || (Policy{Flow: FlowFast}).StageDisabled("spec") {
		t.Error("fast flow keeps the test plan and drops the review")
	}
	if (Policy{}).StageDisabled("review") {
		t.Error("an empty flow runs everything")
	}
	if err := (Policy{Flow: "x"}).Validate(); err == nil {
		t.Error("an unknown flow must be rejected")
	}
}

func TestReadInstructionsListsSpecKit(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".specify/memory/constitution.md", "# Regras")
	write(t, root, "specs/001-login/spec.md", "spec")
	write(t, root, "specs/002-checkout/spec.md", "spec")
	write(t, root, "specs/002-checkout/tasks.md", "- [ ] T001")
	if err := os.MkdirAll(filepath.Join(root, "specs", "003-vazia"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := ReadInstructions(root)
	for _, want := range []string{"### Spec Kit", "`.specify/memory/constitution.md`", "- `specs/002-checkout/`: spec.md, tasks.md\n- `specs/001-login/`: spec.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions miss %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "003-vazia") || strings.Contains(got, "T001") {
		t.Errorf("only feature paths with artifacts belong in the prompt:\n%s", got)
	}
}

func TestPolicyCheckReports(t *testing.T) {
	m, _ := NewManager(t.TempDir())
	set := func(r *quality.ReportSpec) error {
		p := m.Policy()
		p.AutoChecks = false
		p.Checks = []quality.Command{{Name: "go", Run: "go test -json ./...", Report: r}}
		_, err := m.SetPolicy(p)
		return err
	}
	for _, ok := range []*quality.ReportSpec{nil, {Format: " go-json "}, {Format: "junit", Path: "reports/junit.xml"}, {Format: "playwright-json", Path: "e2e.json"}} {
		if err := set(ok); err != nil {
			t.Errorf("%+v refused: %v", ok, err)
		}
	}
	if got := m.Policy().Checks[0].Report; got == nil || got.Format != "playwright-json" {
		t.Errorf("report not kept: %+v", got)
	}
	if err := set(&quality.ReportSpec{}); err != nil || m.Policy().Checks[0].Report != nil {
		t.Errorf("an empty report means none: %v, %+v", err, m.Policy().Checks[0].Report)
	}
	for _, bad := range []*quality.ReportSpec{{Format: "tap"}, {Format: "junit"}, {Format: "junit", Path: "../x.xml"}, {Format: "junit", Path: "/tmp/x.xml"}} {
		if err := set(bad); err == nil {
			t.Errorf("%+v must be refused", bad)
		}
	}
}

func TestPolicyProfiles(t *testing.T) {
	m, _ := NewManager(t.TempDir())
	p := m.Policy()
	p.Profiles = []string{" go ", "go", "angular-nestjs-postgres"}
	got, err := m.SetPolicy(p)
	if err != nil || !slices.Equal(got.Profiles, []string{"go", "angular-nestjs-postgres"}) {
		t.Fatalf("profiles = %v, %v", got.Profiles, err)
	}
	p.Profiles = []string{"rails"}
	if _, err := m.SetPolicy(p); err == nil {
		t.Error("an unknown profile must be refused")
	}
	if got := SuggestProfiles([]Stack{{Name: "Go"}, {Name: "NestJS"}}); !slices.Equal(got, []string{"angular-nestjs-postgres", "go"}) {
		t.Errorf("suggested = %v", got)
	}
	if got := SuggestProfiles([]Stack{{Name: "React"}}); len(got) != 0 {
		t.Errorf("suggested = %v", got)
	}
}

func TestPolicySCM(t *testing.T) {
	m, _ := NewManager(t.TempDir())
	p := m.Policy()
	if p.SCM.Mode != "" {
		t.Errorf("the default publishes nothing: %+v", p.SCM)
	}
	for _, ok := range []SCMPolicy{{}, {Mode: SCMLocal}, {Mode: SCMPullRequest, Draft: true, ReviewFrom: RiskHigh}} {
		p.SCM = ok
		if _, err := m.SetPolicy(p); err != nil {
			t.Errorf("%+v refused: %v", ok, err)
		}
	}
	for _, bad := range []SCMPolicy{{Mode: "push"}, {Mode: SCMPullRequest, ReviewFrom: "altíssimo"}} {
		p.SCM = bad
		if _, err := m.SetPolicy(p); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
