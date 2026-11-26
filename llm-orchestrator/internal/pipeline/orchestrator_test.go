package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

type fakeExecutor struct {
	mu      sync.Mutex
	dry     bool
	prompts []string
	reqs    []provider.Request
	roles   []string
	dirs    []string
	outputs map[string][]string             // scripted outputs per role, consumed in order
	failAt  int                             // 1-based call index that fails; 0 = never
	block   chan struct{}                   // when set, every call waits on it or ctx
	onCall  func(req provider.Request)      // side effects in the repository, like a real agent
	failIf  func(req provider.Request) bool // fails the call after its side effects
}

func (f *fakeExecutor) Execute(ctx context.Context, req provider.Request, logf provider.Logf) (*provider.Response, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, req.Prompt)
	f.reqs = append(f.reqs, req)
	f.roles = append(f.roles, req.Role)
	f.dirs = append(f.dirs, req.WorkDir)
	n := len(f.prompts)
	f.mu.Unlock()

	if f.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.block:
		}
	}
	if n == f.failAt {
		return nil, errors.New("boom")
	}
	if f.onCall != nil {
		f.onCall(req)
	}
	if f.failIf != nil && f.failIf(req) {
		return nil, errors.New("boom")
	}
	f.mu.Lock()
	// Unscripted calls answer like an agent that approves: gates need a
	// valid verdict, and other stages ignore a block that is not theirs.
	out := "artifact-" + req.Role + "\n\n" + fence(approved)
	if q := f.outputs[req.Role]; len(q) > 0 {
		out, f.outputs[req.Role] = q[0], q[1:]
	}
	f.mu.Unlock()
	return &provider.Response{Output: out, Name: "Fake", Model: "m1"}, nil
}

func (f *fakeExecutor) SetDryRun(v bool) { f.dry = v }

func (f *fakeExecutor) count(role string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.roles {
		if r == role {
			n++
		}
	}
	return n
}

var testStages = []Stage{
	{ID: "a", Title: "A", Role: "ra", Task: "do a"},
	{ID: "b", Title: "B", Role: "rb", Task: "do b"},
	{ID: "c", Title: "C", Role: "rc", Task: "do c"},
}

func newTest(t *testing.T, exec *fakeExecutor) (*Orchestrator, string) {
	t.Helper()
	repo := t.TempDir()
	ws, err := workspace.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	o := New(exec, ws, testStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.attempts = 1 // failAt scripts a hard failure; retries have their own test
	return o, repo
}

func one(dry bool) StartOptions {
	return StartOptions{Stories: []stories.Story{{ID: "US-1", Title: "US-1 X", Content: "story X"}}, Source: "Markdown", DryRun: dry}
}

// waitDone allows a shared CI runner's slowness: these runs finish in tens
// of milliseconds, and the limit only matters when one hangs.
func waitDone(t *testing.T, o *Orchestrator) Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.Wait(ctx); err != nil {
		t.Fatal("run did not finish:", err)
	}
	return o.Snapshot()
}

func TestRunSucceedsAndChainsArtifacts(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newTest(t, exec)
	if err := o.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)

	if run.Result != ResultSucceeded || run.Running || !run.DryRun {
		t.Fatalf("unexpected run: %+v", run)
	}
	if run.Discovery.Status != StepDone || run.Stories[0].Status != StepDone {
		t.Fatalf("discovery %s, story %s", run.Discovery.Status, run.Stories[0].Status)
	}
	for _, s := range run.Stories[0].Steps {
		if s.Status != StepDone || s.Output == "" {
			t.Errorf("step %s: %s", s.ID, s.Status)
		}
	}
	last := exec.prompts[len(exec.prompts)-1]
	for _, want := range []string{"story X", "artifact-ra", "artifact-rb", "artifact-" + DiscoveryStage.Role, repo} {
		if !strings.Contains(last, want) {
			t.Errorf("last stage prompt missing %q:\n%s", want, last)
		}
	}
	if strings.Contains(last, "Sequência de desenvolvimento") {
		t.Error("a single story must not mention a sequence")
	}
	for _, d := range exec.dirs {
		if d != repo {
			t.Errorf("agents must run inside the repository, got WorkDir %q", d)
		}
	}
}

func TestSequenceDevelopsStoriesInOrder(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newTest(t, exec)
	err := o.Start(StartOptions{
		Preamble: "Épico de pagamentos",
		Stories: []stories.Story{
			{ID: "US-1", Title: "US-1 Checkout", Content: "conteúdo 1"},
			{ID: "US-2", Title: "US-2 Estorno", Content: "conteúdo 2"},
		},
		Source: "Markdown", DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)

	if run.Result != ResultSucceeded || run.Current != 1 {
		t.Fatalf("result=%s current=%d", run.Result, run.Current)
	}
	if exec.count(DiscoveryStage.Role) != 1 {
		t.Error("discovery must run once per sequence")
	}
	if want := 2 + 2*len(testStages); len(exec.prompts) != want { // discovery, analysis, stages
		t.Fatalf("calls = %d, want %d", len(exec.prompts), want)
	}
	if run.Analysis == nil || run.Analysis.Status != StepDone || run.Stories[1].Wave != 2 {
		t.Errorf("an analysis without JSON keeps the order: %+v, waves %v", run.Analysis, run.Waves)
	}

	second := exec.prompts[2+len(testStages)] // first stage of US-2
	for _, want := range []string{"US 2 de 2", "- US-1 Checkout", "conteúdo 2", "Épico de pagamentos"} {
		if !strings.Contains(second, want) {
			t.Errorf("US-2 prompt missing %q:\n%s", want, second)
		}
	}
	if strings.Contains(second, "conteúdo 1") || strings.Contains(second, "artifact-rc") {
		t.Error("US-2 must not carry US-1 content or artifacts, only its title")
	}
}

func TestSequenceStopsOnFailure(t *testing.T) {
	// call 1 = discovery, 2 = analysis, 3..5 = US-1, 6 = first stage of US-2 fails
	o, _ := newTest(t, &fakeExecutor{failAt: 6})
	_ = o.Start(StartOptions{Stories: []stories.Story{{Title: "1"}, {Title: "2"}, {Title: "3"}}, DryRun: true})
	run := waitDone(t, o)

	want := []StepStatus{StepDone, StepFailed, StepCanceled}
	for i, s := range run.Stories {
		if s.Status != want[i] {
			t.Errorf("story %d = %s, want %s", i, s.Status, want[i])
		}
	}
	if st := run.Stories[1].Steps; st[0].Status != StepFailed || st[1].Status != StepCanceled || st[0].Error == "" {
		t.Errorf("unexpected US-2 steps: %+v", st)
	}
	if run.Result != ResultFailed {
		t.Errorf("result = %s", run.Result)
	}
}

func TestRetryFailedContinuesFromFailedStage(t *testing.T) {
	exec := &fakeExecutor{failAt: 3} // discovery, A, then B fails
	o, _ := newTest(t, exec)
	if err := o.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	failed := waitDone(t, o)
	if failed.Result != ResultFailed || failed.Stories[0].Steps[1].Status != StepFailed {
		t.Fatalf("expected B to fail: %+v", failed.Stories[0].Steps)
	}

	if err := o.RetryFailed(0, "b"); err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, o)
	if done.Result != ResultSucceeded || done.Stories[0].Status != StepDone {
		t.Fatalf("retry result=%s story=%s", done.Result, done.Stories[0].Status)
	}
	if exec.count("ra") != 1 || exec.count("rb") != 2 || exec.count("rc") != 1 {
		t.Fatalf("retry reran the wrong stages: roles=%v", exec.roles)
	}
	for _, s := range done.Stories[0].Steps {
		if s.Status != StepDone {
			t.Errorf("step %s = %s", s.ID, s.Status)
		}
	}
}

func TestFailingAgentIsRetried(t *testing.T) {
	exec := &fakeExecutor{failAt: 3} // discovery, A, then B fails once
	o, _ := newTest(t, exec)
	o.attempts, o.retryDelay = 3, time.Millisecond
	if err := o.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if run.Result != ResultSucceeded || exec.count("rb") != 2 {
		t.Fatalf("result = %s, B calls = %d; want succeeded after one retry", run.Result, exec.count("rb"))
	}
	if !strings.Contains(exec.prompts[3], "[RETOMADA]") {
		t.Errorf("retried prompt must ask the agent to resume:\n%s", exec.prompts[3])
	}
}

func TestRetryRejectsNonFailedStep(t *testing.T) {
	o, _ := newTest(t, &fakeExecutor{})
	if err := o.RetryFailed(0, "a"); !errors.Is(err, ErrNoFailedStep) {
		t.Fatalf("retry without a failure = %v", err)
	}
}

func TestRetryFailedDiscoveryStartsCleanRun(t *testing.T) {
	exec := &fakeExecutor{failAt: 1}
	o, _ := newTest(t, exec)
	if err := o.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	if failed := waitDone(t, o); failed.Discovery.Status != StepFailed {
		t.Fatalf("discovery = %s", failed.Discovery.Status)
	}
	if err := o.RetryFailed(-1, DiscoveryStage.ID); err != nil {
		t.Fatal(err)
	}
	if done := waitDone(t, o); done.Result != ResultSucceeded {
		t.Fatalf("retry result = %s", done.Result)
	}
}

func TestStopCancelsRun(t *testing.T) {
	exec := &fakeExecutor{block: make(chan struct{})}
	o, _ := newTest(t, exec)
	_ = o.Start(one(true))

	if err := o.Start(one(false)); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second start: %v", err)
	}
	if !exec.dry {
		t.Error("a rejected start must not change the simulation mode")
	}
	if _, err := o.SetRepository(t.TempDir()); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("changing repository during a run: %v", err)
	}
	if !o.Stop() {
		t.Fatal("Stop should report an active run")
	}
	run := waitDone(t, o)
	if run.Result != ResultCanceled || run.Discovery.Status != StepCanceled || run.Stories[0].Status != StepCanceled {
		t.Fatalf("unexpected run after stop: result=%s discovery=%s story=%s", run.Result, run.Discovery.Status, run.Stories[0].Status)
	}
	for _, s := range run.Stories[0].Steps {
		if s.Status != StepCanceled {
			t.Errorf("step %s = %s, want canceled", s.ID, s.Status)
		}
	}
	if o.Stop() {
		t.Error("Stop on idle orchestrator should return false")
	}
}

func TestStartValidation(t *testing.T) {
	o, _ := newTest(t, &fakeExecutor{})
	if err := o.Start(StartOptions{}); !errors.Is(err, ErrNoStories) {
		t.Errorf("err = %v, want ErrNoStories", err)
	}

	ws, _ := workspace.NewManager("")
	noRepo := New(&fakeExecutor{}, ws, testStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := noRepo.Start(one(true)); !errors.Is(err, workspace.ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}

func TestLogsAreCapped(t *testing.T) {
	o, _ := newTest(t, &fakeExecutor{})
	for range maxLogs + 50 {
		o.logf(LevelInfo, "x")
	}
	if n := len(o.Snapshot().Logs); n != maxLogs {
		t.Fatalf("logs = %d, want %d", n, maxLogs)
	}
}

func TestDiscoveryRunsOncePerSession(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newTest(t, exec)

	_ = o.Start(one(true))
	waitDone(t, o)
	_ = o.Start(one(true))
	run := waitDone(t, o)

	if n := exec.count(DiscoveryStage.Role); n != 1 {
		t.Fatalf("discovery ran %d times, want 1", n)
	}
	if run.Discovery.Status != StepSkipped || run.Discovery.Note == "" || run.Discovery.Output == "" {
		t.Errorf("second run should reuse discovery, got %+v", run.Discovery)
	}
	if run.Result != ResultSucceeded {
		t.Errorf("result = %s", run.Result)
	}

	opts := one(true)
	opts.Rediscover = true
	_ = o.Start(opts)
	waitDone(t, o)
	if n := exec.count(DiscoveryStage.Role); n != 2 {
		t.Errorf("Rediscover must force a new discovery, ran %d times", n)
	}

	if _, err := o.SetRepository(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	_ = o.Start(one(true))
	waitDone(t, o)
	if n := exec.count(DiscoveryStage.Role); n != 3 {
		t.Errorf("a new repository must trigger discovery, ran %d times", n)
	}
}

func TestDiscoveryFailureIsNotCached(t *testing.T) {
	o, _ := newTest(t, &fakeExecutor{failAt: 1})
	_ = o.Start(one(true))
	run := waitDone(t, o)
	if run.Discovery.Status != StepFailed || run.Result != ResultFailed || run.Stories[0].Status != StepCanceled {
		t.Fatalf("discovery failure must fail the run: %+v / %s", run.Discovery, run.Stories[0].Status)
	}
	if o.ws.Discovery() != nil {
		t.Error("failed discovery must not be cached")
	}
}

func TestRunRefusedWhileAnotherWindowHoldsTheRepository(t *testing.T) {
	o, repo := newTest(t, &fakeExecutor{})
	other, err := workspace.LockRepository(repo) // another FazTudo process running there
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Start(realStory("API")); !errors.Is(err, workspace.ErrRepositoryBusy) {
		t.Fatalf("err = %v, want ErrRepositoryBusy", err)
	}
	other()
	if err := o.Start(realStory("API")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)
	if err := o.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	unlock, err := workspace.LockRepository(repo)
	if err != nil {
		t.Fatalf("a finished run must release the repository: %v", err)
	}
	unlock()
}
