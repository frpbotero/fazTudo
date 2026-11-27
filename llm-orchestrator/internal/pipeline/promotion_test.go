package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

// gatedStages implement and then verify, so a story ends with a validation.
var gatedStages = []Stage{
	{ID: "a", Title: "A", Role: "ra", Implementer: true},
	{ID: "checks", Title: "Checks", Role: "gate", Verifier: true},
}

// newPromotionTest is a git repository whose stories are verified by a
// command that passes while pass(dir) holds; agents write <story>.txt.
func newPromotionTest(t *testing.T, sequential bool, pass func(dir string) bool) (*Orchestrator, string, *fakeExecutor) {
	t.Helper()
	exec := &fakeExecutor{outputs: map[string][]string{AnalysisStage.Role: {independentPair}}}
	o, repo := newParallelStoryTest(t, exec)
	o.baseStages = gatedStages
	o.stages = o.baseStages
	policy := o.ws.Policy()
	policy.PromoteUnverified = false
	policy.Sequential = sequential
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	exec.onCall = func(req provider.Request) {
		if m := storyMarker(req.Prompt); m != "" && req.Role == "ra" {
			writeFile(t, req.WorkDir, m+".txt", m+"\n")
		}
	}
	o.runCheck = func(_ context.Context, dir string, c quality.Command, _ time.Duration) quality.Result {
		return quality.Result{Name: c.Name, Command: c.Run, Passed: pass(dir)}
	}
	return o, repo, exec
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestStoriesThatPassAloneButBreakTogetherStayOutOfTheBase(t *testing.T) {
	o, repo, _ := newPromotionTest(t, false, func(dir string) bool {
		return !(exists(dir, "um.txt") && exists(dir, "dois.txt"))
	})
	_ = o.Start(StartOptions{Stories: threeStories, Source: "Markdown"})
	run := waitDone(t, o)
	base := run.BaseBranch

	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	for _, si := range []int{0, 1} {
		st := run.Stories[si]
		if st.Validation != ValidationPassed {
			t.Errorf("%s passes alone: validation %s", st.ID, st.Validation)
		}
		if st.Integration != IntegrationBlocked || !strings.Contains(st.IntegrationNote, "conjunto integrado") {
			t.Errorf("%s must be kept out by the integrated check: %s %q", st.ID, st.Integration, st.IntegrationNote)
		}
		if gitOut(t, repo, "branch", "--list", st.Branch) == "" {
			t.Errorf("%s: branch %s must be kept for review", st.ID, st.Branch)
		}
	}
	if exists(repo, "um.txt") || exists(repo, "dois.txt") {
		t.Error("the base must stay as it was")
	}
	if got := gitOut(t, repo, "branch", "--show-current"); got != base {
		t.Errorf("repository must be back on the base %q, got %q", base, got)
	}
	if st := run.Stories[2]; st.Status != StepSkipped || !strings.Contains(st.IntegrationNote, "depende de") {
		t.Errorf("US-3 depends on stories kept out: %s %q", st.Status, st.IntegrationNote)
	}
	if n := len(run.Promotions); n != 1 || run.Promotions[0].Status != IntegrationBlocked || len(run.Promotions[0].Checks) == 0 || run.Promotions[0].Checks[0].Passed {
		t.Errorf("the decision must record the failing check on the candidate: %+v", run.Promotions)
	}
}

func TestStoriesThatPassTogetherAreIntegratedAfterTheCandidateCheck(t *testing.T) {
	o, repo, _ := newPromotionTest(t, false, func(string) bool { return true })
	_ = o.Start(twoIndependent())
	run := waitDone(t, o)

	if !exists(repo, "um.txt") || !exists(repo, "dois.txt") {
		t.Fatalf("both stories must reach the base; logs: %+v", run.Logs)
	}
	for _, st := range run.Stories {
		if st.Integration != IntegrationIntegrated || st.Status != StepDone {
			t.Errorf("%s: %s %s", st.ID, st.Status, st.Integration)
		}
	}
	if len(run.Promotions) != 1 || run.Promotions[0].Status != IntegrationIntegrated || run.Promotions[0].Commit == "" {
		t.Errorf("promotions = %+v", run.Promotions)
	}
}

func TestRejectedStoryIsNotPromotedAndIndependentOnesGoOn(t *testing.T) {
	o, repo, _ := newPromotionTest(t, true, func(dir string) bool { return !exists(dir, "um.txt") })
	_ = o.Start(twoIndependent())
	run := waitDone(t, o)
	base := run.BaseBranch

	first, second := run.Stories[0], run.Stories[1]
	if first.Validation != ValidationFailed || first.Integration != IntegrationBlocked || first.IntegrationNote != "validação reprovada" {
		t.Errorf("US-1: %s %s %q", first.Validation, first.Integration, first.IntegrationNote)
	}
	if second.Integration != IntegrationIntegrated {
		t.Errorf("US-2 is independent and passes: %s %q; logs: %+v", second.Integration, second.IntegrationNote, run.Logs)
	}
	if exists(repo, "um.txt") || !exists(repo, "dois.txt") {
		t.Error("only US-2 may reach the base")
	}
	if got := gitOut(t, repo, "branch", "--show-current"); got != base {
		t.Errorf("repository must end on the base %q, got %q", base, got)
	}
	if out := gitOut(t, repo, "show", first.Branch+":um.txt"); out != "um" {
		t.Errorf("US-1's work must stay on its branch, got %q", out)
	}
}

func TestDeliveryWithoutExecutedEvidenceIsNotPromotedByDefault(t *testing.T) {
	o, repo, _ := newPromotionTest(t, true, func(string) bool { return true })
	o.baseStages = testStages // nothing judges the story
	o.stages = o.baseStages
	policy := o.ws.Policy()
	policy.AutoChecks, policy.Checks = true, nil // nothing to detect in this repository
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	exec := o.exec.(*fakeExecutor)
	exec.onCall = func(req provider.Request) {
		if req.Role == "ra" {
			writeFile(t, req.WorkDir, "um.txt", "um\n")
		}
	}
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run := waitDone(t, o)

	st := run.Stories[0]
	if st.Validation != ValidationNotEvaluated || st.Integration != IntegrationBlocked || !strings.Contains(st.IntegrationNote, "sem validação") {
		t.Fatalf("%s %s %q", st.Validation, st.Integration, st.IntegrationNote)
	}
	if exists(repo, "um.txt") {
		t.Error("an unverified delivery must not reach the base")
	}

	// Allowed explicitly, the same delivery is promoted.
	policy.PromoteUnverified = true
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run = waitDone(t, o)
	if st := run.Stories[0]; st.Integration != IntegrationIntegrated || !exists(repo, "um.txt") {
		t.Errorf("promote_unverified: %s %q; logs: %+v", st.Integration, st.IntegrationNote, run.Logs)
	}
}

func TestBaseThatMovedAfterVerificationIsNotOverwritten(t *testing.T) {
	o, repo, exec := newPromotionTest(t, true, func(string) bool { return true })
	exec.onCall = func(req provider.Request) {
		if req.Role != "ra" {
			return
		}
		base := o.Snapshot().BaseBranch
		writeFile(t, req.WorkDir, "um.txt", "um\n")
		// Someone commits to the base while the story is being developed.
		wt := filepath.Join(t.TempDir(), "base")
		gitOut(t, repo, "worktree", "add", "-q", wt, base)
		writeFile(t, wt, "outro.txt", "de fora\n")
		gitOut(t, wt, "add", "outro.txt")
		gitOut(t, wt, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "fora")
		gitOut(t, repo, "worktree", "remove", wt)
	}
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run := waitDone(t, o)

	st := run.Stories[0]
	if st.Integration != IntegrationBlocked || !strings.Contains(st.IntegrationNote, "a base mudou") {
		t.Fatalf("%s %q; logs: %+v", st.Integration, st.IntegrationNote, run.Logs)
	}
	if exists(repo, "um.txt") || !exists(repo, "outro.txt") {
		t.Error("the base must keep only its own commit")
	}
}

func TestPromotableRequiresAnApprovedDelivery(t *testing.T) {
	strict, lax := workspace.Policy{}, workspace.Policy{PromoteUnverified: true}
	cases := []struct {
		st          StoryRun
		strict, lax bool
	}{
		{StoryRun{Status: StepDone, Validation: ValidationPassed}, true, true},
		{StoryRun{Status: StepWarning, Validation: ValidationFailed}, false, false},
		{StoryRun{Status: StepWarning, Validation: ValidationInconclusive}, false, false},
		{StoryRun{Status: StepDone, Validation: ValidationNotEvaluated}, false, true},
		{StoryRun{Status: StepDone}, false, true}, // older journal: no outcome recorded
		{StoryRun{Status: StepSkipped, Validation: ValidationNotEvaluated}, false, false},
	}
	for _, c := range cases {
		if got := promotable(c.st, strict) == ""; got != c.strict {
			t.Errorf("strict %+v = %v", c.st, got)
		}
		if got := promotable(c.st, lax) == ""; got != c.lax {
			t.Errorf("promote_unverified %+v = %v", c.st, got)
		}
	}
}
