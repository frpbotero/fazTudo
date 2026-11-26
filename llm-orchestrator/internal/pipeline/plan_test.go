package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

var flowStages = []Stage{
	{ID: "arch", Title: "Arch", Role: "architect", Planner: true},
	{ID: "frontend", Title: "Front", Role: "fe", Selectable: true, Implementer: true},
	{ID: "backend", Title: "Back", Role: "be", Selectable: true, Implementer: true},
	{ID: "security", Title: "Sec", Role: "sec", Selectable: true, Gate: true},
	{ID: "review", Title: "Review", Role: "rev", Gate: true},
}

func fence(js string) string { return "relatório...\n\n```json\n" + js + "\n```\n" }

const approved = `{"status": "approved", "issues": []}`

func runFlow(t *testing.T, exec *fakeExecutor) Run {
	t.Helper()
	repo := t.TempDir()
	ws, err := workspace.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	if exec.onCall == nil {
		// A fix that changes nothing ends the loop: these agents do their work.
		var edits atomic.Int32
		exec.onCall = func(provider.Request) {
			writeFile(t, repo, fmt.Sprintf("src/edit-%d.txt", edits.Add(1)), "x")
		}
	}
	o := New(exec, ws, flowStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := o.Start(StartOptions{Stories: []stories.Story{{ID: "US-1", Title: "US-1", Content: "nova tela de perfil"}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3e9)
	defer cancel()
	if err := o.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	return o.Snapshot()
}

func stepIDs(steps []Step) string {
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID+":"+string(s.Status))
	}
	return strings.Join(ids, " ")
}

func TestParsePlan(t *testing.T) {
	out := "análise\n```json\n{\"agents\": [\"Frontend\", \"e2e\", \"gemini\", \"review\"], \"reason\": \"só tela\"}\n```"
	plan, ok := parsePlan(out, DefaultStages)
	if !ok || strings.Join(plan.Agents, ",") != "frontend,e2e" || plan.Reason != "só tela" {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}

	plan, _ = parsePlan(fence(`{"agents": ["security"]}`), DefaultStages)
	if !plan.Includes("frontend") || !plan.Includes("security") {
		t.Errorf("a plan without implementers must get one: %+v", plan)
	}

	if _, ok := parsePlan("sem json aqui", DefaultStages); ok {
		t.Error("missing JSON must not parse")
	}
}

func TestParseVerdictReassignsUnknownAgents(t *testing.T) {
	plan := Plan{Agents: []string{"frontend"}}
	v, ok := parseVerdict(fence(`{"status": "changes_requested", "issues": [
		{"agent": "backend", "description": "validar payload"},
		{"agent": "dba", "description": "índice faltando"},
		{"agent": "frontend", "description": ""}]}`), DefaultStages, plan)
	if !ok || !v.NeedsChanges() || len(v.Issues) != 2 {
		t.Fatalf("verdict = %+v", v)
	}
	if v.Issues[1].Agent != "frontend" {
		t.Errorf("unknown agent must go to a planned implementer, got %q", v.Issues[1].Agent)
	}
}

func TestReadVerdictRejectsInvalidOrContradictoryOutput(t *testing.T) {
	plan := Plan{Agents: []string{"backend"}}
	cases := []struct {
		name, output string
		valid        bool
	}{
		{"empty", "", false},
		{"blank", "  \n ", false},
		{"no json", "Revisão: tudo certo.", false},
		{"truncated", "```json\n{\"status\": \"approved\", \"issues\": [\n```", false},
		{"unknown status", fence(`{"status": "ok", "issues": []}`), false},
		{"changes without issues", fence(`{"status": "changes_requested", "issues": []}`), false},
		{"changes with blank issues", fence(`{"status": "changes_requested", "issues": [{"description": " "}]}`), false},
		{"approved with blocking", fence(`{"status": "approved", "issues": [{"severity": "alta", "description": "SQL injection"}]}`), false},
		{"approved with high", fence(`{"status": "approved", "issues": [{"severity": "High", "description": "x"}]}`), false},
		{"approved", fence(`{"status": "Approved", "issues": []}`), true},
		{"approved with minor note", fence(`{"status": "approved", "issues": [{"severity": "baixa", "description": "nome"}]}`), true},
		{"changes requested", fence(`{"status": "changes_requested", "issues": [{"severity": "média", "description": "validar"}]}`), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, problem := readVerdict(c.output, DefaultStages, plan)
			if (problem == "") != c.valid {
				t.Fatalf("problem = %q, verdict = %+v", problem, v)
			}
			if _, ok := parseVerdict(c.output, DefaultStages, plan); ok != c.valid {
				t.Errorf("parseVerdict ok = %v", ok)
			}
			if !c.valid && (v.Status != "" || v.NeedsChanges()) {
				t.Errorf("an invalid verdict must carry no decision: %+v", v)
			}
		})
	}
}

func TestHeuristicPlan(t *testing.T) {
	p := heuristicPlan("Ajustar o layout da tela de login", DefaultStages)
	if strings.Join(p.Agents, ",") != "frontend,e2e" {
		t.Errorf("front-only story = %v", p.Agents)
	}
	p = heuristicPlan("Novo endpoint de pagamento com token", DefaultStages)
	if strings.Join(p.Agents, ",") != "backend,security" {
		t.Errorf("backend story = %v", p.Agents)
	}
}

func TestArchitectSkipsUnneededAgents(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"architect": {fence(`{"agents": ["frontend"], "reason": "só UI"}`)},
		"rev":       {fence(approved)},
	}}
	run := runFlow(t, exec)

	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}
	if got := stepIDs(run.Stories[0].Steps); got != "arch:done frontend:done backend:omitted security:omitted review:done" {
		t.Errorf("steps = %s", got)
	}
	if exec.count("be") != 0 || exec.count("sec") != 0 {
		t.Error("omitted agents must not be called")
	}
	if p := run.Stories[0].Plan; p == nil || p.Source != "architect" || p.Reason != "só UI" {
		t.Errorf("plan not recorded: %+v", p)
	}
}

func TestGateTriggersFixesAndVerification(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"architect": {fence(`{"agents": ["frontend", "backend"]}`)},
		"rev": {
			fence(`{"status": "changes_requested", "issues": [
				{"agent": "backend", "severity": "alta", "description": "SQL injection em /users", "location": "api/users.go:42"},
				{"agent": "frontend", "description": "botão sem label acessível"}]}`),
			fence(approved),
		},
	}}
	run := runFlow(t, exec)

	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s (%s)", run.Result, stepIDs(run.Stories[0].Steps))
	}
	want := "arch:done frontend:done backend:done security:omitted review:done " +
		"fix-review-1-frontend:done fix-review-1-backend:done review-verify-2:done"
	if got := stepIDs(run.Stories[0].Steps); got != want {
		t.Errorf("steps =\n%s\nwant\n%s", got, want)
	}

	fixBackend := exec.prompts[indexOfRole(exec, "be", 2)]
	for _, wantText := range []string{"SQL injection em /users", "api/users.go:42", "[alta]", "Correções obrigatórias"} {
		if !strings.Contains(fixBackend, wantText) {
			t.Errorf("backend fix prompt missing %q", wantText)
		}
	}
	assigned := fixBackend[strings.Index(fixBackend, "## Correções obrigatórias"):]
	if strings.Contains(assigned, "botão sem label") || strings.Contains(fixBackend, "changes_requested") {
		t.Error("backend must only be assigned its own issues, without the raw verdict JSON")
	}
	verify := exec.prompts[len(exec.prompts)-1]
	if !strings.Contains(verify, "Verificação, rodada 2") || !strings.Contains(verify, "artifact-fe") {
		t.Errorf("verification prompt must list applied fixes:\n%s", verify)
	}
	if review := run.Stories[0].Steps[4]; len(review.Issues) != 2 || review.Note == "" {
		t.Errorf("gate step must record its issues: %+v", review)
	}
}

func TestGateKeepsGoingAfterMaxRounds(t *testing.T) {
	bad := fence(`{"status": "changes_requested", "issues": [{"agent": "backend", "description": "teste quebrado"}]}`)
	exec := &fakeExecutor{outputs: map[string][]string{
		"architect": {fence(`{"agents": ["backend"]}`)},
		"rev":       {bad, bad, bad},
	}}
	run := runFlow(t, exec)

	if run.Result != ResultSucceeded || run.Stories[0].Status != StepWarning {
		t.Fatalf("result = %s, story = %s; want succeeded with pending issues", run.Result, run.Stories[0].Status)
	}
	if n := exec.count("be"); n != 1+maxFixRounds {
		t.Errorf("backend calls = %d, want %d (implementation + %d fixes)", n, 1+maxFixRounds, maxFixRounds)
	}
	last := run.Stories[0].Steps[len(run.Stories[0].Steps)-1]
	if last.Status != StepWarning || !strings.Contains(last.Error, "persistem") || len(last.Issues) == 0 {
		t.Errorf("last verification should keep the pending issues: %+v", last)
	}
}

func TestMissingPlanRunsEveryAgent(t *testing.T) {
	exec := &fakeExecutor{}
	run := runFlow(t, exec)
	if run.Result != ResultSucceeded || run.Stories[0].Plan.Source != "fallback" {
		t.Fatalf("result=%s plan=%+v", run.Result, run.Stories[0].Plan)
	}
	if exec.count("fe") != 1 || exec.count("be") != 1 || exec.count("sec") != 1 {
		t.Error("without a plan all agents must run")
	}
}

func indexOfRole(f *fakeExecutor, role string, nth int) int {
	n := 0
	for i, r := range f.roles {
		if r == role {
			if n++; n == nth {
				return i
			}
		}
	}
	return -1
}

func TestPlanRecordsImpactAndItsGaps(t *testing.T) {
	out := fence(`{"agents": ["frontend", "backend"],
		"streams": [
			{"id": "api", "agent": "backend", "files": ["api/"]},
			{"id": "tela", "agent": "frontend", "files": ["web/"]}],
		"impact": {"contracts": ["POST /pedidos"], "consumers": ["app mobile"], "migrations": ["002_pedidos.sql"]},
		"non_functional": [{"kind": "Desempenho", "requirement": "listar pedidos rápido"}]}`)
	plan, ok := parsePlan(out, DefaultStages)
	if !ok || plan.Impact == nil || plan.Impact.Consumers[0] != "app mobile" {
		t.Fatalf("plan = %+v", plan)
	}
	want := []string{"cenário de aceitação", "reversão", "sem SLO"}
	if len(plan.Gaps) != len(want) {
		t.Fatalf("gaps = %q", plan.Gaps)
	}
	for i, w := range want {
		if !strings.Contains(plan.Gaps[i], w) {
			t.Errorf("gap %d = %q, want %q", i, plan.Gaps[i], w)
		}
	}
	// Disjoint files, but one schema: the fronts do not run side by side.
	if waves := plan.parallelWaves(); waves != nil {
		t.Errorf("a migration serializes the fronts: %+v", waves)
	}
}

func TestFrontsSharingAResourceRunInSeparateWaves(t *testing.T) {
	streams := []Stream{
		{ID: "a", Files: []string{"a/"}, Resources: []string{"contrato post /cart"}},
		{ID: "b", Files: []string{"b/"}, Resources: []string{"contrato post /cart"}},
		{ID: "c", Files: []string{"c/"}},
	}
	waves := streamWaves(streams)
	if len(waves) != 2 || len(waves[0]) != 2 || waves[0][0].ID != "a" || waves[0][1].ID != "c" || waves[1][0].ID != "b" {
		t.Errorf("waves = %+v", waves)
	}
}

func TestCompletePlanHasNoGaps(t *testing.T) {
	plan, _ := parsePlan(fence(`{"agents": ["backend"],
		"impact": {"migrations": ["003.sql"], "rollback": "migration down 003"},
		"acceptance": [{"criterion": "c1", "scenario": "dado X quando Y então Z", "method": "integração"}],
		"non_functional": [{"kind": "desempenho", "requirement": "listar", "slo": "p95 < 200 ms"}]}`), DefaultStages)
	if len(plan.Gaps) != 0 || len(plan.Acceptance) != 1 {
		t.Errorf("plan = %+v", plan)
	}
}
