package pipeline

import (
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
)

const tracedDoc = "Carrinho\n\n## Plano de testes\n| ID | Caso |\n|---|---|\n| UT-CART-01 | adiciona item |\n| UT-CART-02 | remove item |\n"

func reported(cases ...quality.Case) quality.Result {
	return quality.Result{Passed: true, Report: &quality.ReportOutcome{Format: quality.ReportGoJSON, Status: quality.ReportRead, Passed: len(cases)}, Cases: cases}
}

func TestRequiredTestOnlySkippedGoesBackToTheImplementer(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newVerifierTest(t, exec, func(n int) quality.Result {
		if n == 1 {
			return reported(quality.Case{Name: "TestCart/UT-CART-01", Status: quality.CasePassed}, quality.Case{Name: "TestCart/UT-CART-02", Status: quality.CaseSkipped})
		}
		return reported(quality.Case{Name: "TestCart/UT-CART-01", Status: quality.CasePassed}, quality.Case{Name: "TestCart/UT-CART-02", Status: quality.CasePassed})
	})
	exec.onCall = func(req provider.Request) {
		if req.Role == "fe" {
			writeFile(t, repo, "cart/cart_test.go", "// UT-CART-01 UT-CART-02\nfunc TestCart(t *testing.T) {}\n"+strings.Repeat(" ", exec.count("fe")))
		}
	}
	_ = o.Start(realStory(tracedDoc))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	first := run.Stories[0].Steps[2]
	if len(first.Issues) != 1 || !strings.Contains(first.Issues[0].Description, "ignorados") || !strings.Contains(first.Issues[0].Description, "UT-CART-02") {
		t.Fatalf("first verification issues = %+v", first.Issues)
	}
	for _, c := range first.Checks {
		if len(c.Cases) > 0 {
			t.Errorf("the step keeps %d cases; only the counts belong there", len(c.Cases))
		}
	}
	for _, tr := range run.Stories[0].Trace {
		if tr.Status != quality.TraceProven || len(tr.Cases) != 1 {
			t.Errorf("%s = %s with %+v, want proven by its case", tr.ID, tr.Status, tr.Cases)
		}
	}
}

func TestPassingCommandsWithoutReportDoNotProveTheCriteria(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	exec.onCall = func(req provider.Request) {
		if req.Role == "fe" {
			writeFile(t, repo, "tests/cart.test.mjs", "test('UT-CART-01', () => {})\ntest('UT-CART-02', () => {})\n")
		}
	}
	_ = o.Start(realStory(tracedDoc))
	run := waitDone(t, o)
	for _, tr := range run.Stories[0].Trace {
		if tr.Status != quality.TraceExitOnly || tr.Proven() {
			t.Errorf("%s = %s, want %q", tr.ID, tr.Status, quality.TraceExitOnly)
		}
	}
	if out := run.Stories[0].Steps[2].Output; !strings.Contains(out, "0 comprovados por execução") || !strings.Contains(out, quality.TraceExitOnly) {
		t.Errorf("report does not say the criteria are unproven:\n%s", out)
	}
}
