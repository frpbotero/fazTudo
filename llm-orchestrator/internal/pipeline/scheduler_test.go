package pipeline

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

func TestFairSlotsServeInArrivalOrder(t *testing.T) {
	f := newFairSlots(1)
	ctx := context.Background()
	if _, err := f.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.acquire(ctx); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			f.release()
		}()
		// Each waiter queues before the next one starts.
		for deadline := time.Now().Add(time.Second); ; {
			f.mu.Lock()
			n := len(f.waiting)
			f.mu.Unlock()
			if n == i+1 || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	f.release()
	wg.Wait()
	for i, got := range order {
		if got != i {
			t.Fatalf("served %v, want arrival order", order)
		}
	}

	// A waiter that gives up leaves the line; the slot is not lost.
	f = newFairSlots(1)
	_, _ = f.acquire(ctx)
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := f.acquire(cctx); done <- err }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a canceled wait must fail")
	}
	f.release()
	if _, err := f.acquire(ctx); err != nil {
		t.Errorf("the slot was lost after a canceled wait: %v", err)
	}
}

func TestRunBoundsItsAgentCallsAndRecordsTheWait(t *testing.T) {
	var running, peak atomic.Int32
	exec := &fakeExecutor{onCall: func(req provider.Request) {
		n := running.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(15 * time.Millisecond)
		running.Add(-1)
	}}
	policy := workspace.DefaultPolicy()
	policy.ParallelLenses = true
	policy.MaxParallelReviews = 5
	policy.MaxParallelAgents = 1
	run, _ := runWithFlowExec(t, policy, exec)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}
	if p := peak.Load(); p != 1 {
		t.Errorf("peak concurrent agent calls = %d, want 1", p)
	}
	review := step(run.Stories[0].Steps, "review")
	if review.QueueMS == 0 {
		t.Error("the reviewers waited for the single slot, but no queue time was recorded")
	}
}

func TestCommandsSharingAResourceHoldItsLock(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	var held []string
	o.lockResource = func(_ context.Context, name string, _ func()) (func(), error) {
		held = append(held, "+"+name)
		return func() { held = append(held, "-"+name) }, nil
	}
	policy := o.ws.Policy()
	policy.Checks = []quality.Command{{Name: "db", Run: "make test-db", Resource: "postgres"}, {Name: "unit", Run: "npm test"}}
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	_ = o.Start(realStory("Carrinho"))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}
	if got := strings.Join(held, " "); !strings.HasPrefix(got, "+postgres -postgres") || strings.Count(got, "+") != strings.Count(got, "-") {
		t.Errorf("resource lock = %q", got)
	}
}
