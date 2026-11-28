package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/quality"
)

// newBaselineTest is a verifier test with the baseline on; check scripts each
// command call, the first being the baseline's.
func newBaselineTest(t *testing.T, check func(n int) quality.Result) (*Orchestrator, *fakeExecutor) {
	t.Helper()
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, check)
	policy := o.ws.Policy()
	policy.SkipBaseline = false
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	return o, exec
}

func checksIssue(t *testing.T, run Run) Issue {
	t.Helper()
	s := step(run.Stories[0].Steps, "checks")
	if s == nil || len(s.Issues) == 0 {
		t.Fatalf("checks step has no issue: %s", stepIDs(run.Stories[0].Steps))
	}
	return s.Issues[0]
}

func TestFailureThatWasThereBeforeIsLabeledPreexistingAndStillBlocks(t *testing.T) {
	o, _ := newBaselineTest(t, func(int) quality.Result {
		return quality.Result{ExitCode: 1, Output: "FAIL legacy.test.mjs: expected 2, got 1"}
	})
	_ = o.Start(realStory("Carrinho"))
	run := waitDone(t, o)

	st := run.Stories[0]
	if st.Baseline == nil || len(st.Baseline.Failing) != 1 || st.Baseline.Env == "" {
		t.Fatalf("baseline = %+v", st.Baseline)
	}
	is := checksIssue(t, run)
	if is.Origin != originPreexisting || !strings.Contains(is.Description, "preexistente") {
		t.Errorf("issue = %+v", is)
	}
	if st.Validation != ValidationFailed {
		t.Errorf("a preexisting failure is still a failure: %s", st.Validation)
	}
}

func TestFailureTheStoryIntroducedIsLabeledNew(t *testing.T) {
	o, _ := newBaselineTest(t, func(n int) quality.Result {
		if n == 1 {
			return quality.Result{Passed: true} // the baseline
		}
		return quality.Result{ExitCode: 1, Output: "FAIL cart.test.mjs: expected 2, got 1"}
	})
	_ = o.Start(realStory("Carrinho"))
	run := waitDone(t, o)

	if is := checksIssue(t, run); is.Origin != originNew || !strings.Contains(is.Description, "Falha nova") {
		t.Errorf("issue = %+v", is)
	}
	if b := run.Stories[0].Baseline; b == nil || len(b.Failing) != 0 || len(b.Results) != 1 {
		t.Errorf("baseline = %+v", b)
	}
}

func TestBaselineIsSkippedWhenTurnedOffOrSimulated(t *testing.T) {
	calls := 0
	o, _ := newBaselineTest(t, func(int) quality.Result { calls++; return quality.Result{Passed: true} })
	policy := o.ws.Policy()
	policy.SkipBaseline = true
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	_ = o.Start(realStory("Carrinho"))
	if run := waitDone(t, o); run.Stories[0].Baseline != nil || calls != 1 {
		t.Errorf("skip_baseline: baseline %+v, %d command call(s)", run.Stories[0].Baseline, calls)
	}

	policy.SkipBaseline = false
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	opts := realStory("Carrinho")
	opts.DryRun = true
	_ = o.Start(opts)
	if run := waitDone(t, o); run.Stories[0].Baseline != nil {
		t.Error("a simulation runs no command")
	}
}

func TestStoriesOfAWaveShareTheBaselineOfTheirCommit(t *testing.T) {
	o, _, _ := newPromotionTest(t, false, func(string) bool { return true })
	policy := o.ws.Policy()
	policy.SkipBaseline = false
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	_ = o.Start(twoIndependent())
	run := waitDone(t, o)

	a, b := run.Stories[0].Baseline, run.Stories[1].Baseline
	if a == nil || b == nil || a.Commit == "" || a.Commit != b.Commit {
		t.Fatalf("baselines = %+v / %+v", a, b)
	}
	reused := 0
	for _, l := range run.Logs {
		if strings.Contains(l.Message, "Linha de base") && strings.Contains(l.Message, "reutilizada") {
			reused++
		}
	}
	if reused != 1 {
		t.Errorf("the second story of the wave must reuse the first one's baseline (%d reuse logs)", reused)
	}
}

func TestFailureOriginComparesFingerprints(t *testing.T) {
	b := &Baseline{
		Results: []quality.Result{{Name: "unit"}, {Name: "lint", Passed: true}},
		Failing: map[string][]string{"unit": {"a", "b"}},
		Unknown: []string{"e2e"},
	}
	cases := []struct {
		cmd  string
		fps  []string
		want string
	}{
		{"unit", []string{"a"}, originPreexisting},
		{"unit", []string{"a", "c"}, originNew},
		{"unit", nil, originPreexisting},
		{"lint", []string{"x"}, originNew},
		{"e2e", []string{"x"}, ""},
		{"build", nil, ""},
	}
	for _, c := range cases {
		if got := b.failureOrigin(c.cmd, c.fps); got != c.want {
			t.Errorf("%s %v = %q, want %q", c.cmd, c.fps, got, c.want)
		}
	}
	var none *Baseline
	if none.failureOrigin("unit", nil) != "" {
		t.Error("no baseline, no origin")
	}
}

// postgresDown is what Jest prints for a suite whose database is not running.
const postgresDown = "FAIL test/backups.e2e-spec.ts\n  ● Backups (e2e) › status\n\n    AggregateError:\n\n    connect ECONNREFUSED 127.0.0.1:5432\n"

func TestServiceDownIsTheEnvironmentAndCallsNoFix(t *testing.T) {
	o, exec := newBaselineTest(t, func(int) quality.Result {
		return quality.Result{ExitCode: 1, Output: postgresDown}
	})
	_ = o.Start(realStory("Carrinho"))
	run := waitDone(t, o)

	st := run.Stories[0]
	if st.Baseline == nil || !strings.Contains(st.Baseline.Unavailable["unit"], "ECONNREFUSED 127.0.0.1:5432") {
		t.Fatalf("baseline = %+v", st.Baseline)
	}
	is := checksIssue(t, run)
	if !is.Infra || !strings.Contains(is.InfraReason, "suba o banco") || !strings.Contains(is.Description, "[Ambiente indisponível") {
		t.Errorf("issue = %+v", is)
	}
	if n := exec.count("fe"); n != 1 {
		t.Errorf("the implementer was called %d time(s); a database that is down is no code to fix", n)
	}
	if n := exec.count(InvestigatorStage.Role); n != 0 {
		t.Errorf("the investigator was called %d time(s)", n)
	}
	for _, c := range st.Causes {
		if c.Status != causeInfra || !strings.Contains(c.Reason, "Serviço do ambiente indisponível") {
			t.Errorf("cause = %+v", c)
		}
	}
	if s := step(st.Steps, "checks"); s.FailureClass != FailureInfra {
		t.Errorf("checks failure class = %q", s.FailureClass)
	}
	if !hasLog(run, "Ambiente indisponível na linha de base: unit") {
		t.Errorf("no environment warning: %+v", run.Logs)
	}
	if st.Validation != ValidationFailed {
		t.Errorf("nothing was proven with the database down: validation %s", st.Validation)
	}
}

func TestBaselineTakenWithTheEnvironmentDownRunsAgain(t *testing.T) {
	calls := 0
	exec := &fakeExecutor{}
	o, repo := newParallelStoryTest(t, exec)
	policy := o.ws.Policy()
	policy.SkipBaseline = false
	policy.Checks = []quality.Command{{Name: "unit", Run: "npm test"}}
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	o.runCheck = func(_ context.Context, _ string, c quality.Command, _ time.Duration) quality.Result {
		calls++
		return quality.Result{Name: c.Name, Command: c.Run, Passed: true}
	}
	initRepo(t, repo)
	head := gitOut(t, repo, "rev-parse", "HEAD")
	o.baselines.Store(baselineKey(head, policy.Checks), &Baseline{Commit: head, Failing: map[string][]string{"unit": nil}, Unavailable: map[string]string{"unit": "connect ECONNREFUSED 127.0.0.1:5432"}})

	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run := waitDone(t, o)
	b := run.Stories[0].Baseline
	if b == nil || len(b.Unavailable) != 0 || len(b.Failing) != 0 {
		t.Fatalf("the stale baseline was reused: %+v", b)
	}
	if !hasLog(run, "encontrou o ambiente indisponível; rodando de novo") || hasLog(run, "reutilizada") {
		t.Errorf("logs = %+v", run.Logs)
	}
}

func hasLog(run Run, part string) bool {
	for _, l := range run.Logs {
		if strings.Contains(l.Message, part) {
			return true
		}
	}
	return false
}
