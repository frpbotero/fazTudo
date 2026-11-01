package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

// stackEvents records, in order, what touches the test stacks during a run.
type stackEvents struct {
	mu     sync.Mutex
	events []string
}

func (e *stackEvents) add(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (e *stackEvents) String() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.events, " ")
}

// trackStacks replaces the slot and the sweep of o and uses check as the
// repository's only command.
func trackStacks(t *testing.T, o *Orchestrator, check quality.Command, sequential bool) *stackEvents {
	t.Helper()
	events := &stackEvents{}
	o.lockStackSlot = func(context.Context, func()) (func(), error) {
		events.add("lock")
		return func() { events.add("release") }, nil
	}
	o.sweepStacks = func(context.Context, []string, []string) ([]string, error) {
		events.add("sweep")
		return nil, nil
	}
	run := o.runCheck
	o.runCheck = func(ctx context.Context, dir string, c quality.Command, timeout time.Duration) quality.Result {
		events.add("run")
		return run(ctx, dir, c, timeout)
	}
	policy := o.ws.Policy()
	policy.Checks = []quality.Command{check}
	policy.Sequential = sequential
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	return events
}

func alwaysPasses(int) quality.Result { return quality.Result{Passed: true} }

var e2eCheck = quality.Command{Name: "e2e: test", Run: "cd e2e && npm run test"}

func TestHeavyCheckHoldsTheSlotAndSweepsAroundIt(t *testing.T) {
	o, _ := newVerifierTest(t, &fakeExecutor{}, alwaysPasses)
	events := trackStacks(t, o, e2eCheck, true)

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)

	want := "sweep lock sweep run sweep release sweep" // run start, the check, run end
	if got := events.String(); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestLightCheckNeedsNeitherSlotNorPerCheckSweep(t *testing.T) {
	o, _ := newVerifierTest(t, &fakeExecutor{}, alwaysPasses)
	events := trackStacks(t, o, quality.Command{Name: "unit", Run: "go test ./..."}, true)

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)

	if got := events.String(); got != "sweep run sweep" {
		t.Fatalf("events = %q", got)
	}
}

func TestParallelRunsKeepTheSlotButNotThePerCheckSweep(t *testing.T) {
	o, _ := newVerifierTest(t, &fakeExecutor{}, alwaysPasses)
	events := trackStacks(t, o, e2eCheck, false)

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)

	// A sibling story's agent may be using its own stack: only the run's ends sweep.
	if got := events.String(); got != "sweep lock run release sweep" {
		t.Fatalf("events = %q", got)
	}
}

func TestSweepCoversTheRepositoryAndTheCheckDirectory(t *testing.T) {
	o, repo := newVerifierTest(t, &fakeExecutor{}, alwaysPasses)
	trackStacks(t, o, e2eCheck, true)
	var mu sync.Mutex
	var seen [][]string
	o.sweepStacks = func(_ context.Context, roots, patterns []string) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, roots)
		if strings.Join(patterns, ",") != strings.Join(workspace.DefaultEphemeralStacks, ",") {
			t.Errorf("patterns = %v", patterns)
		}
		return nil, nil
	}

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)

	mu.Lock()
	defer mu.Unlock()
	for _, roots := range seen {
		if roots[0] != repo {
			t.Errorf("roots = %v, want the repository first", roots)
		}
	}
}

func TestSweepFailureDoesNotFailTheRun(t *testing.T) {
	o, _ := newVerifierTest(t, &fakeExecutor{}, alwaysPasses)
	trackStacks(t, o, e2eCheck, true)
	o.sweepStacks = func(context.Context, []string, []string) ([]string, error) {
		return nil, errors.New("daemon parado")
	}

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}

	if run := waitDone(t, o); run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}
}

func TestDryRunNeverSweeps(t *testing.T) {
	o, _ := newVerifierTest(t, &fakeExecutor{dry: true}, alwaysPasses)
	events := trackStacks(t, o, e2eCheck, true)

	opts := realStory("Carrinho")
	opts.DryRun = true
	if err := o.Start(opts); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)

	if got := events.String(); got != "" {
		t.Fatalf("events = %q", got)
	}
}

func TestTimedOutCheckBecomesAPendencyWithoutAgentCalls(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result {
		return quality.Result{TimedOut: true, ExitCode: -1, DurationMS: 600_000, Output: "#28 [front builder 7/7] RUN npm run build:ssr"}
	})

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)

	if calls := exec.count("fe"); calls != 1 {
		t.Fatalf("frontend calls = %d, want only the implementation", calls)
	}
	checks := run.Stories[0].Steps[2]
	if checks.Status != StepWarning || len(checks.Issues) != 1 || !checks.Issues[0].Infra {
		t.Fatalf("checks = %+v", checks)
	}
	if strings.Contains(checks.Issues[0].Description, "Corrija o código") {
		t.Fatalf("a timeout says nothing about the code: %s", checks.Issues[0].Description)
	}
}

func TestTimedOutCheckStaysOutOfTheFixRequest(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, func(n int) quality.Result {
		if n%2 == 1 {
			return quality.Result{ExitCode: 1, Output: "FAIL cart.test.mjs"}
		}
		return quality.Result{TimedOut: true, ExitCode: -1, Output: "build em andamento"}
	})
	policy := o.ws.Policy()
	policy.Checks = []quality.Command{{Name: "unit", Run: "npm test"}, {Name: "slow", Run: "npm run slow"}}
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)

	fix := exec.prompts[3] // discovery, spec, frontend, fix
	if !strings.Contains(fix, "FAIL cart.test.mjs") || strings.Contains(fix, "build em andamento") {
		t.Fatalf("fix prompt must carry only the code failure:\n%s", fix)
	}
}

func TestModelCannotMarkIssuesAsInfrastructure(t *testing.T) {
	v, ok := parseVerdict("```json\n"+`{"status":"changes_requested","issues":[{"description":"x","infra":true}]}`+"\n```", verifierStages, Plan{})
	if !ok || v.Issues[0].Infra {
		t.Fatalf("verdict = %+v", v)
	}
}

// TestFailuresGoToWhoOwnsThem covers the swaps of US-PRC-02: keywords sent the
// API's Jest suite to the frontend and the store's tests to the backend, and
// each agent left the item for the other.
func TestFailuresGoToWhoOwnsThem(t *testing.T) {
	o, _ := newVerifierTest(t, &fakeExecutor{}, alwaysPasses)
	o.stages = []Stage{{ID: "frontend", Implementer: true}, {ID: "backend", Implementer: true}}
	ss := &session{report: &workspace.Report{Stacks: []workspace.Stack{
		{Dir: "front", Name: "Node.js"}, {Dir: "front", Name: "Angular"},
		{Dir: "core-api", Name: "Node.js"}, {Dir: "e2e", Name: "Playwright"},
	}}}
	plan := Plan{Agents: []string{"frontend", "backend"}, Streams: []Stream{{Agent: "backend", Files: []string{"front/server/"}}}}

	cases := []struct {
		command  quality.Command
		failures []quality.Failure
		want     string
	}{
		{quality.Command{Name: "core-api: test:e2e", Run: "cd core-api && npm run test:e2e"}, nil, "backend"},
		{quality.Command{Name: "front: test", Run: "cd front && npm run test"}, nil, "frontend"},
		{quality.Command{Name: "front: build", Run: "cd front && npm run build"}, nil, "frontend"},
		{quality.Command{Name: "e2e: test", Run: "cd e2e && npm run test"}, nil, "frontend"},
		{quality.Command{Name: "front: test", Run: "cd front && npm run test"}, []quality.Failure{{File: "server/ssr.ts"}}, "backend"},
	}
	for _, tc := range cases {
		if got := o.assignFailure(ss, tc.command, tc.failures, plan); got != tc.want {
			t.Errorf("%s = %s, want %s", tc.command.Name, got, tc.want)
		}
	}
}

func TestFixRequestQuotesEvidenceLostFromTheTail(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, func(n int) quality.Result {
		if n == 1 {
			return quality.Result{ExitCode: 1, Output: "TOTAL: 1 FAILED", Evidence: "Chrome Foo deve salvar FAILED"}
		}
		return quality.Result{Passed: true}
	})

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)

	if fix := exec.prompts[3]; !strings.Contains(fix, "Foo deve salvar FAILED") {
		t.Fatalf("fix prompt lost the test name:\n%s", fix)
	}
}
