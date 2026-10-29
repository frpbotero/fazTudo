package pipeline

import (
	"context"
	"strings"
	"testing"

	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

func TestPromptsNameOnlyInstalledSkills(t *testing.T) {
	saved := installedSkills
	t.Cleanup(func() { installedSkills = saved })
	installedSkills = func(names []string) []string {
		var out []string
		for _, n := range names {
			if n != "api-and-interface-design" {
				out = append(out, n)
			}
		}
		return out
	}
	stage := Stage{ID: "backend", Task: "faça", Skills: []string{"test-driven-development", "api-and-interface-design"}}
	prompt := buildPrompt(stage, storyContext{}, nil, nil)
	if !strings.Contains(prompt, "## Skills de referência") || !strings.Contains(prompt, "`test-driven-development`") {
		t.Errorf("installed skill missing from prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "api-and-interface-design") {
		t.Error("a skill that is not installed must not be named")
	}

	installedSkills = func([]string) []string { return nil }
	if prompt := buildPrompt(stage, storyContext{}, nil, nil); strings.Contains(prompt, "## Skills") {
		t.Error("no installed skill: no skills section")
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
	o.stages = lensStages

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
