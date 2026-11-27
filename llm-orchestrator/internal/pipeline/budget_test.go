package pipeline

import (
	"errors"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/workspace"
)

func budgetTest(t *testing.T, exec *fakeExecutor, mutate func(*workspace.Policy)) Run {
	t.Helper()
	o, _ := newTest(t, exec)
	policy := o.ws.Policy()
	mutate(&policy)
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := o.Start(one(false)); err != nil {
		t.Fatal(err)
	}
	return waitDone(t, o)
}

func TestBudgetStopsNewCallsAndKeepsTheWork(t *testing.T) {
	exec := &fakeExecutor{usage: &provider.Usage{InputTokens: 600, OutputTokens: 100}}
	run := budgetTest(t, exec, func(p *workspace.Policy) { p.Budget.MaxTokens = 1000 })
	if run.Result != ResultFailed {
		t.Fatalf("result = %s", run.Result)
	}
	// discovery (700) and A (1400 > 1000) ran; B never started.
	if exec.count("rb") != 0 || exec.count("ra") != 1 {
		t.Errorf("calls after the budget: roles %v", exec.roles)
	}
	b := step(run.Stories[0].Steps, "b")
	if b.FailureClass != FailureBudget || !strings.Contains(b.Error, "tokens") {
		t.Errorf("step b = %s %q %q", b.Status, b.FailureClass, b.Error)
	}
	if a := step(run.Stories[0].Steps, "a"); a.Status != StepDone {
		t.Errorf("the work before the budget must stay: a = %s", a.Status)
	}
	if run.Usage == nil || run.Usage.Tokens() != 1400 || !strings.Contains(run.Usage.StoppedBy, "tokens") {
		t.Errorf("usage = %+v", run.Usage)
	}
	if run.Resume == nil || run.Resume.Step != "b" {
		t.Errorf("the run resumes where the budget stopped it: %+v", run.Resume)
	}
}

func TestBudgetBoundsCalls(t *testing.T) {
	exec := &fakeExecutor{}
	run := budgetTest(t, exec, func(p *workspace.Policy) { p.Budget.MaxCalls = 2 })
	if got := len(exec.roles); got != 2 || run.Result != ResultFailed {
		t.Errorf("calls = %d (%v), result %s", got, exec.roles, run.Result)
	}
}

func TestRetriesHaveTheirOwnBudget(t *testing.T) {
	exec := &fakeExecutor{failIf: func(req provider.Request) bool { return req.Role == "ra" }}
	o, _ := newTest(t, exec)
	o.attempts, o.retryDelay = 3, time.Millisecond
	policy := o.ws.Policy()
	policy.Budget.MaxRetries = 1
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	_ = o.Start(one(false))
	run := waitDone(t, o)
	if exec.count("ra") != 2 || run.Usage.Retries != 1 {
		t.Errorf("A ran %d time(s), retries %d; want the first try and one retry", exec.count("ra"), run.Usage.Retries)
	}
	if a := step(run.Stories[0].Steps, "a"); a.FailureClass != FailureInfra {
		t.Errorf("failure class = %q", a.FailureClass)
	}
}

func TestCostIsObservedEstimatedOrUnknown(t *testing.T) {
	o, _ := newTest(t, &fakeExecutor{})
	prices := map[string]workspace.Price{"claude-sonnet": {Input: 3, Output: 15}}
	policy := workspace.Policy{Prices: prices, PricesVersion: "tabela-2026-11"}
	o.account(&provider.Response{Model: "claude-sonnet-5-5", Usage: &provider.Usage{InputTokens: 1_000_000, OutputTokens: 100_000}}, policy)
	o.account(&provider.Response{Model: "x", Usage: &provider.Usage{InputTokens: 10, CostUSD: 0.25}}, policy)
	o.account(&provider.Response{Model: "sem-preço", Usage: &provider.Usage{InputTokens: 10}}, policy)
	o.account(&provider.Response{Model: "m"}, policy)
	u := o.Snapshot().Usage
	if u.CostObservedUSD != 0.25 || u.CostEstimatedUSD != 4.5 || u.EstimatedCalls != 1 || u.PricesVersion != "tabela-2026-11" {
		t.Errorf("costs = %+v", u)
	}
	if u.UnknownCost != 2 || u.Unreported != 1 || u.Calls != 4 {
		t.Errorf("unknown = %d, unreported = %d, calls = %d", u.UnknownCost, u.Unreported, u.Calls)
	}
}

func TestFailureClasses(t *testing.T) {
	cases := map[error]string{
		ErrBudgetExceeded:             FailureBudget,
		provider.ErrNoCapableProvider: FailureProvider,
		errors.New("exit status 1"):   FailureInfra,
	}
	for err, want := range cases {
		if got := classifyFailure(err); got != want {
			t.Errorf("%v = %s, want %s", err, got, want)
		}
	}
	if retryable(ErrBudgetExceeded) || !retryable(errors.New("boom")) {
		t.Error("a spent budget is not retried; a crash is")
	}
}
