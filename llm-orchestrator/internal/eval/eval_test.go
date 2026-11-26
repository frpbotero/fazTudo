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
		// Only the hidden checks were observed: a simulation validates no
		// delivery, traces no test and runs no verification.
		if r.Outcome != "succeeded" || r.HiddenPassed != 1 || r.HiddenTotal != 2 || r.Score != 20 ||
			!r.Simulated || r.Validation != "not_evaluated" || len(r.NotEvaluated) != 3 {
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
	if err != nil || !strings.Contains(string(report), "Nenhum braço ranqueado") ||
		!strings.Contains(string(report), "| claude | 20.0 | succeeded (simulado, fora do ranking) |") {
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

func TestScoreGivesNothingForWhatWasNotObserved(t *testing.T) {
	empty, missing := score(Result{Outcome: "succeeded"})
	if empty != 0 || len(missing) != 4 {
		t.Errorf("no observation: score %.1f, not evaluated %v", empty, missing)
	}

	full, missing := score(Result{
		Outcome: "succeeded", Validation: "passed", Verified: true,
		HiddenPassed: 2, HiddenTotal: 2, TestsFound: 3, TestsTotal: 3,
	})
	if full != 100 || len(missing) != 0 {
		t.Errorf("everything observed and passing: score %.1f, not evaluated %v", full, missing)
	}

	// A run that finished with its delivery rejected earns no validation.
	rejected, _ := score(Result{Outcome: "succeeded", Validation: "failed", Verified: true, HiddenPassed: 1, HiddenTotal: 1, TestsFound: 1, TestsTotal: 1})
	if rejected != 80 {
		t.Errorf("rejected delivery score = %.1f, want 80", rejected)
	}
}

func TestReportRanksOnlyRealDeliveries(t *testing.T) {
	report := Report([]Result{
		{Task: "t", Arm: "real", Score: 40},
		{Task: "t", Arm: "sim", Score: 90, Simulated: true},
	})
	ranking := report[strings.Index(report, "## Média por braço"):strings.Index(report, "\n## t")]
	if !strings.Contains(ranking, "| real | 40.0 | 1 |") || strings.Contains(ranking, "| sim |") {
		t.Errorf("ranking:\n%s", ranking)
	}
}
