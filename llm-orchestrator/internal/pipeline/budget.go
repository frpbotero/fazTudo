package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/workspace"
)

// ErrBudgetExceeded stops new agent calls once a run's budget is spent.
var ErrBudgetExceeded = errors.New("orçamento da execução atingido")

// RunUsage is what a run spent, as observed. Tokens and cost come from the
// CLIs; a call that reported none is counted apart, never as zero. The
// estimated cost, from the policy's prices, is kept apart from the observed.
type RunUsage struct {
	Calls      int `json:"calls"`
	Retries    int `json:"retries,omitempty"`
	Unreported int `json:"unreported,omitempty"` // calls with no token count
	provider.Usage
	// CostObservedUSD is what the CLIs reported; CostEstimatedUSD what the
	// price table gives for the calls that reported tokens but no cost.
	CostObservedUSD  float64 `json:"cost_observed_usd,omitempty"`
	CostEstimatedUSD float64 `json:"cost_estimated_usd,omitempty"`
	EstimatedCalls   int     `json:"estimated_calls,omitempty"`
	// UnknownCost counts calls whose cost is unknown: no cost reported and
	// no price for their model.
	UnknownCost   int    `json:"unknown_cost,omitempty"`
	PricesVersion string `json:"prices_version,omitempty"`
	// StoppedBy says which bound stopped the run, when one did.
	StoppedBy string `json:"stopped_by,omitempty"`
}

// Tokens is every token the CLIs reported.
func (u RunUsage) Tokens() int64 {
	return u.InputTokens + u.CachedTokens + u.OutputTokens + u.ReasoningTokens
}

// account adds a finished call to the run's usage and to its story's.
func (o *Orchestrator) account(story int, resp *provider.Response, policy workspace.Policy) {
	if resp == nil || resp.Simulated {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	// Copy on write: snapshots share the pointers and are read unlocked.
	o.run.Usage = withUsage(o.run.Usage, func(u *RunUsage) { addUsage(u, resp, policy) })
	if story >= 0 && story < len(o.run.Stories) {
		o.run.Stories[story].Usage = withUsage(o.run.Stories[story].Usage, func(u *RunUsage) { addUsage(u, resp, policy) })
	}
}

// withUsage returns a changed copy of u, which is never written in place.
func withUsage(u *RunUsage, change func(*RunUsage)) *RunUsage {
	next := RunUsage{}
	if u != nil {
		next = *u
	}
	change(&next)
	return &next
}

func addUsage(u *RunUsage, resp *provider.Response, policy workspace.Policy) {
	u.Calls++
	if resp.Usage == nil {
		u.Unreported++
		u.UnknownCost++
		return
	}
	r := *resp.Usage
	u.InputTokens += r.InputTokens
	u.CachedTokens += r.CachedTokens
	u.OutputTokens += r.OutputTokens
	u.ReasoningTokens += r.ReasoningTokens
	u.Turns += r.Turns
	switch price, ok := priceFor(policy.Prices, resp.Model); {
	case r.CostUSD > 0:
		u.CostObservedUSD += r.CostUSD
	case ok:
		u.CostEstimatedUSD += (float64(r.InputTokens)*price.Input + float64(r.CachedTokens)*price.CachedInput +
			float64(r.OutputTokens+r.ReasoningTokens)*price.Output) / 1e6
		u.EstimatedCalls++
		u.PricesVersion = policy.PricesVersion
	default:
		u.UnknownCost++
	}
}

// priceFor finds the model's price: an exact name, else the longest prefix.
func priceFor(prices map[string]workspace.Price, model string) (workspace.Price, bool) {
	if p, ok := prices[model]; ok {
		return p, true
	}
	best, found := "", false
	var price workspace.Price
	for name, p := range prices {
		if strings.HasPrefix(model, name) && len(name) > len(best) {
			best, price, found = name, p, true
		}
	}
	return price, found
}

// spend checks the budget before a new call. It returns ErrBudgetExceeded,
// wrapped with the bound, when one is reached.
func (o *Orchestrator) spend(policy workspace.Policy) error {
	b := policy.Budget
	o.mu.Lock()
	defer o.mu.Unlock()
	u := o.run.Usage
	if u == nil {
		u = &RunUsage{}
	}
	var why string
	switch {
	case b.MaxCalls > 0 && u.Calls >= b.MaxCalls:
		why = fmt.Sprintf("%d chamadas de agente (limite %d)", u.Calls, b.MaxCalls)
	case b.MaxTokens > 0 && u.Tokens() >= b.MaxTokens:
		why = fmt.Sprintf("%d tokens informados (limite %d)", u.Tokens(), b.MaxTokens)
	case b.MaxMinutes > 0 && o.run.StartedAt != nil && time.Since(*o.run.StartedAt) >= time.Duration(b.MaxMinutes)*time.Minute:
		why = fmt.Sprintf("%s de execução (limite %d min)", time.Since(*o.run.StartedAt).Round(time.Second), b.MaxMinutes)
	}
	if why == "" {
		return nil
	}
	o.run.Usage = withUsage(o.run.Usage, func(u *RunUsage) { u.StoppedBy = why })
	return fmt.Errorf("%w: %s; o trabalho feito fica salvo. Aumente o orçamento e continue de onde parou", ErrBudgetExceeded, why)
}

// mayRetry tells whether a failed call may be retried within the budget,
// counting the retry, and how long to wait first.
func (o *Orchestrator) mayRetry(policy workspace.Policy, attempt int) (time.Duration, bool) {
	b := policy.Budget
	o.mu.Lock()
	defer o.mu.Unlock()
	if b.MaxRetries > 0 && o.run.Usage != nil && o.run.Usage.Retries >= b.MaxRetries {
		return 0, false
	}
	o.run.Usage = withUsage(o.run.Usage, func(u *RunUsage) { u.Retries++ })
	wait := o.retryDelay * time.Duration(attempt)
	if b.MaxRetryWaitS > 0 {
		wait = min(wait, time.Duration(b.MaxRetryWaitS)*time.Second)
	}
	return wait, true
}

// Failure classes say what a stopped or unsettled step ran into, so retries
// and reports treat each one for what it is.
const (
	FailureBudget   = "orçamento"
	FailureProvider = "provider"       // no provider able or available
	FailureInfra    = "infraestrutura" // the CLI, the machine, a time-out
	FailureFormat   = "formato"        // no valid answer in the contract
	FailureBehavior = "comportamento"  // the code does not do what is required
	FailureCanceled = "cancelada"
)

// classifyFailure names the class of an agent call's error.
func classifyFailure(err error) string {
	switch {
	case errors.Is(err, ErrBudgetExceeded):
		return FailureBudget
	case errors.Is(err, provider.ErrNoCapableProvider) || errors.Is(err, provider.ErrNoProvider):
		return FailureProvider
	case errors.Is(err, context.Canceled):
		return FailureCanceled
	}
	return FailureInfra
}
