package pipeline

import (
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"llm-orchestrator/internal/workspace"
)

func runWithFlow(t *testing.T, policy workspace.Policy) (Run, *fakeExecutor) {
	t.Helper()
	repo := t.TempDir()
	ws, err := workspace.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	policy.Git = false
	if _, err := ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{}
	o := New(exec, ws, DefaultStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.attempts = 1
	if err := o.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	return waitDone(t, o), exec
}

func stepStatuses(run Run) map[string]StepStatus {
	out := map[string]StepStatus{}
	for _, s := range run.Stories[0].Steps {
		out[s.ID] = s.Status
	}
	return out
}

func TestFastFlowKeepsPasso0SoImplementedStoriesAreSkipped(t *testing.T) {
	policy := workspace.DefaultPolicy()
	policy.Flow = workspace.FlowFast
	run, exec := runWithFlow(t, policy)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	got := stepStatuses(run)
	for _, id := range []string{"security", "review", "e2e", "docs"} {
		if got[id] != StepOmitted {
			t.Errorf("%s = %s, want omitted", id, got[id])
		}
	}
	for _, id := range []string{"context", "spec", "arch", "frontend", "backend", "checks"} {
		if got[id] != StepDone {
			t.Errorf("%s = %s, want done", id, got[id])
		}
	}
	for _, role := range []string{"Staff Reviewer", "AppSec & QA Engineer", "E2E QA Lead", "Technical Writer"} {
		if exec.count(role) != 0 {
			t.Errorf("%s must not run", role)
		}
	}
	if slices.Contains(run.Stories[0].Plan.Agents, "security") {
		t.Error("the plan must not keep a disabled agent")
	}
}

func TestCustomFlowTurnsOffOnlyTheChosenStages(t *testing.T) {
	policy := workspace.DefaultPolicy()
	policy.Flow, policy.DisabledStages = workspace.FlowCustom, []string{"docs", "arch"}
	run, _ := runWithFlow(t, policy)
	got := stepStatuses(run)
	if got["docs"] != StepOmitted || got["arch"] != StepDone || got["review"] != StepDone {
		t.Fatalf("statuses = %v", got)
	}
}

func TestFullFlowIsTheDefault(t *testing.T) {
	run, _ := runWithFlow(t, workspace.DefaultPolicy())
	for id, status := range stepStatuses(run) {
		if status != StepDone {
			t.Errorf("%s = %s", id, status)
		}
	}
}

func TestAuditFlowReviewsExistingCodeWithoutDeveloping(t *testing.T) {
	policy := workspace.DefaultPolicy()
	policy.Flow = workspace.FlowAudit
	run, exec := runWithFlow(t, policy)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	got := stepStatuses(run)
	for _, id := range []string{"context", "spec", "arch", "frontend", "backend", "e2e", "docs"} {
		if got[id] != StepOmitted {
			t.Errorf("%s = %s, want omitted", id, got[id])
		}
	}
	for _, id := range []string{"checks", "security", "review"} {
		if got[id] != StepDone {
			t.Errorf("%s = %s, want done", id, got[id])
		}
	}
	for _, role := range []string{"Chief Architect", "Frontend Specialist", "Backend & ML Engineer"} {
		if exec.count(role) != 0 {
			t.Errorf("%s must not run in an audit", role)
		}
	}
	if !slices.Contains(run.Stories[0].Plan.Agents, "security") {
		t.Errorf("plan = %+v, security must run", run.Stories[0].Plan)
	}
	if !strings.Contains(exec.prompts[len(exec.prompts)-1], "Modo auditoria") {
		t.Error("reviewers must be told the code already exists")
	}
}

func TestAuditFlowKeepsFindingsAsPendingInsteadOfFixingThem(t *testing.T) {
	policy := workspace.DefaultPolicy()
	policy.Flow, policy.Git = workspace.FlowAudit, false
	repo := t.TempDir()
	ws, err := workspace.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{outputs: map[string][]string{"Staff Reviewer": {
		"```json\n{\"status\":\"changes_requested\",\"issues\":[{\"agent\":\"backend\",\"description\":\"sem validação\",\"location\":\"api.go:3\"}]}\n```"}}}
	o := New(exec, ws, DefaultStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.attempts = 1
	if err := o.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if run.Stories[0].Status != StepWarning {
		t.Fatalf("story = %s, logs: %+v", run.Stories[0].Status, run.Logs)
	}
	for _, s := range run.Stories[0].Steps {
		if s.Kind == "fix" {
			t.Errorf("an audit must not schedule fixes: %s", s.ID)
		}
	}
	if !strings.Contains(renderPendencies(run), "sem validação") {
		t.Error("the finding must reach the pendencies file")
	}
}
