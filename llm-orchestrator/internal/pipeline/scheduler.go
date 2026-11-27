package pipeline

import (
	"context"
	"sync"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/workspace"
)

// defaultParallelAgents bounds a run's agent calls when the policy sets none.
const defaultParallelAgents = 4

// fairSlots is a semaphore that serves waiters in arrival order: a call that
// waits is never overtaken forever by later ones, whatever spawned them
// (stories, fronts, reviewers, candidates).
type fairSlots struct {
	mu      sync.Mutex
	free    int
	waiting []chan struct{}
}

func newFairSlots(n int) *fairSlots { return &fairSlots{free: max(n, 1)} }

// acquire takes a slot, waiting in line; it returns how long it waited, or
// ctx's error with the place given up.
func (f *fairSlots) acquire(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	f.mu.Lock()
	if f.free > 0 && len(f.waiting) == 0 {
		f.free--
		f.mu.Unlock()
		return 0, nil
	}
	ch := make(chan struct{})
	f.waiting = append(f.waiting, ch)
	f.mu.Unlock()
	select {
	case <-ch:
		return time.Since(start), nil
	case <-ctx.Done():
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, w := range f.waiting {
			if w == ch {
				f.waiting = append(f.waiting[:i], f.waiting[i+1:]...)
				return time.Since(start), ctx.Err()
			}
		}
		// Granted just as ctx ended: hand the slot on.
		f.releaseLocked()
		return time.Since(start), ctx.Err()
	}
}

func (f *fairSlots) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseLocked()
}

func (f *fairSlots) releaseLocked() {
	if len(f.waiting) > 0 {
		next := f.waiting[0]
		f.waiting = f.waiting[1:]
		close(next)
		return
	}
	f.free++
}

// useSlots sizes the run's agent slots from its policy.
func (o *Orchestrator) useSlots(p workspace.Policy) {
	n := p.MaxParallelAgents
	if n <= 0 {
		n = defaultParallelAgents
	}
	o.mu.Lock()
	o.agentSlots = newFairSlots(n)
	o.mu.Unlock()
}

// callAgent is the one door to the providers: it waits for a slot, keeps the
// time waited on the step, and records the call's usage.
func (o *Orchestrator) callAgent(ctx context.Context, ref stepRef, req provider.Request, attempt int) (*provider.Response, error) {
	policy := o.ws.Policy()
	if err := o.spend(policy); err != nil {
		return nil, err
	}
	o.mu.RLock()
	slots := o.agentSlots
	o.mu.RUnlock()
	if slots != nil {
		waited, err := slots.acquire(ctx)
		if waited > 0 {
			o.updateStep(ref, func(s *Step) { s.QueueMS += waited.Milliseconds() })
		}
		if err != nil {
			return nil, err
		}
		defer slots.release()
	}
	resp, err := o.exec.Execute(ctx, req, o.providerLog)
	o.recordUsage(ref, req, resp, attempt)
	o.account(ref.story, resp, policy)
	return resp, err
}

// retryable tells failures worth another attempt: not a spent budget nor a
// missing provider, which a retry cannot change.
func retryable(err error) bool {
	switch classifyFailure(err) {
	case FailureBudget, FailureProvider, FailureCanceled:
		return false
	}
	return true
}
