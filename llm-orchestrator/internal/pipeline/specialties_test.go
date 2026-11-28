package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

func TestSpecialistContract(t *testing.T) {
	ok := parseSpecialist(fence(`{"scope":"login","requirements":[{"id":"TM-01","text":"bloquear após 5 tentativas","verification":"teste IT-AUTH-03"}]}`))
	if len(ok.Problems) != 0 || len(ok.Requirements) != 1 {
		t.Errorf("valid report = %+v", ok)
	}
	none := parseSpecialist(fence(`{"scope":"tela de perfil","requirements":[],"none_reason":"só leitura de dados públicos"}`))
	if len(none.Problems) != 0 {
		t.Errorf("a reasoned empty report is valid: %+v", none)
	}
	for name, out := range map[string]string{
		"no json":         "parece seguro",
		"no verification": fence(`{"scope":"x","requirements":[{"text":"validar entrada"}]}`),
		"nothing, no why": fence(`{"scope":"x","requirements":[]}`),
	} {
		if r := parseSpecialist(out); len(r.Problems) == 0 {
			t.Errorf("%s accepted: %+v", name, r)
		}
	}
}

var specialtyStages = []Stage{
	{ID: "threat-model", Title: "Ameaças", Role: "tm", Selectable: true, Conditional: true, Specialist: true, ReadOnly: true},
	{ID: "arch", Title: "Arq", Role: "architect", Planner: true},
	{ID: "data", Title: "Dados", Role: "dba", Selectable: true, Conditional: true, Specialist: true, ReadOnly: true},
	{ID: "backend", Title: "Back", Role: "be", Selectable: true, Implementer: true, Inputs: []string{"threat-model", "arch", "data"}},
	{ID: "checks", Title: "Checks", Role: "gate", Verifier: true},
	{ID: "migrations", Title: "Migrations", Role: "gate", Verifier: true, Selectable: true, Conditional: true, Commands: "migrations"},
	{ID: "performance", Title: "Desempenho", Role: "perf", Selectable: true, Conditional: true, Gate: true, Measured: true, ReadOnly: true},
	{ID: "acceptance", Title: "QA", Role: "qa", Gate: true, Acceptor: true, ReadOnly: true},
}

const specialistOK = "ameaças...\n```json\n{\"scope\":\"login\",\"requirements\":[{\"id\":\"TM-01\",\"text\":\"recusar token expirado\",\"verification\":\"teste negativo\"}]}\n```"

// newSpecialtyTest runs specialtyStages; checks decides each command's result.
func newSpecialtyTest(t *testing.T, exec *fakeExecutor, mutate func(*workspace.Policy), check func(c quality.Command) quality.Result) *Orchestrator {
	t.Helper()
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	o.baseStages = specialtyStages
	o.stages = o.baseStages
	policy := o.ws.Policy()
	mutate(&policy)
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if check != nil {
		o.runCheck = func(_ context.Context, _ string, c quality.Command, _ time.Duration) quality.Result {
			r := check(c)
			r.Name, r.Command = c.Name, c.Run
			return r
		}
	}
	edits := 0
	exec.onCall = func(req provider.Request) {
		if req.Role == "be" {
			edits++
			writeFile(t, repo, "src/edit.txt", strings.Repeat("x", edits))
		}
	}
	return o
}

func TestSensitiveStoryGetsAThreatModelBeforeThePlan(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"tm": {specialistOK}}}
	o := newSpecialtyTest(t, exec, func(*workspace.Policy) {}, nil)
	_ = o.Start(realStory("Login com senha e token"))
	run := waitDone(t, o)
	st := run.Stories[0]
	if exec.count("tm") != 1 {
		t.Fatalf("threat model calls = %d, logs %+v", exec.count("tm"), run.Logs)
	}
	if indexOfRole(exec, "tm", 1) > indexOfRole(exec, "architect", 1) {
		t.Errorf("the threat model must come before the plan: %v", exec.roles)
	}
	if r := st.Specialties["threat-model"]; len(r.Requirements) != 1 {
		t.Errorf("report = %+v", r)
	}
	for _, role := range []string{"be", "qa"} {
		if !strings.Contains(lastPromptOf(exec, role), "recusar token expirado") {
			t.Errorf("%s did not get the threat model's requirement", role)
		}
	}
	if s := step(st.Steps, "threat-model"); s.Validation != ValidationPassed {
		t.Errorf("threat model step = %s %s", s.Status, s.Validation)
	}
}

func TestSpecialistWithoutContractIsInconclusive(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"tm": {"tudo certo"}}}
	o := newSpecialtyTest(t, exec, func(*workspace.Policy) {}, nil)
	_ = o.Start(realStory("Login com senha"))
	run := waitDone(t, o)
	st := run.Stories[0]
	if s := step(st.Steps, "threat-model"); s.Status != StepWarning || s.Validation != ValidationInconclusive {
		t.Errorf("step = %s %s", s.Status, s.Validation)
	}
	if st.Validation != ValidationInconclusive {
		t.Errorf("validation = %s", st.Validation)
	}
}

const migrationPlan = `{"agents":["backend"],"impact":{"migrations":["nova coluna pedidos.frete"],"rollback":"drop column"}}`

func TestPlannedMigrationIsPlannedAndRunOnTheDisposableDatabase(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"architect": {fence(migrationPlan)},
		"dba":       {fence(`{"scope":"pedidos","requirements":[{"text":"coluna nula até o backfill","verification":"migrate up/down"}]}`)},
	}}
	migrated := 0
	o := newSpecialtyTest(t, exec, func(p *workspace.Policy) {
		p.MigrationChecks = []quality.Command{{Name: "migrate", Run: "make db-up db-down db-up"}}
	}, func(c quality.Command) quality.Result {
		if c.Name != "migrate" {
			return quality.Result{Passed: true}
		}
		migrated++
		if migrated == 1 {
			return quality.Result{ExitCode: 1, Output: "ERROR: column frete contains null values"}
		}
		return quality.Result{Passed: true}
	})
	_ = o.Start(realStory("Mostrar frete no pedido"))
	run := waitDone(t, o)
	st := run.Stories[0]
	if exec.count("dba") != 1 {
		t.Errorf("data specialist calls = %d", exec.count("dba"))
	}
	m := step(st.Steps, "migrations")
	if m.Validation != ValidationPassed || len(m.Issues) == 0 || m.Issues[0].Agent != "backend" {
		t.Fatalf("migration step = %s %s %+v, logs %+v", m.Status, m.Validation, m.Issues, run.Logs)
	}
	if migrated != 2 {
		t.Errorf("migration commands ran %d time(s), want a failure and the re-run after the fix", migrated)
	}
}

func TestMigrationWithoutCommandsIsInconclusive(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"architect": {fence(migrationPlan)},
		"dba":       {fence(`{"scope":"pedidos","requirements":[],"none_reason":"coluna nova sem dados"}`)},
	}}
	o := newSpecialtyTest(t, exec, func(*workspace.Policy) {}, nil)
	_ = o.Start(realStory("Mostrar frete no pedido"))
	run := waitDone(t, o)
	m := step(run.Stories[0].Steps, "migrations")
	if m.Validation != ValidationInconclusive || !strings.Contains(m.Note, "migration_checks") {
		t.Fatalf("migration step = %s %s %q", m.Status, m.Validation, m.Note)
	}
}

const perfPlan = `{"agents":["backend"],"non_functional":[{"kind":"desempenho","requirement":"listar pedidos","slo":"p95 < 200 ms"}]}`

func TestPerformanceIsJudgedFromTheMeasurement(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"architect": {fence(perfPlan)},
		"perf": {
			fence(`{"status":"approved","issues":[],"measurements":[{"metric":"p95 /pedidos","value":"340 ms","slo":"200 ms","met":false}]}`),
			fence(`{"status":"approved","issues":[],"measurements":[{"metric":"p95 /pedidos","value":"150 ms","slo":"200 ms","met":true}]}`),
		},
	}}
	o := newSpecialtyTest(t, exec, func(p *workspace.Policy) {
		p.BenchmarkChecks = []quality.Command{{Name: "bench", Run: "k6 run bench.js"}}
	}, func(c quality.Command) quality.Result {
		return quality.Result{Passed: true, Output: "http_req_duration p(95)=340ms"}
	})
	_ = o.Start(realStory("Listar pedidos"))
	run := waitDone(t, o)
	st := run.Stories[0]
	p := step(st.Steps, "performance")
	if len(p.Issues) != 1 || !strings.Contains(p.Issues[0].Description, "não atingida") {
		t.Fatalf("a missed target approved: %+v", p.Issues)
	}
	if !strings.Contains(exec.prompts[indexOfRole(exec, "perf", 1)], "p(95)=340ms") {
		t.Error("the specialist did not get the benchmark output")
	}
	if st.Validation != ValidationInconclusive && st.Validation != ValidationPassed {
		t.Errorf("validation = %s", st.Validation)
	}
	if len(st.Measurements) != 1 || !st.Measurements[0].Met {
		t.Errorf("measurements = %+v", st.Measurements)
	}
}

func TestPerformanceWithoutBenchmarkIsInconclusive(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"architect": {fence(perfPlan)}}}
	o := newSpecialtyTest(t, exec, func(*workspace.Policy) {}, nil)
	_ = o.Start(realStory("Listar pedidos"))
	run := waitDone(t, o)
	if p := step(run.Stories[0].Steps, "performance"); p.Validation != ValidationInconclusive {
		t.Fatalf("performance step = %s %s", p.Status, p.Validation)
	}
	if !strings.Contains(exec.prompts[indexOfRole(exec, "perf", 1)], "benchmark_checks") {
		t.Error("the specialist must be told there is no measurement")
	}
}

// authRisk makes any change under auth/ high risk, with a threat model.
func authRisk(p *workspace.Policy) {
	p.RiskRules = []workspace.RiskRule{{Name: "Auth", Paths: []string{"**/auth/**"}, Level: workspace.RiskHigh, Require: []string{"threat-model"}}}
}

// touchAuth makes the back-end implementer change a file under auth/.
func touchAuth(t *testing.T, exec *fakeExecutor) {
	be := exec.onCall
	exec.onCall = func(req provider.Request) {
		be(req)
		if req.Role == "be" {
			writeFile(t, req.WorkDir, "src/auth/session.txt", "x")
		}
	}
}

func TestSpecialtyRequiredOnlyByTheDiffRunsOnTheBuiltCode(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"tm": {specialistOK}}}
	o := newSpecialtyTest(t, exec, authRisk, nil)
	touchAuth(t, exec)
	_ = o.Start(realStory("Mostrar o perfil"))
	run := waitDone(t, o)
	st := run.Stories[0]
	if st.Risk == nil || st.Risk.Level != workspace.RiskHigh {
		t.Fatalf("the diff must raise the risk: %+v", st.Risk)
	}
	if exec.count("tm") != 1 {
		t.Fatalf("threat model calls = %d: a mandatory specialty must not be skipped, logs %+v", exec.count("tm"), run.Logs)
	}
	if indexOfRole(exec, "tm", 1) < indexOfRole(exec, "be", 1) {
		t.Errorf("the late threat model must look at the built code: %v", exec.roles)
	}
	prompt := exec.promptsFor("tm")[0]
	if !strings.Contains(prompt, "Implementação já feita") || !strings.Contains(prompt, "src/auth/session.txt") {
		t.Errorf("the late specialty must be told what was built:\n%s", prompt)
	}
	if r := st.Specialties["threat-model"]; len(r.Requirements) != 1 {
		t.Errorf("report = %+v", r)
	}
	if s := step(st.Steps, "threat-model"); s.Status != StepDone || s.Validation != ValidationPassed {
		t.Errorf("threat-model step = %s %s %q", s.Status, s.Validation, s.Note)
	}
	if !hasLog(run, "roda agora sobre o código já feito") || hasLog(run, "segue sem ela") {
		t.Errorf("logs = %+v", run.Logs)
	}
	if st.Validation != ValidationPassed {
		t.Errorf("validation = %s, logs %+v", st.Validation, run.Logs)
	}
}

func TestLateSpecialtyWithoutAResultBlocksApproval(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"tm": {"tudo certo"}}}
	o := newSpecialtyTest(t, exec, authRisk, nil)
	touchAuth(t, exec)
	_ = o.Start(realStory("Mostrar o perfil"))
	run := waitDone(t, o)
	if v := run.Stories[0].Validation; v == ValidationPassed {
		t.Errorf("a mandatory threat model without a valid report cannot pass: %s", v)
	}
}

// The architect chose the data specialty; the diff then requires it too. It
// ran: nothing is late and nothing is skipped (the false warning of a run
// where "Dados e migrations" had passed).
func TestSpecialtyThatAlreadyRanIsNotReportedLate(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"architect": {fence(`{"agents": ["data", "backend"]}`)},
		"dba":       {specialistOK},
	}}
	o := newSpecialtyTest(t, exec, func(p *workspace.Policy) {
		p.RiskRules = []workspace.RiskRule{{Name: "Migrations", Paths: []string{"**/migrations/**"}, Level: workspace.RiskHigh, Require: []string{"data"}}}
	}, nil)
	be := exec.onCall
	exec.onCall = func(req provider.Request) {
		be(req)
		if req.Role == "be" {
			writeFile(t, req.WorkDir, "src/migrations/001.sql", "x")
		}
	}
	_ = o.Start(realStory("Reclassificar ordens"))
	run := waitDone(t, o)
	if exec.count("dba") != 1 {
		t.Errorf("data specialty calls = %d", exec.count("dba"))
	}
	if hasLog(run, "passou a ser exigida pelo diff") {
		t.Errorf("a stage that ran is not late: %+v", run.Logs)
	}
	if st := run.Stories[0]; st.Risk == nil || st.Risk.Level != workspace.RiskHigh || st.Validation != ValidationPassed {
		t.Errorf("risk %+v, validation %s", st.Risk, st.Validation)
	}
}

func TestTestKindAndTheCommandsThatRunIt(t *testing.T) {
	for _, tc := range []struct {
		req  quality.Requirement
		want string
	}{
		{quality.Requirement{ID: "UT-US101-01"}, "unit"},
		{quality.Requirement{ID: "it-us101-02"}, "integration"},
		{quality.Requirement{ID: "E2E-US101-03"}, "e2e"},
		{quality.Requirement{ID: "UT-1", Kind: "e2e"}, "e2e"},
		{quality.Requirement{ID: "AC-1"}, ""},
	} {
		if got := testKind(tc.req); got != tc.want {
			t.Errorf("testKind(%+v) = %q, want %q", tc.req, got, tc.want)
		}
	}
	lintE2E := []quality.Command{{Name: "lint", Run: "npm run lint"}, {Name: "test:e2e", Run: "npm run test:e2e"}}
	if commandRunsKind(lintE2E, "unit") || !commandRunsKind(lintE2E, "integration") {
		t.Error("lint and e2e run no unit test; integration tests may live in the e2e suite")
	}
	if !commandRunsKind([]quality.Command{{Name: "unit", Run: "npm test"}}, "unit") {
		t.Error("npm test runs unit tests")
	}
	if commandRunsKind([]quality.Command{{Name: "lint", Run: "npm run lint"}}, "integration") {
		t.Error("lint runs no integration test")
	}
}

// The mag run only learned at the end that no command ran its UT and IT
// cases; the spec's own list says it before any code is written.
func TestSpecWarnsAboutTestsNoCommandRuns(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"spec": {fence(`{"tests": [{"id": "UT-US101-01", "kind": "unit"}, {"id": "UT-US101-02", "kind": "unit"}, {"id": "IT-US101-01", "kind": "integration"}]}`)},
	}}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	policy := o.ws.Policy()
	policy.Checks = []quality.Command{{Name: "lint", Run: "npm run lint"}, {Name: "test:e2e", Run: "npm run test:e2e"}}
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	_ = o.Start(realStory("Relatórios"))
	run := waitDone(t, o)
	if !hasLog(run, "Nenhum comando do projeto roda testes unitários: 2 caso(s) da especificação (UT-US101-01, UT-US101-02)") {
		t.Errorf("no warning for the unit tests: %+v", run.Logs)
	}
	if hasLog(run, "testes de integração") {
		t.Error("the e2e suite may run integration tests: no warning for them")
	}
}

func TestPlannedMigrationsWithoutChecksAreWarnedEarlyOnce(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"architect": {fence(`{"agents": ["backend", "migrations"]}`)}}}
	o := newSpecialtyTest(t, exec, func(p *workspace.Policy) {
		p.MigrationChecks = nil
		p.RiskRules = []workspace.RiskRule{{Name: "Migrations", Paths: []string{"**/migrations/**"}, Level: workspace.RiskHigh, Require: []string{"migrations"}}}
	}, nil)
	be := exec.onCall
	exec.onCall = func(req provider.Request) {
		be(req)
		if req.Role == "be" {
			writeFile(t, req.WorkDir, "src/migrations/001.sql", "x")
		}
	}
	_ = o.Start(realStory("Reclassificar ordens"))
	run := waitDone(t, o)
	warned, beAt := -1, -1
	n := 0
	for i, l := range run.Logs {
		if strings.Contains(l.Message, "vai alterar migrations, mas a política não tem `migration_checks`") {
			n++
			if warned < 0 {
				warned = i
			}
		}
		if beAt < 0 && strings.Contains(l.Message, "Back concluída") {
			beAt = i
		}
	}
	if n != 1 || warned < 0 || warned > beAt {
		t.Errorf("warning count %d at %d, back-end done at %d: %+v", n, warned, beAt, run.Logs)
	}
}
