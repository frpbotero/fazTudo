package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
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
