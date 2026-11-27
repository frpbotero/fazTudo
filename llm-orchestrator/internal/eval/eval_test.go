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
		// Each arm gets its own copy: one agent cannot change another's.
		if fi, err := os.Lstat(filepath.Join(r.Workdir, "node_modules")); err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			t.Errorf("%s: node_modules must be copied, not linked", r.Arm)
		}
		if r.Ranked {
			t.Errorf("%s: a simulation is never ranked", r.Arm)
		}
	}
	if router.Config().DefaultProvider != provider.Codex {
		t.Error("each arm must select its provider")
	}
	report, err := os.ReadFile(filepath.Join(filepath.Dir(results[0].Workdir), "report.md"))
	if err != nil || !strings.Contains(string(report), "Nenhum resultado com evidência mínima") ||
		!strings.Contains(string(report), "| claude | 1 | 20.0 | succeeded (simulado) |") {
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

func TestReportRanksOnlyResultsWithEvidence(t *testing.T) {
	report := Report([]Result{
		{Task: "t", Arm: "real", Score: 40, Ranked: true, Accepted: true, WronglyAccepted: true, Tokens: 1000, UnknownCost: 2},
		{Task: "t", Arm: "real", Score: 60, Ranked: true},
		{Task: "t", Arm: "real", Score: 50, Ranked: true},
		{Task: "t", Arm: "sim", Score: 90, Simulated: true},
		{Task: "t", Arm: "cego", Score: 70, Verified: false},
	})
	ranking := report[strings.Index(report, "## Por braço"):strings.Index(report, "\n## t")]
	if !strings.Contains(ranking, "| real | 3 | 50.0 | 40.0 | 60.0 | 10.0 | 1 | 1 |") || strings.Contains(ranking, "| sim |") {
		t.Errorf("ranking:\n%s", ranking)
	}
	if !strings.Contains(ranking, "sim em t: simulado") || !strings.Contains(ranking, "cego em t: nenhuma verificação executada") {
		t.Errorf("unranked results must say why:\n%s", ranking)
	}
	if !strings.Contains(report, "**sim, indevidamente**") {
		t.Error("a wrongly accepted change must stand out")
	}
	few := Report([]Result{{Task: "t", Arm: "a", Score: 50, Ranked: true}})
	if !strings.Contains(few, "1 (amostra insuficiente)") {
		t.Errorf("one run must not pass for a measured spread:\n%s", few)
	}
}

func TestSafetyCasesCheckThePromotionItself(t *testing.T) {
	r := Result{Accepted: true, Verified: true, HiddenPassed: 1, HiddenTotal: 1, SafetyViolation: true}
	finish(&r, Task{Expect: ExpectBlocked})
	if r.Score != 0 {
		t.Errorf("a violation scores %.1f", r.Score)
	}
	report := Report([]Result{{Task: "impossivel", Arm: "a", SafetyViolation: true}})
	if !strings.Contains(report, "**Violação**: a em impossivel") {
		t.Errorf("report:\n%s", report)
	}
	ok := Report([]Result{{Task: "x", Arm: "a"}})
	if !strings.Contains(ok, "Nenhuma tarefa marcada como bloqueada foi aprovada") {
		t.Error("the safety section must say when nothing was violated")
	}
	wrong := Result{Accepted: true, Verified: true, HiddenPassed: 0, HiddenTotal: 2}
	finish(&wrong, Task{})
	if !wrong.WronglyAccepted || !wrong.Ranked {
		t.Errorf("accepted with failing hidden checks = %+v", wrong)
	}
}

func TestHiddenFilesStayOutOfTheAgentsReach(t *testing.T) {
	dir := t.TempDir()
	tpl := filepath.Join(dir, "tpl")
	os.MkdirAll(filepath.Join(tpl, "reservado"), 0o755)
	os.WriteFile(filepath.Join(tpl, "US.md"), []byte("# US-1: x\n"), 0o644)
	write := func(body string) string {
		p := filepath.Join(dir, "s.json")
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	inside := write(`{"tasks":[{"name":"x","template":"tpl","story":"US.md","hidden_dir":"tpl/reservado"}],"arms":[{"name":"a","provider":"claude"}]}`)
	if _, err := Load(inside); err == nil || !strings.Contains(err.Error(), "dentro do template") {
		t.Errorf("hidden_dir inside the template = %v", err)
	}
	os.MkdirAll(filepath.Join(dir, "ocultos"), 0o755)
	os.WriteFile(filepath.Join(dir, "ocultos", "hidden_test.sh"), []byte("exit 0\n"), 0o644)
	outside := write(`{"tasks":[{"name":"x","template":"tpl","story":"US.md","hidden_dir":"ocultos","hidden_checks":[{"name":"h","run":"sh hidden_test.sh"}]}],"arms":[{"name":"a","provider":"claude"}]}`)
	s, err := Load(outside)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	var res Result
	if err := hiddenChecks(context.Background(), s.Tasks[0], work, &res); err != nil || res.HiddenPassed != 1 {
		t.Errorf("hidden checks = %+v, %v", res, err)
	}
	for _, bad := range []string{
		`{"tasks":[{"name":"x","template":"tpl","story":"US.md","expect":"talvez"}],"arms":[{"name":"a","provider":"claude"}]}`,
		`{"tasks":[{"name":"x","template":"tpl","story":"US.md"}],"arms":[{"name":"a","provider":"claude","mode":"turbo"}]}`,
		`{"tasks":[{"name":"x","template":"tpl","story":"US.md"}],"arms":[{"name":"a","provider":"claude","policy":{"candidates":"três"}}]}`,
	} {
		if _, err := Load(write(bad)); err == nil {
			t.Errorf("Load(%s) must fail", bad)
		}
	}
}

func TestArmsChangeFlowPolicyAndPrompts(t *testing.T) {
	p, err := armPolicy(Arm{Policy: json.RawMessage(`{"max_line_length": 120, "parallel_lenses": true}`)})
	if err != nil || p.MaxLineLength != 120 || !p.ParallelLenses || !p.AutoChecks {
		t.Errorf("policy = %+v, %v", p, err)
	}
}

func TestDirectArmRunsOneAgentWithoutThePipeline(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("CODEX_HOME", t.TempDir())
	dir := t.TempDir()
	tpl := filepath.Join(dir, "tpl")
	os.MkdirAll(tpl, 0o755)
	os.WriteFile(filepath.Join(tpl, "US.md"), []byte("# US-1: Loja\n"), 0o644)
	suite := Suite{DryRun: true, Output: filepath.Join(dir, "out"), Repeat: 2,
		Tasks: []Task{{Name: "loja", Template: tpl, Story: "US.md", Checks: []quality.Command{{Name: "ok", Run: "true"}},
			HiddenChecks: []quality.Command{{Name: "h", Run: "true"}}}},
		Arms: []Arm{{Name: "cli", Provider: provider.Claude, Mode: ModeDirect}}}
	router := provider.NewRouter(true, []provider.CLI{provider.ClaudeCLI}, provider.WithSimulationDelay(time.Millisecond))
	results, err := Run(context.Background(), suite, router, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || len(results) != 2 {
		t.Fatalf("results = %+v, %v", results, err)
	}
	for i, r := range results {
		if r.AgentCalls != 1 || r.Repetition != i+1 || !r.Verified || r.HiddenPassed != 1 || r.Accepted {
			t.Errorf("direct run %d = %+v (a simulation is never accepted)", i+1, r)
		}
		if _, err := os.Stat(filepath.Join(r.Workdir, ".faztudo-eval.log")); err == nil {
			t.Error("the direct arm must not run the pipeline")
		}
	}
}

func TestPilotSuiteFollowsItsProtocol(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "evals", "piloto", "piloto.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s Suite
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.Tasks) != 10 || s.Repeat < 3 {
		t.Errorf("the pilot has %d tasks, repeat %d", len(s.Tasks), s.Repeat)
	}
	blocked := 0
	for _, task := range s.Tasks {
		if task.Kind == "" || task.HiddenDir == "" || len(task.HiddenChecks) == 0 {
			t.Errorf("task %s lacks its kind or reserved checks", task.Name)
		}
		if task.Expect == ExpectBlocked {
			blocked++
		}
	}
	if blocked != 1 {
		t.Errorf("approval-safety tasks = %d", blocked)
	}
	var arms []string
	for _, a := range s.Arms {
		arms = append(arms, a.Name)
		if _, err := armPolicy(a); err != nil {
			t.Errorf("%s: %v", a.Name, err)
		}
	}
	if strings.Join(arms, ",") != "cli-direto,fluxo-minimo,fluxo-completo" || s.Arms[0].Mode != ModeDirect {
		t.Errorf("arms = %v", arms)
	}
}

func TestRereportJoinsTheReviewersTime(t *testing.T) {
	dir := t.TempDir()
	results := []Result{
		{Task: "bug-1", Arm: "fluxo-completo", Repetition: 1, Kind: "bug", Ranked: true, Accepted: true},
		{Task: "bug-1", Arm: "cli-direto", Repetition: 1, Kind: "bug", Ranked: true},
	}
	data, _ := json.Marshal(results)
	os.WriteFile(filepath.Join(dir, "results.json"), data, 0o644)
	os.WriteFile(filepath.Join(dir, "review.json"), []byte(`{"bug-1__fluxo-completo__1": {"minutes": 12, "rework_minutes": 5}}`), 0o644)
	report, err := Rereport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "## Piloto por tipo de tarefa") || !strings.Contains(report, "| bug | fluxo-completo | 1 | 1 | 0 |") ||
		!strings.Contains(report, "| 17 |") {
		t.Errorf("report:\n%s", report)
	}
	saved, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	if string(saved) != report {
		t.Error("report.md was not rewritten")
	}
}
