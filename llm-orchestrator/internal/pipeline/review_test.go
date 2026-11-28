package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

func TestConsolidateIssuesBySameCause(t *testing.T) {
	issues := []Issue{
		{Agent: "backend", Severity: "média", Description: "nome ruim", Location: "src/cart.go:40", Sources: []string{"Simplicidade"}, Category: "estilo"},
		{Agent: "backend", Severity: "alta", Description: "erro ignorado no total", Location: "src/cart.go:42", Sources: []string{"Especificação"}, Category: "comportamento"},
		{Agent: "frontend", Severity: "alta", Description: "erro ignorado no total", Location: "src/cart.go:42", Sources: []string{"Testes"}},
		{Agent: "backend", Severity: "baixa", Description: "Erro ignorado no total", Location: "outro.go", Sources: []string{"Estrutura"}},
		{Agent: "backend", Severity: "baixa", Description: "falta índice", Location: "src/cart.go:90", Sources: []string{"Estrutura"}},
	}
	got := consolidateIssues(issues)
	if len(got) != 3 {
		t.Fatalf("got %d issues: %+v", len(got), got)
	}
	m := got[0]
	if m.Severity != "alta" || m.Category != "comportamento" || len(m.Sources) != 3 ||
		!strings.Contains(m.Location, "outro.go") || !strings.Contains(m.Description, "nome ruim") {
		t.Errorf("merged = %+v", m)
	}
	if got[1].Agent != "frontend" || got[2].Location != "src/cart.go:90" {
		t.Errorf("distinct causes merged: %+v", got[1:])
	}
}

func TestStyleIsAdviceUnlessTheProjectMakesItBinding(t *testing.T) {
	v := Verdict{Status: VerdictChangesRequested, Issues: []Issue{
		{Agent: "backend", Severity: "média", Category: "estilo", Description: "função com 30 linhas"},
	}}
	got, advice := proportional(v, workspace.Policy{})
	if got.NeedsChanges() || got.Status != VerdictApproved || len(advice) != 1 {
		t.Errorf("style-only review = %+v, advice %+v", got, advice)
	}
	if got, _ := proportional(v, workspace.Policy{StyleBlocking: true}); !got.NeedsChanges() {
		t.Error("style_blocking keeps style binding")
	}
	high := Verdict{Status: VerdictChangesRequested, Issues: []Issue{{Agent: "backend", Severity: "alta", Category: "estilo", Description: "código minificado"}}}
	if got, _ := proportional(high, workspace.Policy{}); !got.NeedsChanges() {
		t.Error("a blocking severity is not downgraded by its category")
	}
}

func TestStyleRemarksAreReportedWithoutAFixRound(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"rev": {fence(`{"status":"changes_requested","issues":[{"agent":"frontend","severity":"baixa","category":"estilo","description":"prefira isEmpty","location":"src/a.ts:3"}]}`)},
	}}
	run := runFlow(t, exec)
	rev := step(run.Stories[0].Steps, "review")
	if len(rev.Issues) != 0 || len(rev.Advisory) != 1 || rev.Validation != ValidationPassed {
		t.Fatalf("review = issues %+v advisory %+v validation %s", rev.Issues, rev.Advisory, rev.Validation)
	}
	if exec.count("fe") != 1 {
		t.Errorf("a style remark triggered %d front-end call(s)", exec.count("fe"))
	}
}

func TestReviewerEditsAreUndone(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newParallelStoryTest(t, exec)
	o.baseStages = []Stage{
		{ID: "a", Title: "A", Role: "ra", Implementer: true},
		{ID: "review", Title: "Review", Role: "rev", Gate: true, ReadOnly: true},
	}
	o.stages = o.baseStages
	exec.onCall = func(req provider.Request) {
		switch req.Role {
		case "ra":
			writeFile(t, req.WorkDir, "cart.txt", "implementado\n")
		case "rev":
			writeFile(t, req.WorkDir, "cart.txt", "reescrito pelo revisor\n")
			writeFile(t, req.WorkDir, "novo.txt", "x\n")
		}
	}
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run := waitDone(t, o)
	rev := step(run.Stories[0].Steps, "review")
	if !strings.Contains(rev.Error, "revisor alterou arquivos, desfeito") {
		t.Fatalf("review step = %+v, logs %+v", rev, run.Logs)
	}
	gitOut(t, repo, "checkout", "-q", run.Stories[0].Branch)
	data, _ := os.ReadFile(filepath.Join(repo, "cart.txt"))
	if string(data) != "implementado\n" || exists(repo, "novo.txt") {
		t.Errorf("the reviewer's edits survived: cart.txt=%q, novo.txt exists=%v", data, exists(repo, "novo.txt"))
	}
}

// A fix step names its read-only gate but is run by an implementer: what it
// changes is the fix and must reach the branch.
func TestFixEditsAreKeptAfterAReadOnlyGate(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"rev": {
			fence(`{"status": "changes_requested", "issues": [{"agent": "a", "severity": "alta", "description": "total errado", "location": "cart.txt:1"}]}`),
			fence(approved),
		},
	}}
	o, repo := newParallelStoryTest(t, exec)
	o.baseStages = []Stage{
		{ID: "a", Title: "A", Role: "ra", Implementer: true},
		{ID: "review", Title: "Review", Role: "rev", Gate: true, ReadOnly: true},
	}
	o.stages = o.baseStages
	var calls atomic.Int32
	exec.onCall = func(req provider.Request) {
		if req.Role != "ra" {
			return
		}
		if calls.Add(1) == 1 {
			writeFile(t, req.WorkDir, "cart.txt", "implementado\n")
			return
		}
		writeFile(t, req.WorkDir, "cart.txt", "corrigido\n")
		writeFile(t, req.WorkDir, "cart_test.txt", "teste novo\n")
	}
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run := waitDone(t, o)
	fix := step(run.Stories[0].Steps, "fix-review-1-a")
	if fix.Status != StepDone || strings.Contains(fix.Error, "desfeito") {
		t.Fatalf("fix step = %+v", fix)
	}
	for _, l := range run.Logs {
		if strings.Contains(l.Message, "revisor alterou arquivos") || strings.Contains(l.Message, "não alteraram nenhum arquivo") {
			t.Errorf("the fix was taken for a reviewer's edit: %s", l.Message)
		}
	}
	gitOut(t, repo, "checkout", "-q", run.Stories[0].Branch)
	if data, _ := os.ReadFile(filepath.Join(repo, "cart.txt")); string(data) != "corrigido\n" || !exists(repo, "cart_test.txt") {
		t.Errorf("the fix did not reach the branch: cart.txt=%q, cart_test.txt exists=%v", data, exists(repo, "cart_test.txt"))
	}
}

// In a parent with a nested repository, undoing a reviewer restores the
// nested files from the nested HEAD: never deletes them.
func TestReviewerEditsInANestedRepositoryAreRestored(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newParallelStoryTest(t, exec)
	initNested(t, repo, "api", map[string]string{"src/service.ts": "original\n", "src/outro.ts": "intacto\n"})
	o.baseStages = []Stage{
		{ID: "a", Title: "A", Role: "ra", Implementer: true},
		{ID: "review", Title: "Review", Role: "rev", Gate: true, ReadOnly: true},
	}
	o.stages = o.baseStages
	exec.onCall = func(req provider.Request) {
		switch req.Role {
		case "ra":
			writeFile(t, req.WorkDir, "api/src/service.ts", "implementado\n")
		case "rev":
			writeFile(t, req.WorkDir, "api/src/service.ts", "reescrito pelo revisor\n")
			writeFile(t, req.WorkDir, "api/src/outro.ts", "mexido pelo revisor\n")
			writeFile(t, req.WorkDir, "api/src/novo.ts", "x\n")
		}
	}
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run := waitDone(t, o)
	rev := step(run.Stories[0].Steps, "review")
	if !strings.Contains(rev.Error, "revisor alterou arquivos, desfeito") {
		t.Fatalf("review step = %+v, logs %+v", rev, run.Logs)
	}
	api := filepath.Join(repo, "api")
	gitOut(t, api, "checkout", "-q", run.Stories[0].Branch)
	for file, want := range map[string]string{"src/service.ts": "implementado\n", "src/outro.ts": "intacto\n"} {
		if data, err := os.ReadFile(filepath.Join(api, file)); err != nil || string(data) != want {
			t.Errorf("api/%s = %q, %v; want %q", file, data, err, want)
		}
	}
	if exists(api, "src/novo.ts") {
		t.Error("the reviewer's new file survived")
	}
	if log := gitOut(t, api, "log", "--diff-filter=D", "--name-only", "--format=", run.Stories[0].Branch); strings.TrimSpace(log) != "" {
		t.Errorf("a commit deleted nested files:\n%s", log)
	}
}

func TestParallelReviewsRespectTheLimit(t *testing.T) {
	var running, peak atomic.Int32
	exec := &fakeExecutor{}
	exec.onCall = func(req provider.Request) {
		if !strings.HasPrefix(req.Role, "Staff Reviewer") && !strings.Contains(req.Role, " · ") {
			return
		}
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
	}
	policy := workspace.DefaultPolicy()
	policy.ParallelLenses = true
	policy.MaxParallelReviews = 2
	policy.ReviewProvider = "codex"
	run, _ := runWithFlowExec(t, policy, exec)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}
	if p := peak.Load(); p > 2 || p == 0 {
		t.Errorf("peak concurrent reviews = %d, want 1..2", p)
	}
	for _, r := range exec.reqs {
		if strings.Contains(r.Role, " · ") && r.Prefer != provider.Codex {
			t.Errorf("review %q did not prefer the review provider: %q", r.Role, r.Prefer)
		}
	}
}

// initNested versions root with name as a nested repository holding files,
// the way a client project keeps its app inside a parent repository.
func initNested(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	git := func(dir string, args ...string) {
		t.Helper()
		gitOut(t, dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	}
	dir := filepath.Join(root, name)
	for file, content := range files {
		writeFile(t, dir, file, content)
	}
	git(dir, "init", "-q")
	git(dir, "add", "-A")
	git(dir, "commit", "-q", "-m", name)
	git(root, "init", "-q")
	git(root, "add", "-A")
	git(root, "commit", "-q", "-m", "pai")
}

// Removing versioned files is allowed (taking a feature out) but always
// shows in the log and in the step.
func TestStepThatDeletesVersionedFilesIsReported(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newParallelStoryTest(t, exec)
	initNested(t, repo, "api", map[string]string{"src/painel.ts": "painel\n", "src/lista.ts": "lista\n"})
	o.baseStages = []Stage{{ID: "a", Title: "A", Role: "ra", Implementer: true}}
	o.stages = o.baseStages
	exec.onCall = func(req provider.Request) {
		if req.Role == "ra" {
			if err := os.Remove(filepath.Join(req.WorkDir, "api/src/painel.ts")); err != nil {
				t.Error(err)
			}
		}
	}
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run := waitDone(t, o)
	a := step(run.Stories[0].Steps, "a")
	if !strings.Contains(a.Note, "removeu 1 arquivo(s) versionado(s): api/src/painel.ts") {
		t.Errorf("step a note = %q", a.Note)
	}
	found := false
	for _, l := range run.Logs {
		found = found || strings.Contains(l.Message, "A removeu 1 arquivo(s) versionado(s)")
	}
	if !found {
		t.Errorf("no deletion warning in the logs: %+v", run.Logs)
	}
}

// Without git nothing can be undone: the step says so, and the files the
// implementer wrote before the review are not blamed on the reviewer.
func TestReviewerEditsWithoutGitAreReportedNotUndone(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	o.baseStages = []Stage{
		{ID: "a", Title: "A", Role: "ra", Implementer: true},
		{ID: "review", Title: "Review", Role: "rev", Gate: true, ReadOnly: true},
	}
	o.stages = o.baseStages
	exec.onCall = func(req provider.Request) {
		switch req.Role {
		case "ra":
			writeFile(t, req.WorkDir, "impl.txt", "x\n")
		case "rev":
			writeFile(t, req.WorkDir, "rev.txt", "x\n")
		}
	}
	_ = o.Start(realStory("Carrinho"))
	run := waitDone(t, o)
	rev := step(run.Stories[0].Steps, "review")
	if !strings.Contains(rev.Error, "sem git, não foi possível desfazer; confira: rev.txt") || strings.Contains(rev.Error, "impl.txt") {
		t.Errorf("review step error = %q", rev.Error)
	}
}
