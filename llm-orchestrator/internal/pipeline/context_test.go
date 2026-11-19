package pipeline

import (
	"context"
	"slices"
	"strings"
	"testing"

	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/skills"
	"llm-orchestrator/internal/workspace"
)

func TestPromptsPointAtBundledSkills(t *testing.T) {
	stage := Stage{ID: "backend", Task: "faça", Skills: []string{"test-driven-development", "not-bundled"}}
	prompt := buildPrompt(stage, storyContext{}, nil, nil)
	if !strings.Contains(prompt, "## Skills") || !strings.Contains(prompt, skills.File("/skills", "test-driven-development")) {
		t.Errorf("bundled skill path missing from prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "not-bundled") {
		t.Error("a skill that is not bundled must not be named")
	}

	saved := skillsDir
	t.Cleanup(func() { skillsDir = saved })
	skillsDir = func() string { return "" }
	if prompt := buildPrompt(stage, storyContext{}, nil, nil); strings.Contains(prompt, "## Skills") {
		t.Error("skills unavailable: no skills section")
	}
}

func TestStageSkillsAreBundled(t *testing.T) {
	names := slices.Clone(fixSkills)
	for _, s := range DefaultStages {
		names = append(names, s.Skills...)
	}
	for _, n := range names {
		if !skills.Has(n) {
			t.Errorf("skill %q is named by a stage but not bundled (scripts/update-skills.sh)", n)
		}
	}
}

func TestCodeReviewChecksStructure(t *testing.T) {
	i := slices.IndexFunc(DefaultStages, func(s Stage) bool { return s.ID == "review" })
	if i < 0 {
		t.Fatal("review stage missing")
	}
	review := DefaultStages[i]
	if !slices.Contains(review.Skills, "structural-health") || !strings.Contains(review.Task, "structural-health") {
		t.Errorf("code review must run structural-health in review mode: %+v", review)
	}
}

func TestPromptsPointAtTheCodeGraph(t *testing.T) {
	report := &workspace.Report{Path: "/repo", Graph: "/repo/graphify-out/graph.json"}
	prompt := buildPrompt(Stage{Task: "faça"}, storyContext{}, report, nil)
	if !strings.Contains(prompt, "graphify query") || !strings.Contains(prompt, "--graph /repo/graphify-out/graph.json") {
		t.Errorf("prompt lacks the graph:\n%s", prompt)
	}
	report.Graph = ""
	if prompt := buildPrompt(Stage{Task: "faça"}, storyContext{}, report, nil); strings.Contains(prompt, "graphify") {
		t.Error("without a graph the prompt must not mention graphify")
	}
}

func TestEveryAgentGetsTheCodeGraph(t *testing.T) {
	saved := updateCodeGraph
	t.Cleanup(func() { updateCodeGraph = saved })
	updates := 0
	updateCodeGraph = func(context.Context, string) (string, error) {
		updates++
		return "/g/graph.json", nil
	}
	exec := &fakeExecutor{outputs: map[string][]string{"arch": {"```json\n{\"agents\":[\"backend\"]}\n```"}}}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	o.baseStages = lensStages
	o.stages = o.baseStages

	_ = o.Start(realStory("API de carrinho"))
	if run := waitDone(t, o); run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if updates != 1 {
		t.Errorf("graph updated %d times, want once per run", updates)
	}
	for _, req := range exec.reqs {
		if !strings.Contains(req.Prompt, "--graph /g/graph.json") {
			t.Errorf("%s prompt lacks the code graph", req.Role)
		}
	}
}
