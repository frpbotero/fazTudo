package pipeline

import (
	"strings"
	"testing"

	"llm-orchestrator/internal/quality"
)

func TestPlannersAndReviewersAreReadOnly(t *testing.T) {
	writers := map[string]bool{"frontend": true, "backend": true, "e2e": true}
	for _, s := range DefaultStages {
		if s.Verifier {
			continue
		}
		if s.ReadOnly == writers[s.ID] {
			t.Errorf("stage %s: ReadOnly = %v", s.ID, s.ReadOnly)
		}
	}
}

var lensStages = []Stage{
	{ID: "arch", Title: "Arch", Role: "arch", Planner: true, ReadOnly: true},
	{ID: "frontend", Title: "Front", Role: "fe", Selectable: true, Implementer: true},
	{ID: "backend", Title: "Back", Role: "be", Selectable: true, Implementer: true},
	{ID: "review", Title: "Review", Role: "rev", Gate: true, ReadOnly: true, Lenses: []Lens{
		{ID: "spec", Title: "Spec", Focus: "requisitos"},
		{ID: "tests", Title: "Tests", Focus: "testes"},
		{ID: "ux", Title: "UX", Focus: "interface", Requires: "frontend"},
	}},
}

func TestReviewLensesRunInParallelAndMerge(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"arch":        {"```json\n{\"agents\":[\"backend\"]}\n```"},
		"rev · Spec":  {"ok\n```json\n{\"status\":\"approved\",\"issues\":[]}\n```"},
		"rev · Tests": {"faltou\n```json\n{\"status\":\"changes_requested\",\"issues\":[{\"agent\":\"backend\",\"description\":\"falta teste UT-1\"}]}\n```"},
		"rev":         {"```json\n{\"status\":\"approved\",\"issues\":[]}\n```"},
	}}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	o.stages = lensStages
	writeFile(t, repo, "AGENTS.md", "Sempre escreva testes primeiro.")

	_ = o.Start(realStory("API de carrinho"))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if exec.count("rev · UX") != 0 {
		t.Error("the UX lens needs the frontend agent, which the plan did not select")
	}
	if exec.count("rev · Spec") != 1 || exec.count("rev · Tests") != 1 {
		t.Error("each active lens must run once")
	}
	review := run.Stories[0].Steps[3]
	if len(review.Issues) != 1 || review.Issues[0].Agent != "backend" ||
		!strings.Contains(review.Output, "## Revisão: Tests") || !strings.Contains(review.Output, "## Revisão: Spec") {
		t.Fatalf("merged review = %+v", review)
	}
	if exec.count("be") != 2 || exec.count("rev") != 1 {
		t.Errorf("expected a backend fix and one general verification: be=%d rev=%d", exec.count("be"), exec.count("rev"))
	}

	for _, req := range exec.reqs {
		wantRO := req.Role == "arch" || strings.HasPrefix(req.Role, "rev") || req.Role == DiscoveryStage.Role
		if req.ReadOnly != wantRO {
			t.Errorf("%s: ReadOnly = %v", req.Role, req.ReadOnly)
		}
		if req.Role != DiscoveryStage.Role && !strings.Contains(req.Prompt, "Sempre escreva testes primeiro.") {
			t.Errorf("%s prompt lacks the project instructions", req.Role)
		}
	}
}
