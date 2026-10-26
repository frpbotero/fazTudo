package pipeline

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

// journalWindow is one app window over repo: its own orchestrator, sharing
// the saved discovery and runs with the other windows.
func journalWindow(t *testing.T, exec *fakeExecutor, repo, config string) *Orchestrator {
	t.Helper()
	ws, err := workspace.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetCacheDir(filepath.Join(config, "discovery"))
	o := New(exec, ws, testStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.attempts = 1
	o.SetJournalDir(filepath.Join(config, "runs"))
	return o
}

func waitStepRunning(t *testing.T, o *Orchestrator, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run := o.Snapshot()
		if len(run.Stories) > 0 {
			if i := stepIndex(run.Stories[0].Steps, id); i >= 0 && run.Stories[0].Steps[i].Status == StepRunning {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("step %s never ran", id)
}

func TestRunCutByAppCloseResumesWhereItStopped(t *testing.T) {
	repo, config := t.TempDir(), t.TempDir()
	release := make(chan struct{})
	exec := &fakeExecutor{onCall: func(req provider.Request) {
		if req.Role == "rb" {
			<-release // the app closes while B runs
		}
	}}
	first := journalWindow(t, exec, repo, config)
	if err := first.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	waitStepRunning(t, first, "b")
	first.FlushJournal()
	file := journalFile(filepath.Join(config, "runs"), repo)
	crashed, err := os.ReadFile(file) // what a crash leaves: the run still running
	if err != nil {
		t.Fatal(err)
	}
	first.Stop()
	close(release)
	waitDone(t, first)
	if err := os.WriteFile(file, crashed, 0o644); err != nil {
		t.Fatal(err)
	}

	resumed := &fakeExecutor{}
	reopened := journalWindow(t, resumed, repo, config)
	run := reopened.Snapshot()
	if run.Running || run.Result != ResultCanceled {
		t.Fatalf("reloaded run: running=%v result=%s", run.Running, run.Result)
	}
	if run.Resume == nil || run.Resume.Story != 0 || run.Resume.Step != "b" {
		t.Fatalf("resume point = %+v, want story 0 step b", run.Resume)
	}
	if a := run.Stories[0].Steps[0]; a.Status != StepDone || a.Output != "artifact-ra" {
		t.Fatalf("finished step A not kept: %+v", a)
	}

	if err := reopened.Resume(); err != nil {
		t.Fatal(err)
	}
	run = waitDone(t, reopened)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if got := strings.Join(resumed.roles, ","); got != "rb,rc" {
		t.Errorf("resumed calls = %s, want only B and C", got)
	}
	if !strings.Contains(resumed.prompts[0], "artifact-ra") {
		t.Error("the resumed step must get the saved output of A as context")
	}
}

func TestFinishedRunIsReloadedWithoutResumePoint(t *testing.T) {
	repo, config := t.TempDir(), t.TempDir()
	first := journalWindow(t, &fakeExecutor{}, repo, config)
	if err := first.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	waitDone(t, first)
	first.FlushJournal()

	run := journalWindow(t, &fakeExecutor{}, repo, config).Snapshot()
	if run.Result != ResultSucceeded || len(run.Stories) != 1 || run.Stories[0].Status != StepDone {
		t.Fatalf("reloaded run = %+v", run)
	}
	if run.Resume != nil {
		t.Errorf("a finished run has nothing to resume: %+v", run.Resume)
	}
}

func TestIdleWindowNeverOverwritesTheSavedRun(t *testing.T) {
	repo, config := t.TempDir(), t.TempDir()
	first := journalWindow(t, &fakeExecutor{}, repo, config)
	if err := first.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	waitDone(t, first)
	first.FlushJournal()
	file := journalFile(filepath.Join(config, "runs"), repo)
	saved, _ := os.ReadFile(file)

	idle := journalWindow(t, &fakeExecutor{}, repo, config)
	idle.logf(LevelInfo, "janela ociosa")
	idle.FlushJournal()

	if now, _ := os.ReadFile(file); !bytes.Equal(now, saved) {
		t.Error("a window that did not run must not rewrite the repository's run")
	}
}

func TestStoppedRunResumesInTheSameWindow(t *testing.T) {
	release := make(chan struct{})
	var killed atomic.Bool
	exec := &fakeExecutor{
		onCall: func(req provider.Request) {
			if req.Role == "rb" && !killed.Load() {
				<-release
			}
		},
		// the canceled CLI dies, so its call fails
		failIf: func(req provider.Request) bool { return req.Role == "rb" && killed.CompareAndSwap(true, false) },
	}
	o := journalWindow(t, exec, t.TempDir(), t.TempDir())
	if err := o.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	waitStepRunning(t, o, "b")
	o.Stop()
	killed.Store(true)
	close(release)
	if run := waitDone(t, o); run.Result != ResultCanceled || run.Resume == nil || run.Resume.Step != "b" {
		t.Fatalf("stopped run = %s, resume %+v", run.Result, run.Resume)
	}
	if err := o.Resume(); err != nil {
		t.Fatal(err)
	}
	if run := waitDone(t, o); run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}
}

// Sequential development never runs the dependency analysis; its step must
// not be left pending, or an interruption makes it the resume point and the
// whole run starts over.
func TestSequentialRunResumesInsideTheInterruptedStory(t *testing.T) {
	repo, config := t.TempDir(), t.TempDir()
	release := make(chan struct{})
	var killed atomic.Bool
	exec := &fakeExecutor{
		onCall: func(req provider.Request) {
			if req.Role == "rb" && strings.Contains(req.Prompt, "story two") && !killed.Load() {
				<-release
			}
		},
		failIf: func(req provider.Request) bool {
			return req.Role == "rb" && strings.Contains(req.Prompt, "story two") && killed.CompareAndSwap(true, false)
		},
	}
	o := journalWindow(t, exec, repo, config)
	policy := o.ws.Policy()
	policy.Sequential, policy.Git = true, false
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	opts := StartOptions{DryRun: true, Source: "Markdown", Stories: []stories.Story{
		{ID: "US-1", Title: "US-1 one", Content: "story one"},
		{ID: "US-2", Title: "US-2 two", Content: "story two"},
	}}
	if err := o.Start(opts); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(o.Snapshot().Stories) == 2 && o.Snapshot().Stories[1].Steps[1].Status != StepRunning && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	o.Stop()
	killed.Store(true)
	close(release)
	run := waitDone(t, o)
	if run.Resume == nil || run.Resume.Story != 1 || run.Resume.Step != "b" {
		t.Fatalf("resume point = %+v, want story 1 step b", run.Resume)
	}

	exec.mu.Lock()
	before := len(exec.roles)
	exec.mu.Unlock()
	if err := o.Resume(); err != nil {
		t.Fatal(err)
	}
	if run = waitDone(t, o); run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	exec.mu.Lock()
	after := strings.Join(exec.roles[before:], ",")
	exec.mu.Unlock()
	if after != "rb,rc" {
		t.Errorf("calls after resume = %s, want only B and C of the second story", after)
	}
}

func TestSavedRunWithCanceledUnusedAnalysisResumesInTheStory(t *testing.T) {
	run := Run{Result: ResultCanceled, Waves: [][]int{{0}, {1}}, Analysis: &Step{ID: AnalysisStage.ID, Status: StepCanceled}}
	skipUnusedAnalysis(&run)
	if run.Analysis.Status != StepSkipped {
		t.Fatalf("analysis = %s", run.Analysis.Status)
	}
	noWaves := Run{Analysis: &Step{ID: AnalysisStage.ID, Status: StepCanceled}}
	skipUnusedAnalysis(&noWaves)
	if noWaves.Analysis.Status != StepCanceled {
		t.Fatal("an analysis that never finished must stay the resume point")
	}
}
