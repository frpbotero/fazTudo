package pipeline

import (
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

// runRiskFlow runs flowStages on one story under a policy changed by mutate.
func runRiskFlow(t *testing.T, exec *fakeExecutor, story string, mutate func(*workspace.Policy)) Run {
	t.Helper()
	repo := t.TempDir()
	ws, err := workspace.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	policy := ws.Policy()
	policy.Git = false
	// These tests are about the gates the risk turns on; flowStages has no
	// specialty, which the specialties' own tests cover.
	policy.RiskRules = gatesOnly(workspace.DefaultRiskRules)
	mutate(&policy)
	if _, err := ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if exec.onCall == nil {
		exec.onCall = func(provider.Request) {}
	}
	o := New(exec, ws, flowStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := o.Start(StartOptions{Stories: []stories.Story{{ID: "US-1", Title: "US-1", Content: story}}}); err != nil {
		t.Fatal(err)
	}
	return waitDone(t, o)
}

func TestFastFlowStillRunsTheStagesTheRiskRequires(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"architect": {fence(frontOnly)}}}
	run := runRiskFlow(t, exec, "Tela de login com senha", func(p *workspace.Policy) { p.Flow = workspace.FlowFast })

	st := run.Stories[0]
	if st.Risk == nil || st.Risk.Level != workspace.RiskHigh {
		t.Fatalf("risk = %+v", st.Risk)
	}
	for _, id := range []string{"security", "review"} {
		s := step(st.Steps, id)
		if s.Status != StepDone || s.Validation != ValidationPassed {
			t.Errorf("%s must run despite the fast flow: %s %s", id, s.Status, s.Validation)
		}
	}
	if exec.count("sec") != 1 || exec.count("rev") != 1 {
		t.Errorf("calls sec=%d rev=%d", exec.count("sec"), exec.count("rev"))
	}
	if st.Validation != ValidationPassed {
		t.Errorf("validation = %s", st.Validation)
	}
}

func TestArchitectCannotDropAMandatoryStage(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"architect": {fence(frontOnly)}}}
	run := runRiskFlow(t, exec, "Cobrança por pix", func(*workspace.Policy) {})

	st := run.Stories[0]
	if !st.Plan.Includes("security") || exec.count("sec") != 1 {
		t.Errorf("security is mandatory for payments: plan %v, calls %d", st.Plan.Agents, exec.count("sec"))
	}
}

func TestTheRealDiffRaisesTheRisk(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"architect": {fence(frontOnly)}}}
	exec.onCall = func(req provider.Request) {
		if req.Role == "fe" {
			writeFile(t, req.WorkDir, "db/migrations/001_pedidos.sql", "create table pedidos();")
		}
	}
	run := runRiskFlow(t, exec, "Listar pedidos", func(p *workspace.Policy) { p.Flow = workspace.FlowFast })

	st := run.Stories[0]
	if st.Risk == nil || !strings.Contains(strings.Join(st.Risk.Reasons, " "), "001_pedidos.sql") {
		t.Fatalf("risk = %+v", st.Risk)
	}
	if exec.count("rev") != 1 {
		t.Error("a migration in the diff makes the review mandatory, even in the fast flow")
	}
	if exec.count("sec") != 0 {
		t.Error("persistence rules do not require security")
	}
}

func TestMandatoryStageWithoutResultBlocksApproval(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"architect": {fence(frontOnly)}}}
	run := runRiskFlow(t, exec, "Exportar relatório", func(p *workspace.Policy) {
		p.RiskRules = []workspace.RiskRule{{Name: "Relatórios", Keywords: []string{"relatório"}, Level: workspace.RiskHigh, Require: []string{"e2e"}}}
	})

	st := run.Stories[0]
	if st.Validation != ValidationInconclusive || st.Status != StepWarning {
		t.Errorf("e2e is required but this flow has no such stage: %s %s", st.Validation, st.Status)
	}
}

func TestStoriesWithoutRiskRunTheFlowAsConfigured(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"architect": {fence(frontOnly)}}}
	run := runRiskFlow(t, exec, "Mudar a cor do botão", func(p *workspace.Policy) { p.Flow = workspace.FlowFast })
	if exec.count("sec") != 0 || exec.count("rev") != 0 {
		t.Error("the fast flow keeps security and review off for a low-risk story")
	}
	if r := run.Stories[0].Risk; r == nil || r.Level != workspace.RiskLow {
		t.Errorf("risk = %+v", r)
	}
	for _, l := range run.Logs {
		if strings.Contains(l.Message, "Risco ") {
			t.Errorf("a story no rule matched logs its risk: %q", l.Message)
		}
	}
}

func gatesOnly(rules []workspace.RiskRule) []workspace.RiskRule {
	out := make([]workspace.RiskRule, 0, len(rules))
	for _, r := range rules {
		r.Require = slices.DeleteFunc(slices.Clone(r.Require), func(id string) bool {
			return slices.Contains([]string{"threat-model", "data", "migrations", "performance"}, id)
		})
		if len(r.Require) > 0 {
			out = append(out, r)
		}
	}
	return out
}
