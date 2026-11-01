package pipeline

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
)

var candidateStages = []Stage{
	{ID: "frontend", Title: "Front", Role: "fe", Implementer: true},
	{ID: "checks", Title: "Checks", Role: "gate", Verifier: true},
}

func newCandidateTest(t *testing.T, exec *fakeExecutor) (*Orchestrator, string) {
	t.Helper()
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	o.stages = candidateStages
	policy := o.ws.Policy()
	policy.Git, policy.Candidates = true, 3
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "README.md", "loja")
	return o, repo
}

func TestBestOfNPicksTheCandidateThatPassesChecks(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newCandidateTest(t, exec)
	exec.onCall = func(req provider.Request) {
		switch {
		case strings.HasSuffix(req.Role, "candidato A"):
			writeFile(t, req.WorkDir, "app.js", "const a = 1;"+strings.Repeat(" a += 1;", 120)+"\n")
		case strings.HasSuffix(req.Role, "candidato B"):
			writeFile(t, req.WorkDir, "app.js", "const a = 1;\n")
		}
	}

	_ = o.Start(realStory("x"))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	front := run.Stories[0].Steps[0]
	if len(front.Candidates) != 3 {
		t.Fatalf("candidates = %+v", front.Candidates)
	}
	a, b, c := front.Candidates[0], front.Candidates[1], front.Candidates[2]
	if a.Chosen || a.Passed || !b.Chosen || !b.Passed || c.Note != "nenhuma alteração" {
		t.Fatalf("candidates = %+v", front.Candidates)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "app.js")); string(data) != "const a = 1;\n" {
		t.Errorf("repository must hold candidate B, got %q", data)
	}
	if front.Commit == nil || len(front.Commit.Files) != 1 {
		t.Errorf("step commit = %+v", front.Commit)
	}
	if out, _ := osexec.Command("git", "-C", repo, "worktree", "list").Output(); strings.Count(string(out), "\n") != 1 {
		t.Errorf("worktrees must be removed:\n%s", out)
	}
	if exec.count(SelectorStage.Role) != 0 {
		t.Error("no tie: the selector must not be called")
	}
	if s := run.Stories[0].Steps[1]; s.Status != StepSkipped {
		t.Errorf("checks after a verified candidate = %s, want skipped", s.Status)
	}
	if entries, _ := filepath.Glob(filepath.Join(os.TempDir(), "faztudo-candidates-*")); len(entries) > 0 {
		for _, e := range entries {
			if strings.Contains(e, filepath.Base(repo)) {
				t.Errorf("worktree left behind: %s", e)
			}
		}
	}
}

func TestBestOfNTieIsBrokenBySelector(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		SelectorStage.Role: {"C é mais simples\n```json\n{\"choice\": \"c\", \"reason\": \"mais simples\"}\n```"},
	}}
	o, repo := newCandidateTest(t, exec)
	exec.onCall = func(req provider.Request) {
		if i := strings.Index(req.Role, "candidato "); i >= 0 {
			writeFile(t, req.WorkDir, "app.js", "// "+req.Role[i+len("candidato "):]+"\n")
		}
	}
	_ = o.Start(realStory("x"))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "app.js")); string(data) != "// C\n" {
		t.Errorf("selector chose C, repository has %q", data)
	}
	if !strings.Contains(run.Stories[0].Steps[0].Output, "mais simples") {
		t.Error("output must explain the choice")
	}
	if exec.count(SelectorStage.Role) != 1 {
		t.Error("the selector must break the tie")
	}
	for _, r := range exec.reqs {
		if r.Role == SelectorStage.Role && !strings.Contains(r.Prompt, "+// A") {
			t.Error("the selector must see the candidates' diffs")
		}
	}
}

func TestBestOfNWithoutChangesIsNotAFailure(t *testing.T) {
	exec := &fakeExecutor{onCall: func(provider.Request) {}}
	o, _ := newCandidateTest(t, exec)
	_ = o.Start(realStory("já implementado"))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	front := run.Stories[0].Steps[0]
	if front.Commit != nil || !front.Candidates[0].Chosen {
		t.Errorf("unchanged candidates: commit = %+v, candidates = %+v", front.Commit, front.Candidates)
	}
}
