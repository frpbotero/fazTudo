package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/stories"
)

func failing(check string, failures ...quality.Failure) quality.Result {
	c := quality.Command{Name: check}
	out := quality.Result{ExitCode: 1, Output: "FAIL", Evidence: "FAIL"}
	for _, f := range failures {
		f.Check, f.Fingerprint = c.Name, quality.TextFingerprint(f.File, f.Test, f.Message)
		out.Failures = append(out.Failures, f)
	}
	return out
}

var (
	cartFailure  = quality.Failure{File: "cart.spec.ts", Test: "soma o total", Message: "expected 2"}
	orderFailure = quality.Failure{File: "order.spec.ts", Test: "fecha o pedido", Message: "expected 3"}
	typeError    = quality.Failure{File: "cart.ts", Line: 3, Message: "TS2345: tipo errado", Compile: true}
)

func storyCauses(run Run) map[string]Cause {
	out := map[string]Cause{}
	for _, c := range run.Stories[0].Causes {
		out[c.Fingerprint] = c
	}
	return out
}

func joinedStepIDs(steps []Step) string {
	ids := make([]string, len(steps))
	for i, s := range steps {
		ids[i] = s.ID
	}
	return strings.Join(ids, " ")
}

func TestPersistentCauseGetsTwoAttemptsThenBecomesAPendency(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result { return failing("unit", cartFailure) })

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)

	if fixes := exec.count("fe") - 1; fixes != 2 {
		t.Fatalf("fix attempts = %d, want %d\nsteps: %s", fixes, maxCauseAttempts, joinedStepIDs(run.Stories[0].Steps))
	}
	cause := run.Stories[0].Causes[0]
	if cause.Status != causePending || cause.Attempts != 2 {
		t.Fatalf("cause = %+v", cause)
	}
}

func TestSecondAttemptUsesTheHeavyModelAndSeesTheFirst(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"fe": {
		"implementado", "```json\n" + `{"results":[{"cluster":"x","status":"fixed"}]}` + "\n```",
	}}}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result { return failing("unit", cartFailure) })

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)

	var tiers []provider.Tier
	var last string
	for i, role := range exec.roles {
		if role == "fe" {
			tiers = append(tiers, exec.reqs[i].Tier)
			last = exec.prompts[i]
		}
	}
	if tiers[len(tiers)-1] != provider.TierHeavy || tiers[1] == provider.TierHeavy {
		t.Fatalf("tiers = %v", tiers)
	}
	if !strings.Contains(last, "Tentativas anteriores") {
		t.Fatalf("second attempt must see the first:\n%s", last)
	}
}

func TestFixThatChangesNothingEndsTheLoopWithoutVerifying(t *testing.T) {
	exec := &fakeExecutor{onCall: func(provider.Request) {}}
	calls := 0
	o, _ := newVerifierTest(t, exec, func(int) quality.Result {
		calls++
		return failing("unit", cartFailure)
	})

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)

	if calls != 1 {
		t.Fatalf("command ran %d times; nothing changed after the first", calls)
	}
	if run.Stories[0].Causes[0].Status != causePending {
		t.Fatalf("cause = %+v", run.Stories[0].Causes[0])
	}
}

func TestBuildFailureSkipsTheCostlierLayers(t *testing.T) {
	var ran []string
	o, _ := newVerifierTest(t, &fakeExecutor{}, alwaysPasses)
	o.runCheck = recordingCheck(&ran, map[string]quality.Result{"build": failing("build", typeError)})
	setChecks(t, o, []quality.Command{
		{Name: "e2e", Run: "cd e2e && npm test"}, {Name: "unit", Run: "npm test"}, {Name: "build", Run: "npm run build"},
	})

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)

	if ran[0] != "build" || strings.Contains(strings.Join(ran, " "), "unit") {
		t.Fatalf("ran = %v", ran)
	}
}

func TestFixIsProvenByTheFailedCommandBeforeTheOthers(t *testing.T) {
	var ran []string
	first := true
	o, _ := newVerifierTest(t, &fakeExecutor{}, alwaysPasses)
	o.runCheck = recordingCheck(&ran, nil)
	base := o.runCheck
	o.runCheck = func(ctx contextT, dir string, c quality.Command, timeout durationT) quality.Result {
		if c.Name == "unit" && first {
			first = false
			base(ctx, dir, c, timeout)
			return failing("unit", cartFailure)
		}
		return base(ctx, dir, c, timeout)
	}
	setChecks(t, o, []quality.Command{{Name: "unit", Run: "npm test"}, {Name: "e2e", Run: "npx playwright test"}})

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	if run := waitDone(t, o); run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}

	if got := strings.Join(ran, " "); got != "unit unit e2e" {
		t.Fatalf("ran = %q, want the failed command first and the costly one once", got)
	}
}

func TestSameCompileErrorInTwoCommandsIsOneIssue(t *testing.T) {
	exec := &fakeExecutor{}
	fingerprinted := typeError
	fingerprinted.Fingerprint = "abc"
	same := quality.Result{ExitCode: 1, Output: "x", Failures: []quality.Failure{fingerprinted}}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result { return same })
	setChecks(t, o, []quality.Command{{Name: "api: build", Run: "npm run build"}, {Name: "web: build", Run: "npm run build"}})

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)

	if issues := run.Stories[0].Steps[2].Issues; len(issues) != 1 {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestFailureLeftPendingByAnEarlierStoryIsInherited(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result { return failing("unit", cartFailure) })
	opts := realStory("Carrinho")
	opts.Stories = append(opts.Stories, stories.Story{ID: "US-8", Title: "US-8 Pedido", Content: "Pedido"})
	policy := o.ws.Policy()
	policy.Sequential = true
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}

	if err := o.Start(opts); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)

	second := run.Stories[1]
	if len(second.Causes) != 1 || second.Causes[0].Status != causeInherited {
		t.Fatalf("second story causes = %+v", second.Causes)
	}
	for _, s := range second.Steps {
		if s.Kind == "fix" {
			t.Fatalf("an inherited failure must not reach an agent: %s", joinedStepIDs(second.Steps))
		}
	}
}

func TestInheritedFailureInAChangedFileIsARegression(t *testing.T) {
	is := Issue{Files: []string{"front/cart.spec.ts"}}

	if !touchesChanges(is, []string{"front/cart.spec.ts"}) {
		t.Fatal("the story changed the failing file")
	}
	if touchesChanges(is, []string{"front/other.ts"}) {
		t.Fatal("the story did not touch it")
	}
}

func TestInvestigationRunsBeforeFixesAndDirectsThem(t *testing.T) {
	failure := failing("unit", cartFailure)
	fingerprint := failure.Failures[0].Fingerprint
	dossierOutput := "```json\n" + fmt.Sprintf(`{"findings":[{"cluster":%q,"status":"confirmed","root_cause":"arredonda antes de somar",`+
		`"evidence":"cart.ts:9","reproduced_by":"rm -rf /","owner":"frontend","strategy":["somar antes"],"regression_test":"UT-1"}],"order":[%q]}`, fingerprint, fingerprint) + "\n```"
	exec := &fakeExecutor{outputs: map[string][]string{InvestigatorStage.Role: {dossierOutput}}}
	o, _ := newVerifierTest(t, exec, func(n int) quality.Result {
		if n == 1 {
			return failure
		}
		return quality.Result{Passed: true}
	})

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)

	want := "spec frontend checks investigate-checks-1 fix-checks-1-frontend checks-verify-2"
	if got := joinedStepIDs(run.Stories[0].Steps); got != want {
		t.Fatalf("steps = %s", got)
	}
	var fix string
	for i, role := range exec.roles {
		if role == InvestigatorStage.Role && (!exec.reqs[i].ReadOnly || !exec.reqs[i].Diagnose) {
			t.Fatalf("the investigator must not write: %+v", exec.reqs[i])
		}
		if role == "fe" {
			fix = exec.prompts[i]
		}
	}
	if !strings.Contains(fix, "arredonda antes de somar") || !strings.Contains(fix, "Dossiê") {
		t.Fatalf("fix prompt lost the dossier:\n%s", fix)
	}
	if strings.Contains(fix, "rm -rf") {
		t.Fatalf("a command from an agent has no place in the next prompt:\n%s", fix)
	}
	if storyCauses(run)[fingerprint].Status != causeFixed {
		t.Fatalf("causes = %+v", run.Stories[0].Causes)
	}
}

func TestCompileErrorGoesStraightToItsOwner(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, func(n int) quality.Result {
		if n == 1 {
			return failing("unit", typeError)
		}
		return quality.Result{Passed: true}
	})

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)

	if exec.count(InvestigatorStage.Role) != 0 {
		t.Fatal("a compile error explains itself")
	}
}

func TestBlockedCauseStopsWithoutAnotherAttempt(t *testing.T) {
	failure := failing("unit", typeError)
	blocked := "```json\n" + fmt.Sprintf(`{"results":[{"cluster":%q,"status":"blocked","reason":"falta credencial"}]}`, failure.Failures[0].Fingerprint) + "\n```"
	exec := &fakeExecutor{outputs: map[string][]string{"fe": {"implementado", blocked}}}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result { return failure })

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)

	if fixes := exec.count("fe") - 1; fixes != 1 {
		t.Fatalf("fixes = %d", fixes)
	}
	if cause := run.Stories[0].Causes[0]; cause.Status != causeBlocked || cause.Reason != "falta credencial" {
		t.Fatalf("cause = %+v", cause)
	}
}

func TestHandOverMovesTheCauseWithoutSpendingAnAttempt(t *testing.T) {
	o, _ := newVerifierTest(t, &fakeExecutor{}, alwaysPasses)
	o.baseStages = []Stage{{ID: "frontend", Implementer: true}, {ID: "backend", Implementer: true}}
	o.stages = o.baseStages
	o.run.Stories = []StoryRun{{Causes: []Cause{{Fingerprint: "x", Owner: "frontend", Status: causeOpen, Attempts: 1}}}}
	st := &storyState{}
	handOver := "```json\n" + `{"results":[{"cluster":"x","status":"reassign","owner":"backend","reason":"é da API"}]}` + "\n```"

	o.recordFix(st, []Issue{{Fingerprints: []string{"x"}}}, handOver)

	if c := o.causes(0)["x"]; c.Owner != "backend" || c.Attempts != 0 || !c.Reassigned {
		t.Fatalf("cause = %+v", c)
	}
	o.recordFix(st, []Issue{{Fingerprints: []string{"x"}}}, strings.ReplaceAll(handOver, "backend", "frontend"))
	if c := o.causes(0)["x"]; c.Owner != "backend" {
		t.Fatalf("an item must not bounce back: %+v", c)
	}
}

func TestE2ETestsAreNotRequiredWithTheStageOff(t *testing.T) {
	o, _ := newVerifierTest(t, &fakeExecutor{}, alwaysPasses)
	policy := o.ws.Policy()
	policy.Flow = "fast"
	ss := &session{policy: policy}
	traces := []quality.Trace{{Requirement: quality.Requirement{ID: "E2E-1", Kind: "e2e"}}}

	if issues := o.traceIssues(ss, traces, Plan{}); len(issues) != 0 {
		t.Fatalf("issues = %+v", issues)
	}
}

type (
	contextT  = context.Context
	durationT = time.Duration
)

// recordingCheck answers from results, passing by default, and logs the names.
func recordingCheck(ran *[]string, results map[string]quality.Result) func(contextT, string, quality.Command, durationT) quality.Result {
	return func(_ contextT, _ string, c quality.Command, _ durationT) quality.Result {
		*ran = append(*ran, c.Name)
		if r, ok := results[c.Name]; ok {
			r.Name, r.Command = c.Name, c.Run
			return r
		}
		return quality.Result{Name: c.Name, Command: c.Run, Passed: true}
	}
}

func setChecks(t *testing.T, o *Orchestrator, checks []quality.Command) {
	t.Helper()
	policy := o.ws.Policy()
	policy.Checks = checks
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
}

func lastPromptOf(exec *fakeExecutor, role string) string {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	last := ""
	for i, r := range exec.roles {
		if r == role {
			last = exec.prompts[i]
		}
	}
	return last
}

func TestReportedIdsMustBelongToTheFix(t *testing.T) {
	o, _ := newVerifierTest(t, &fakeExecutor{}, alwaysPasses)
	o.run.Stories = []StoryRun{{Causes: []Cause{{Fingerprint: "x", Status: causeOpen}}}}
	report := "```json\n" + `{"results":[{"cluster":"inventado","status":"fixed"}]}` + "\n```"

	o.recordFix(&storyState{}, []Issue{{Fingerprints: []string{"x"}}}, report)

	causes := o.causes(0)
	if _, invented := causes["inventado"]; invented || len(causes["x"].History) != 1 {
		t.Fatalf("causes = %+v", causes)
	}
}

func TestPendenciesHandOverTheDiagnosis(t *testing.T) {
	run := Run{Stories: []StoryRun{{ID: "US-1", Title: "Carrinho", Status: StepWarning, Causes: []Cause{
		{Fingerprint: "a1", Title: "soma o total", Status: causePending, RootCause: "arredonda antes", Reason: "Persistiu após 2 tentativas."},
		{Fingerprint: "b2", Title: "já corrigida", Status: causeFixed},
	}}}}

	got := renderPendencies(run)

	for _, want := range []string{"soma o total", "arredonda antes", "persistiu após as tentativas", "`a1`"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "já corrigida") {
		t.Errorf("fixed causes are not pendencies:\n%s", got)
	}
}
