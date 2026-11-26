package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

var verifierStages = []Stage{
	{ID: "spec", Title: "Spec", Role: "spec", Specifier: true},
	{ID: "frontend", Title: "Front", Role: "fe", Implementer: true},
	{ID: "checks", Title: "Checks", Role: "gate", Verifier: true},
}

// newVerifierTest builds an orchestrator in real mode over a temp repository
// with git disabled and one scripted check command.
func newVerifierTest(t *testing.T, exec *fakeExecutor, check func(n int) quality.Result) (*Orchestrator, string) {
	t.Helper()
	repo := t.TempDir()
	ws, err := workspace.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	policy := workspace.DefaultPolicy()
	policy.AutoChecks = false
	policy.Git = false
	policy.Checks = []quality.Command{{Name: "unit", Run: "npm test"}}
	// These tests script each command call; baselines have their own tests.
	policy.SkipBaseline = true
	if _, err := ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	o := New(exec, ws, verifierStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var mu sync.Mutex
	calls := 0
	if exec.onCall == nil {
		// An agent that changes nothing ends the fix loop: these tests model one that works.
		edits := 0
		exec.onCall = func(provider.Request) {
			mu.Lock()
			defer mu.Unlock()
			edits++
			writeFile(t, repo, fmt.Sprintf("src/edit-%d.txt", edits), "x")
		}
	}
	o.runCheck = func(_ context.Context, _ string, c quality.Command, _ time.Duration) quality.Result {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		r := check(n)
		r.Name, r.Command = c.Name, c.Run
		return r
	}
	return o, repo
}

func realStory(content string) StartOptions {
	return StartOptions{Stories: []stories.Story{{ID: "US-7", Title: "US-7 Loja", Content: content}}, Source: "Markdown"}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFailingCommandGoesBackToImplementer(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, func(n int) quality.Result {
		if n == 1 {
			return quality.Result{ExitCode: 1, Output: "FAIL cart.test.mjs: expected 2, got 1"}
		}
		return quality.Result{Passed: true}
	})
	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}

	steps := run.Stories[0].Steps
	ids := make([]string, len(steps))
	for i, s := range steps {
		ids[i] = s.ID + ":" + string(s.Status)
	}
	want := "spec:done frontend:done checks:done investigate-checks-1:done fix-checks-1-frontend:done checks-verify-2:done"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("steps =\n%s\nwant\n%s", got, want)
	}
	if exec.count("fe") != 2 {
		t.Errorf("frontend calls = %d, want 2 (implementation + fix)", exec.count("fe"))
	}
	fix := lastPromptOf(exec, "fe")
	for _, want := range []string{"npm test", "expected 2, got 1", "Correções obrigatórias"} {
		if !strings.Contains(fix, want) {
			t.Errorf("fix prompt missing %q:\n%s", want, fix)
		}
	}
	if c := steps[2].Checks; len(c) != 1 || c[0].Passed {
		t.Errorf("first verification must record the failing command: %+v", c)
	}
}

func TestPersistentFailureMovesOnWithPendingIssues(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{ExitCode: 2, Output: "erro"} })
	opts := realStory("x")
	opts.Stories = append(opts.Stories, stories.Story{ID: "US-8", Title: "US-8 Próxima", Content: "y"})
	_ = o.Start(opts)
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}
	if run.Stories[0].Status != StepWarning || run.Stories[1].Status != StepWarning {
		t.Errorf("stories = %s, %s; both must finish with pending issues", run.Stories[0].Status, run.Stories[1].Status)
	}
	// The second story only develops: it inherits the failure the first one left.
	if got, want := exec.count("fe"), 1+maxCauseAttempts+1; got != want {
		t.Errorf("frontend calls = %d, want %d (both stories developed)", got, want)
	}
	if st := run.Stories[0].Steps; st[len(st)-1].Status != StepWarning {
		t.Errorf("last verification must keep the pending issues: %+v", st[len(st)-1])
	}
}

func TestProtectedInputIsRestoredAndFailsStep(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	writeFile(t, repo, "US_loja.md", "# US original")
	writeFile(t, repo, "historia.md", "# carregada pela UI")
	exec.onCall = func(req provider.Request) {
		if req.Role == "fe" {
			writeFile(t, repo, "US_loja.md", "O protótipo está em index.html")
		}
	}

	opts := realStory("x")
	opts.DocumentName = "/Users/alguem/Downloads/historia.md"
	_ = o.Start(opts)
	run := waitDone(t, o)

	if run.Result != ResultFailed {
		t.Fatalf("result = %s", run.Result)
	}
	if want := []string{"US_loja.md", "historia.md"}; strings.Join(run.Protected, ",") != strings.Join(want, ",") {
		t.Errorf("protected = %v, want %v", run.Protected, want)
	}
	front := run.Stories[0].Steps[1]
	if front.Status != StepFailed || !strings.Contains(front.Error, "US_loja.md") {
		t.Fatalf("frontend step = %+v", front)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "US_loja.md")); string(data) != "# US original" {
		t.Errorf("input not restored: %q", data)
	}
	if !strings.Contains(exec.prompts[1], "`US_loja.md`") {
		t.Error("prompts must tell agents which files are read-only")
	}
}

func TestTraceabilityRequiresTestsForSpecIDs(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"spec": {"EDD...\n```json\n{\"tests\":[{\"id\":\"UT-CART-02\",\"description\":\"remove item\",\"kind\":\"unit\"}]}\n```"},
	}}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	fixes := 0
	exec.onCall = func(req provider.Request) {
		if req.Role != "fe" {
			return
		}
		if fixes++; fixes == 1 {
			writeFile(t, repo, "tests/cart.test.mjs", "test('UT-CART-01 adiciona', () => {})\n")
			return
		}
		writeFile(t, repo, "tests/cart.test.mjs", "test('UT-CART-01 adiciona', () => {})\ntest('UT-CART-02 remove', () => {})\n")
	}

	doc := "Carrinho\n\n## Plano de testes\n| ID | Caso |\n|---|---|\n| UT-CART-01 | adiciona item |\n"
	_ = o.Start(realStory(doc))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	trace := run.Stories[0].Trace
	if len(trace) != 2 || !trace[0].Found() || !trace[1].Found() {
		t.Fatalf("trace = %+v", trace)
	}
	first := run.Stories[0].Steps[2]
	if len(first.Issues) != 1 || !strings.Contains(first.Issues[0].Description, "UT-CART-02") || first.Issues[0].Agent != "frontend" {
		t.Errorf("first verification issues = %+v", first.Issues)
	}
}

func TestReadabilityFindingsBlockMinifiedCode(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	calls := 0
	exec.onCall = func(req provider.Request) {
		if req.Role != "fe" {
			return
		}
		if calls++; calls == 1 {
			writeFile(t, repo, "app.js", "const a=1;"+strings.Repeat("a+=1;", 200)+"\n")
			return
		}
		writeFile(t, repo, "app.js", "const a = 1;\n")
	}
	_ = o.Start(realStory("x"))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}
	first := run.Stories[0].Steps[2]
	if len(first.Findings) != 1 || first.Findings[0].Check != "readability" || first.Issues[0].Location != "app.js:1" {
		t.Errorf("findings = %+v issues = %+v", first.Findings, first.Issues)
	}
}

func TestVerifierIsSimulatedInDryRun(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result {
		t.Error("commands must not run in simulation")
		return quality.Result{}
	})
	opts := realStory("x")
	opts.DryRun = true
	_ = o.Start(opts)
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}
	if s := run.Stories[0].Steps[2]; s.Status != StepDone || !strings.Contains(s.Note, "Simulação") {
		t.Errorf("checks step = %+v", s)
	}
}

func TestGitBranchAndCommitPerStep(t *testing.T) {
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	exec := &fakeExecutor{}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	policy := o.ws.Policy()
	policy.Git = true
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "README.md", "projeto")
	exec.onCall = func(req provider.Request) {
		if req.Role == "fe" {
			writeFile(t, repo, "index.html", "<h1>loja</h1>\n")
		}
	}

	_ = o.Start(realStory("x"))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	st := run.Stories[0]
	if !strings.HasPrefix(st.Branch, "faztudo/us-7-") || run.BaseBranch == "" {
		t.Fatalf("branch = %q base = %q", st.Branch, run.BaseBranch)
	}
	if st.Steps[0].Commit != nil {
		t.Error("a step without changes must not commit")
	}
	c := st.Steps[1].Commit
	if c == nil || len(c.Files) != 1 || c.Files[0].Path != "index.html" {
		t.Fatalf("frontend commit = %+v", c)
	}

	base := run.BaseBranch

	// Pending changes left on a FazTudo branch (an interrupted run) are
	// committed and the next run goes on from them.
	osexec.Command("git", "-C", repo, "switch", "-q", st.Branch).Run()
	writeFile(t, repo, "index.html", "trabalho interrompido")
	_ = o.Start(realStory("y"))
	if run = waitDone(t, o); run.Result != ResultSucceeded {
		t.Fatalf("interrupted work must be saved, not block: %s, logs: %+v", run.Result, run.Logs)
	}

	// Pending changes on the user's own branch block the next run.
	git := func(args ...string) {
		t.Helper()
		cmd := osexec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("switch", "-q", base)
	writeFile(t, repo, "README.md", "edição manual")
	_ = o.Start(realStory("z"))
	run = waitDone(t, o)
	if run.Result != ResultFailed || run.Discovery.Status != StepCanceled {
		t.Fatalf("dirty user branch must fail before discovery: %s / %s", run.Result, run.Discovery.Status)
	}
}
