package pipeline

import (
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/workspace"
)

func TestDependentRecordsTheApprovedSnapshotItBuiltOn(t *testing.T) {
	o, _, _ := newPromotionTest(t, false, func(string) bool { return true })
	_ = o.Start(StartOptions{Stories: threeStories, Source: "Markdown"})
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs %+v", run.Result, run.Logs)
	}
	built := run.Stories[2].BuiltOn
	if len(built) != 2 || built["US-1"] == "" || built["US-2"] == "" {
		t.Fatalf("US-3 built on = %v", built)
	}
	for _, p := range run.Promotions {
		if p.Status == IntegrationIntegrated && strings.Contains(strings.Join(p.Stories, ","), "US-1") && p.Commit != built["US-1"] {
			t.Errorf("US-3 records %s, the promotion was %s", built["US-1"], p.Commit)
		}
		if p.Status == IntegrationIntegrated && len(p.Repos) == 0 {
			t.Errorf("promotion %v has no per-repository record", p.Stories)
		}
	}
}

func TestAStoryFailingInItsLaneDoesNotStopTheOthers(t *testing.T) {
	o, repo, exec := newPromotionTest(t, false, func(string) bool { return true })
	exec.failIf = func(req provider.Request) bool { return storyMarker(req.Prompt) == "dois" && req.Role == "ra" }
	_ = o.Start(StartOptions{Stories: threeStories, Source: "Markdown"})
	run := waitDone(t, o)
	if run.Result != ResultFailed {
		t.Fatalf("result = %s", run.Result)
	}
	if st := run.Stories[0]; st.Status != StepDone || st.Integration != IntegrationIntegrated {
		t.Errorf("US-1 must finish and enter the base beside the failure: %s %s", st.Status, st.Integration)
	}
	if !exists(repo, "um.txt") {
		t.Error("US-1's work is not in the base")
	}
	if st := run.Stories[1]; st.Status != StepFailed {
		t.Errorf("US-2 = %s", st.Status)
	}
	if st := run.Stories[2]; st.Status == StepDone {
		t.Error("US-3 depends on the failed US-2 and must not run")
	}
	if run.Resume == nil || run.Resume.Story != 1 {
		t.Errorf("the run resumes from the failed story: %+v", run.Resume)
	}
}

func TestPromotableRefusesAStoryThatDidNotFinish(t *testing.T) {
	for _, s := range []StepStatus{StepFailed, StepCanceled, StepRunning, StepPending} {
		if promotable(StoryRun{Status: s, Validation: ValidationPassed}, policyPromotingUnverified()) == "" {
			t.Errorf("%s story was promotable", s)
		}
	}
}

func policyPromotingUnverified() workspace.Policy {
	p := workspace.DefaultPolicy()
	p.PromoteUnverified = true
	return p
}
