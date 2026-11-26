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
	"llm-orchestrator/internal/stories"
)

func TestChangeKind(t *testing.T) {
	cases := []struct{ kind, reason, text, want string }{
		{"bug", "", "", ChangeBug},
		{"Correção", "", "", ChangeBug},
		{"refactor", "", "", ChangeRefactor},
		{"trivial", "só um texto", "", ChangeTrivial},
		{"trivial", "", "", ChangeFeature}, // a trivial claim needs its reason
		{"", "", "US-3 Corrigir o total do carrinho", ChangeBug},
		{"", "", "Refatorar o serviço de frete", ChangeRefactor},
		{"qualquer", "", "Adicionar cupom", ChangeFeature},
	}
	for _, c := range cases {
		if got, _ := changeKind(c.kind, c.reason, c.text); got != c.want {
			t.Errorf("changeKind(%q, %q, %q) = %q, want %q", c.kind, c.reason, c.text, got, c.want)
		}
	}
}

func TestJudgeReproductionNeedsABehaviorFailure(t *testing.T) {
	behavior := quality.Result{Name: "unit", ExitCode: 1, Failures: []quality.Failure{{Test: "TestTotal", Message: "want 10", Fingerprint: "f1"}}}
	compile := quality.Result{Name: "unit", ExitCode: 2, Failures: []quality.Failure{{File: "cart.go", Line: 3, Compile: true, Fingerprint: "c1"}}}
	timeout := quality.Result{Name: "unit", ExitCode: -1, TimedOut: true}
	if got, failures := judgeReproduction([]quality.Result{behavior}, nil); got != ReproDone || len(failures) != 1 {
		t.Errorf("behavior failure = %s %v", got, failures)
	}
	for name, r := range map[string]quality.Result{"compile": compile, "timeout": timeout} {
		if got, _ := judgeReproduction([]quality.Result{r}, nil); got != ReproUnclear {
			t.Errorf("%s = %s, want %s", name, got, ReproUnclear)
		}
	}
	// The failure was already there before the story: it shows nothing.
	before := &Baseline{Failing: map[string][]string{"unit": {"f1"}}}
	if got, _ := judgeReproduction([]quality.Result{behavior}, before); got != ReproUnclear {
		t.Errorf("preexisting failure = %s", got)
	}
	if got, _ := judgeReproduction([]quality.Result{{Name: "unit", Passed: true}}, nil); got != ReproNotDone {
		t.Errorf("passing tests = %s", got)
	}
}

// newBugTest is a git repository whose check fails, with a test failure,
// where cart_test.go exists without fix.txt; agents write what write says.
func newBugTest(t *testing.T, write func(dir string, call int)) (*Orchestrator, *fakeExecutor) {
	t.Helper()
	exec := &fakeExecutor{}
	o, _ := newParallelStoryTest(t, exec)
	o.baseStages = gatedStages
	o.stages = o.baseStages
	calls := 0
	exec.onCall = func(req provider.Request) {
		if req.Role == "ra" {
			calls++
			write(req.WorkDir, calls)
		}
	}
	o.runCheck = func(_ context.Context, dir string, c quality.Command, _ time.Duration) quality.Result {
		r := quality.Result{Name: c.Name, Command: c.Run, Passed: true}
		if exists(dir, "cart_test.go") && !exists(dir, "fix.txt") {
			r.Passed, r.ExitCode = false, 1
			r.Failures = []quality.Failure{{Test: "TestTotal", Message: "total errado", Fingerprint: "total"}}
		}
		return r
	}
	return o, exec
}

func bugStory() StartOptions {
	return StartOptions{Stories: []stories.Story{{ID: "US-9", Title: "US-9 Corrigir o total do carrinho", Content: "O total ignora o frete."}}, Source: "Markdown"}
}

func put(t *testing.T, dir, name string) { writeFile(t, dir, name, name+"\n") }

func TestBugFixMustCarryATestThatFailsBeforeTheFix(t *testing.T) {
	o, _ := newBugTest(t, func(dir string, _ int) {
		put(t, dir, "cart_test.go")
		put(t, dir, "fix.txt")
	})
	_ = o.Start(bugStory())
	run := waitDone(t, o)
	st := run.Stories[0]
	if st.Change != ChangeBug {
		t.Fatalf("change = %q", st.Change)
	}
	if st.Reproduction == nil || st.Reproduction.Status != ReproDone || st.Reproduction.Commit == "" {
		t.Fatalf("reproduction = %+v, logs %+v", st.Reproduction, run.Logs)
	}
	if !strings.Contains(strings.Join(st.Reproduction.Failures, " "), "TestTotal") {
		t.Errorf("failures = %v", st.Reproduction.Failures)
	}
	if st.Validation != ValidationPassed {
		t.Errorf("validation = %s", st.Validation)
	}
}

func TestBugFixWithoutAReproducingTestGoesBack(t *testing.T) {
	o, _ := newBugTest(t, func(dir string, call int) {
		put(t, dir, "fix.txt")
		if call > 1 {
			put(t, dir, "cart_test.go")
		}
	})
	_ = o.Start(bugStory())
	run := waitDone(t, o)
	st := run.Stories[0]
	first := st.Steps[stepIndex(st.Steps, "checks")]
	if len(first.Issues) != 1 || !strings.Contains(first.Issues[0].Description, "sem teste que reproduza") {
		t.Fatalf("first verification issues = %+v", first.Issues)
	}
	if st.Reproduction == nil || st.Reproduction.Status != ReproDone || st.Validation != ValidationPassed {
		t.Errorf("after the test was written: %+v, validation %s", st.Reproduction, st.Validation)
	}
}

func TestTestThatAlsoPassesBeforeTheFixDoesNotReproduce(t *testing.T) {
	o, _ := newBugTest(t, func(dir string, _ int) {
		put(t, dir, "fix.txt")
		put(t, dir, "other_test.go") // passes with or without the fix
	})
	_ = o.Start(bugStory())
	run := waitDone(t, o)
	st := run.Stories[0]
	first := st.Steps[stepIndex(st.Steps, "checks")]
	if len(first.Issues) != 1 || !strings.Contains(first.Issues[0].Description, "não reproduzem o bug") {
		t.Fatalf("first verification issues = %+v", first.Issues)
	}
	if st.Validation == ValidationPassed {
		t.Errorf("a fix whose test never failed must not pass: %s", st.Validation)
	}
}

func TestRefactoringKeepsTheCharacterizationTests(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	policy := o.ws.Policy()
	policy.SkipBaseline = false
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "tests/cart.test.mjs", "test('UT-CART-01 total', () => {})\n")
	edits := 0
	exec.onCall = func(req provider.Request) {
		if req.Role != "fe" {
			return
		}
		edits++
		writeFile(t, repo, "src/cart.js", strings.Repeat("x", edits))
		if edits == 1 {
			os.Remove(filepath.Join(repo, "tests/cart.test.mjs"))
		} else {
			writeFile(t, repo, "tests/cart.test.mjs", "test('UT-CART-01 total, reescrito', () => {})\n")
		}
	}
	_ = o.Start(realStory("Refatorar o cálculo do carrinho"))
	run := waitDone(t, o)
	st := run.Stories[0]
	if st.Change != ChangeRefactor {
		t.Fatalf("change = %q", st.Change)
	}
	first := st.Steps[stepIndex(st.Steps, "checks")]
	if len(first.Issues) != 1 || !strings.Contains(first.Issues[0].Description, "caracterização") ||
		!strings.Contains(first.Issues[0].Description, "UT-CART-01") {
		t.Fatalf("first verification issues = %+v", first.Issues)
	}
	if st.Validation != ValidationPassed {
		t.Errorf("restored tests must pass: %s", st.Validation)
	}
}

func TestFeatureThatChangesCodeNeedsATest(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	writeFile(t, repo, "tests/a.test.mjs", "test('a', () => {})\n")
	edits := 0
	exec.onCall = func(req provider.Request) {
		if req.Role != "fe" {
			return
		}
		if edits++; edits == 1 {
			writeFile(t, repo, "src/cupom.js", "export const cupom = 1\n")
			return
		}
		writeFile(t, repo, "tests/cupom.test.mjs", "test('cupom', () => {})\n")
	}
	_ = o.Start(realStory("Adicionar cupom de desconto"))
	run := waitDone(t, o)
	st := run.Stories[0]
	first := st.Steps[stepIndex(st.Steps, "checks")]
	if len(first.Issues) != 1 || !strings.Contains(first.Issues[0].Description, "sem teste") {
		t.Fatalf("first verification issues = %+v", first.Issues)
	}
	if st.Validation != ValidationPassed {
		t.Errorf("validation = %s", st.Validation)
	}
}

func TestTrivialChangeWithReasonNeedsNoNewTest(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"ctx": {"ok\n```json\n{\"decision\":\"extend\",\"change_type\":\"trivial\",\"change_reason\":\"só o texto do botão\"}\n```"},
	}}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	o.baseStages = append([]Stage{{ID: "context", Title: "Passo 0", Role: "ctx", Assessor: true}}, verifierStages...)
	o.stages = o.baseStages
	writeFile(t, repo, "tests/a.test.mjs", "test('a', () => {})\n")
	exec.onCall = func(req provider.Request) {
		if req.Role == "fe" {
			writeFile(t, repo, "src/botao.js", "export const label = 'Pagar'\n")
		}
	}
	_ = o.Start(realStory("Adicionar rótulo"))
	run := waitDone(t, o)
	st := run.Stories[0]
	if st.Change != ChangeTrivial || st.ChangeReason == "" {
		t.Fatalf("change = %q (%q)", st.Change, st.ChangeReason)
	}
	if checks := st.Steps[stepIndex(st.Steps, "checks")]; len(checks.Issues) != 0 {
		t.Errorf("a trivial change asked for tests: %+v", checks.Issues)
	}
}

func TestFinalApprovalRunsEveryCommandAfterAFastRound(t *testing.T) {
	exec := &fakeExecutor{}
	ran := map[int][]string{}
	round := 0
	o, repo := newVerifierTest(t, exec, nil)
	policy := o.ws.Policy()
	policy.Checks = []quality.Command{{Name: "lint", Run: "npm run lint"}, {Name: "unit", Run: "npm test"}}
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	exec.onCall = func(req provider.Request) {
		if req.Role == "fe" {
			round++
			writeFile(t, repo, "src/cart.txt", strings.Repeat("x", round))
		}
	}
	o.runCheck = func(_ context.Context, _ string, c quality.Command, _ time.Duration) quality.Result {
		ran[round] = append(ran[round], c.Name)
		return quality.Result{Name: c.Name, Command: c.Run, Passed: c.Name != "unit" || round > 1, ExitCode: 1, Output: "FAIL x"}
	}
	_ = o.Start(realStory("Carrinho"))
	run := waitDone(t, o)
	if run.Stories[0].Validation != ValidationPassed {
		t.Fatalf("validation = %s, logs %+v", run.Stories[0].Validation, run.Logs)
	}
	// The fix round tries the failing command first, then the rest: the
	// approval still rests on every command.
	if got := strings.Join(ran[2], ","); got != "unit,lint" {
		t.Errorf("after the fix ran %q, want unit then lint", got)
	}
}
