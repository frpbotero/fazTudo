package pipeline

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

var resumeStages = []Stage{
	{ID: "a", Title: "A", Role: "ra", Task: "do a"},
	{ID: "checks", Title: "Checks", Role: "gate", Verifier: true},
	{ID: "b", Title: "B", Role: "rb", Task: "do b"},
}

func TestInvalidationsFollowWhatChanged(t *testing.T) {
	ws, err := workspace.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := New(&fakeExecutor{}, ws, resumeStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := RunManifest{
		Schema: journalSchema, Inputs: map[string]string{"preamble": "p", "story:0": "s"},
		Protected: map[string]string{"US-1.md": "x"},
		Policy:    map[string]string{"checks": "c", "flow": "f", "risk": "r", "other": "o"},
		Stages:    map[string]string{"a": "1", "checks": "2", "b": "3"}, Bundle: "v1",
	}
	clone := func(mut func(*RunManifest)) RunManifest {
		data, _ := json.Marshal(base)
		var m RunManifest
		_ = json.Unmarshal(data, &m)
		mut(&m)
		return m
	}
	cases := map[string]struct {
		cur  RunManifest
		from int // -1: nothing to redo
	}{
		"nothing":       {clone(func(*RunManifest) {}), -1},
		"commands":      {clone(func(m *RunManifest) { m.Policy["checks"] = "c2" }), 1},
		"later prompt":  {clone(func(m *RunManifest) { m.Stages["b"] = "9" }), 2},
		"protected doc": {clone(func(m *RunManifest) { m.Protected["US-1.md"] = "y" }), 0},
		"flow":          {clone(func(m *RunManifest) { m.Policy["flow"] = "f2" }), 0},
		"story text":    {clone(func(m *RunManifest) { m.Inputs["story:0"] = "s2" }), 0},
	}
	for name, c := range cases {
		invs := o.invalidations(&base, c.cur, 0)
		got := -1
		if len(invs) > 0 {
			got = earliest(99, invs)
		}
		if got != c.from {
			t.Errorf("%s: redo from %d (%+v), want %d", name, got, invs, c.from)
		}
	}
	if invs := o.invalidations(nil, base, 0); len(invs) != 1 || invs[0].From != 1 {
		t.Errorf("a run without manifest is revalidated from its checks: %+v", invs)
	}
}

// failingResumeTest runs resumeStages until B fails once, with a journal.
func failingResumeTest(t *testing.T) (*Orchestrator, *fakeExecutor, string, string) {
	t.Helper()
	repo, config := t.TempDir(), t.TempDir()
	failed := false
	exec := &fakeExecutor{failIf: func(req provider.Request) bool {
		if req.Role == "rb" && !failed {
			failed = true
			return true
		}
		return false
	}}
	ws, err := workspace.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetCacheDir(filepath.Join(config, "discovery"))
	policy := ws.Policy()
	policy.Git, policy.AutoChecks, policy.SkipBaseline = false, false, true
	policy.Checks = []quality.Command{{Name: "unit", Run: "true"}}
	if _, err := ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	o := New(exec, ws, resumeStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.attempts = 1
	o.SetJournalDir(filepath.Join(config, "runs"))
	writeFile(t, repo, "US-1.md", "# US-1\nstory X\n")
	opts := StartOptions{Stories: []stories.Story{{ID: "US-1", Title: "US-1 X", Content: "story X"}}, Source: "Markdown", DocumentName: "US-1.md"}
	if err := o.Start(opts); err != nil {
		t.Fatal(err)
	}
	if run := waitDone(t, o); run.Result != ResultFailed || run.Manifest == nil {
		t.Fatalf("result = %s, manifest %v", run.Result, run.Manifest)
	}
	return o, exec, repo, config
}

func TestResumeKeepsResultsWhatTheyDependOnDidNotChange(t *testing.T) {
	o, exec, _, _ := failingResumeTest(t)
	if err := o.Resume(); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if run.Result != ResultSucceeded || exec.count("ra") != 1 || len(run.ResumeChecks) != 0 {
		t.Fatalf("result %s, A ran %d time(s), checks %+v", run.Result, exec.count("ra"), run.ResumeChecks)
	}
}

func TestResumeRedoesFromTheChecksWhenTheCommandsChanged(t *testing.T) {
	o, exec, _, _ := failingResumeTest(t)
	policy := o.ws.Policy()
	policy.Checks = []quality.Command{{Name: "unit", Run: "true"}, {Name: "lint", Run: "true"}}
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := o.Resume(); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if exec.count("ra") != 1 {
		t.Errorf("A does not depend on the commands: ran %d time(s)", exec.count("ra"))
	}
	if len(run.ResumeChecks) != 1 || run.ResumeChecks[0].Stage != "checks" {
		t.Fatalf("resume checks = %+v", run.ResumeChecks)
	}
	if checks := step(run.Stories[0].Steps, "checks"); len(checks.Checks) != 2 {
		t.Errorf("the checks did not run again with the new commands: %+v", checks.Checks)
	}
}

func TestResumeStartsOverWhenTheInputDocumentChanged(t *testing.T) {
	o, exec, repo, _ := failingResumeTest(t)
	writeFile(t, repo, "US-1.md", "# US-1\nstory X, agora com outro critério\n")
	if err := o.Resume(); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if exec.count("ra") != 2 || len(run.ResumeChecks) == 0 || !strings.Contains(run.ResumeChecks[0].Reason, "US-1.md") {
		t.Fatalf("A ran %d time(s), checks %+v", exec.count("ra"), run.ResumeChecks)
	}
}

func TestOldJournalIsBackedUpAndRevalidated(t *testing.T) {
	o, exec, repo, config := failingResumeTest(t)
	o.FlushJournal() // the saver writes on its own schedule
	file := journalFile(filepath.Join(config, "runs"), repo)
	var j journal
	data, _ := os.ReadFile(file)
	if err := json.Unmarshal(data, &j); err != nil {
		t.Fatal(err)
	}
	j.Schema, j.Run.Manifest = 0, nil // as saved before manifests
	old, _ := json.Marshal(j)
	if err := os.WriteFile(file, old, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened := New(exec, o.ws, resumeStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	reopened.attempts = 1
	reopened.SetJournalDir(filepath.Join(config, "runs"))
	if _, err := os.Stat(file + ".v0.bak"); err != nil {
		t.Fatalf("the old journal was not kept: %v", err)
	}
	if err := reopened.Resume(); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, reopened)
	if len(run.ResumeChecks) != 1 || run.ResumeChecks[0].Stage != "checks" || run.Result != ResultSucceeded {
		t.Fatalf("result %s, checks %+v", run.Result, run.ResumeChecks)
	}
	saved, _ := os.ReadFile(file)
	if !strings.Contains(string(saved), `"schema":2`) {
		t.Error("the next save writes the current schema")
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the journal is readable by others: %v", info.Mode())
	}
}

func TestCorruptJournalIsIgnored(t *testing.T) {
	repo, config := t.TempDir(), t.TempDir()
	dir := filepath.Join(config, "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalFile(dir, repo), []byte(`{"run": {"repository": `), 0o600); err != nil {
		t.Fatal(err)
	}
	o := journalWindow(t, &fakeExecutor{}, repo, config)
	if run := o.Snapshot(); run.Repository != "" {
		t.Fatalf("a truncated journal loaded a run: %+v", run)
	}
	if err := o.Resume(); err != ErrNoFailedStep {
		t.Errorf("resume = %v, want nothing to resume", err)
	}
}

func TestBranchMovedTellsOutsideCommitsFromTheRunsOwn(t *testing.T) {
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		return gitOut(t, repo, append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	}
	git("init", "-q", "-b", "main")
	writeFile(t, repo, "a.txt", "1")
	git("add", ".")
	git("commit", "-q", "-m", "init")
	git("checkout", "-q", "-b", "faztudo/us-1")
	head := strings.TrimSpace(git("rev-parse", "HEAD"))
	r := StoryRun{Branch: "faztudo/us-1", Head: head}
	ctx := t.Context()

	if _, moved := branchMoved(ctx, repo, r); moved {
		t.Error("an untouched branch did not move")
	}
	writeFile(t, repo, "b.txt", "2")
	git("add", ".")
	git("commit", "-q", "-m", "faztudo(US-1): salva trabalho interrompido")
	if why, moved := branchMoved(ctx, repo, r); moved {
		t.Errorf("the run's own commit counted as outside: %s", why)
	}
	writeFile(t, repo, "c.txt", "3")
	git("add", ".")
	git("commit", "-q", "-m", "ajuste manual")
	if _, moved := branchMoved(ctx, repo, r); !moved {
		t.Error("a commit made outside the run went unnoticed")
	}
	r.Head = strings.TrimSpace(git("rev-parse", "HEAD"))
	git("reset", "-q", "--hard", "main")
	git("commit", "-q", "--allow-empty", "-m", "faztudo: reescrito")
	if why, moved := branchMoved(ctx, repo, r); !moved || !strings.Contains(why, "reescrita") {
		t.Errorf("a rewritten branch = %q, %v", why, moved)
	}
}
