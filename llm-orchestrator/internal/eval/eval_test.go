package eval

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
)

func TestDryRunSuiteProducesScoredReport(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("CODEX_HOME", t.TempDir())

	dir := t.TempDir()
	tpl := filepath.Join(dir, "tpl")
	os.MkdirAll(filepath.Join(tpl, "node_modules", "x"), 0o755)
	os.MkdirAll(filepath.Join(tpl, ".git"), 0o755)
	os.WriteFile(filepath.Join(tpl, "US_loja.md"), []byte("# US-1: Loja\nComo cliente quero comprar.\n"), 0o644)

	suite := Suite{
		DryRun: true,
		Output: "out",
		Tasks: []Task{{
			Name: "loja", Template: "tpl", Story: "US_loja.md",
			Checks:       []quality.Command{{Name: "ok", Run: "true"}},
			HiddenChecks: []quality.Command{{Name: "passa", Run: "test -f US_loja.md"}, {Name: "falha", Run: "false"}},
		}},
		Arms: []Arm{{Name: "claude", Provider: provider.Claude}, {Name: "codex", Provider: provider.Codex}},
	}
	data, _ := json.Marshal(suite)
	path := filepath.Join(dir, "suite.json")
	os.WriteFile(path, data, 0o644)

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	router := provider.NewRouter(true, []provider.CLI{provider.ClaudeCLI, provider.CodexCLI}, provider.WithSimulationDelay(time.Millisecond))
	results, err := Run(context.Background(), loaded, router, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	for _, r := range results {
		if r.Outcome != "succeeded" || r.HiddenPassed != 1 || r.HiddenTotal != 2 || r.Score != 80 {
			t.Errorf("%s: %+v", r.Arm, r)
		}
		if fi, err := os.Lstat(filepath.Join(r.Workdir, "node_modules")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s: node_modules must be linked, not copied", r.Arm)
		}
	}
	if router.Config().DefaultProvider != provider.Codex {
		t.Error("each arm must select its provider")
	}
	report, err := os.ReadFile(filepath.Join(filepath.Dir(results[0].Workdir), "report.md"))
	if err != nil || !strings.Contains(string(report), "| claude | 80.0 |") {
		t.Fatalf("report:\n%s", report)
	}
}

func TestLoadRejectsIncompleteSuites(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{`{}`, `{"tasks":[{"name":"x","template":".","story":"nope.md"}],"arms":[{"name":"a","provider":"claude"}]}`, `{"extra":1}`} {
		p := filepath.Join(dir, "s.json")
		os.WriteFile(p, []byte(body), 0o644)
		if _, err := Load(p); err == nil {
			t.Errorf("Load(%s) must fail", body)
		}
	}
}
