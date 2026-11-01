package pipeline

import (
	"fmt"
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/stories"
)

var assessStages = []Stage{
	{ID: "context", Title: "Passo 0", Role: "scout", Assessor: true},
	{ID: "frontend", Title: "Front", Role: "fe", Implementer: true},
	{ID: "checks", Title: "Checks", Role: "gate", Verifier: true},
	{ID: "review", Title: "Review", Role: "rv", Gate: true},
}

func decision(d string) string {
	return fmt.Sprintf("Relatório.\n```json\n{\"decision\": %q, \"reason\": \"src/x.ts:10\"}\n```", d)
}

func passing(int) quality.Result { return quality.Result{Passed: true} }

func TestParseAssessmentDefaultsToImplement(t *testing.T) {
	cases := map[string]string{
		decision("Reuse"):   DecisionReuse,
		decision("block"):   DecisionBlock,
		decision("rewrite"): DecisionImplement,
		"sem json":          DecisionImplement,
	}
	for out, want := range cases {
		if got := parseAssessment(out).Decision; got != want {
			t.Errorf("parseAssessment(%q) = %s, want %s", out, got, want)
		}
	}
}

func TestPassoZeroReuseClosesStoryWithoutImplementing(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"scout": {decision(DecisionReuse)}}}
	o, _ := newVerifierTest(t, exec, passing)
	o.baseStages = assessStages
	o.stages = o.baseStages
	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if run.Result != ResultSucceeded || run.Stories[0].Status != StepDone {
		t.Fatalf("result = %s, story = %s", run.Result, run.Stories[0].Status)
	}
	if exec.count("fe") != 0 || exec.count("rv") != 0 {
		t.Errorf("no agent may run after reuse: %v", exec.roles)
	}
	for _, s := range run.Stories[0].Steps[1:] {
		if s.Status != StepSkipped {
			t.Errorf("step %s = %s, want skipped", s.ID, s.Status)
		}
	}
}

func TestPassoZeroBlockSkipsStoryAndContinues(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"scout": {decision(DecisionBlock), decision(DecisionImplement)}}}
	o, _ := newVerifierTest(t, exec, passing)
	o.baseStages = assessStages
	o.stages = o.baseStages
	opts := realStory("x")
	opts.Stories = append(opts.Stories, stories.Story{ID: "US-8", Title: "US-8 Próxima", Content: "y"})
	if err := o.Start(opts); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}
	if run.Stories[0].Status != StepSkipped || run.Stories[1].Status != StepDone {
		t.Errorf("stories = %s, %s; want skipped, done", run.Stories[0].Status, run.Stories[1].Status)
	}
	if exec.count("fe") != 1 {
		t.Errorf("frontend calls = %d, want 1 (only the second story)", exec.count("fe"))
	}
}

func TestReviewFixThatBreaksChecksSkipsReviewer(t *testing.T) {
	changes := "```json\n{\"status\": \"changes_requested\", \"issues\": [{\"agent\": \"frontend\", \"description\": \"nome ruim\"}]}\n```"
	approved := "```json\n{\"status\": \"approved\", \"issues\": []}\n```"
	exec := &fakeExecutor{outputs: map[string][]string{
		"scout": {decision(DecisionExtend)},
		"rv":    {changes, approved},
	}}
	o, repo := newVerifierTest(t, exec, func(n int) quality.Result {
		if n == 2 { // right after the review's first fix round
			return quality.Result{ExitCode: 1, Output: "FAIL build"}
		}
		return quality.Result{Passed: true}
	})
	o.baseStages = assessStages
	o.stages = o.baseStages
	edits := 0
	exec.onCall = func(req provider.Request) {
		if req.Role == "fe" {
			edits++
			writeFile(t, repo, "app.js", fmt.Sprintf("const v = %d;\n", edits))
		}
	}
	if err := o.Start(realStory("x")); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if got := exec.count("rv"); got != 2 {
		t.Errorf("reviewer calls = %d, want 2 (failing checks must not reach the reviewer)", got)
	}
	if got := exec.count("fe"); got != 3 {
		t.Errorf("frontend calls = %d, want 3 (implementation + 2 fixes)", got)
	}
	var verify Step
	for _, s := range run.Stories[0].Steps {
		if s.ID == "review-verify-2" {
			verify = s
		}
	}
	if len(verify.Checks) != 1 || verify.Checks[0].Passed || !strings.Contains(verify.Output, "FAIL build") {
		t.Errorf("round 2 must record the failing checks: %+v", verify)
	}
}
