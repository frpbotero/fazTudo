package pipeline

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/quality"
)

func TestRenderPendenciesListsUnresolvedIssues(t *testing.T) {
	now := time.Now()
	run := Run{Result: ResultSucceeded, FinishedAt: &now, Stories: []StoryRun{
		{ID: "US-1", Title: "Login", Status: StepWarning, Steps: []Step{
			{Title: "Revisão", Status: StepWarning, Error: "2 problemas persistem", Issues: []Issue{
				{Severity: "high", Description: "senha sem hash", Location: "auth.go:10"},
			}},
			{Title: "Código", Status: StepDone},
		}},
		{ID: "US-2", Title: "Perfil", Status: StepDone, Steps: []Step{{Title: "Código", Status: StepDone}}},
		{ID: "US-3", Title: "Bloqueada", Status: StepSkipped, Steps: []Step{{Status: StepSkipped, Note: "US bloqueada no Passo 0"}}},
		{ID: "US-4", Title: "Existente", Status: StepSkipped, Steps: []Step{{Status: StepSkipped, Note: "Já atendida pelo código existente (Passo 0)"}}},
	}}
	got := renderPendencies(run)
	for _, want := range []string{"US-1", "senha sem hash", "`auth.go:10`", "US-3", "US bloqueada no Passo 0", "2 item(ns)"} {
		if !strings.Contains(got, want) {
			t.Errorf("faltou %q em:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"US-2", "US-4"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("%q não é pendência:\n%s", unwanted, got)
		}
	}
}

func TestRenderPendenciesEmptyWhenClean(t *testing.T) {
	run := Run{Stories: []StoryRun{{ID: "US-1", Status: StepDone, Steps: []Step{{Status: StepDone}}}}}
	if got := renderPendencies(run); got != "" {
		t.Fatalf("esperava vazio, veio:\n%s", got)
	}
}

func TestWritePendenciesFileLifecycle(t *testing.T) {
	repo, config := t.TempDir(), t.TempDir()
	o := journalWindow(t, &fakeExecutor{}, repo, config)
	file := pendenciesPath(filepath.Join(config, "runs"), repo)
	dirty := Run{Repository: repo, Stories: []StoryRun{{ID: "US-1", Status: StepWarning, Steps: []Step{
		{Title: "Revisão", Status: StepWarning, Issues: []Issue{{Description: "x"}}}}}}}

	o.writePendencies(dirty)
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("arquivo não criado: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "PENDENCIAS.md")); err == nil {
		t.Fatal("o repositório não pode receber arquivos novos")
	}
	o.writePendencies(Run{Repository: repo})
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("arquivo deveria sumir quando nada pendente")
	}

	dry := dirty
	dry.DryRun = true
	o.writePendencies(dry)
	if _, err := os.Stat(file); err == nil {
		t.Fatal("dry run não deve gravar")
	}
}

func TestCauseTitleFollowsItsFingerprint(t *testing.T) {
	cmd := Issue{Location: "e2e", Fingerprints: []string{"f1", "f2", "f3"}, Titles: []string{"Backups › status", "Auth › login", "OS › emitir"}}
	for i, want := range cmd.Titles {
		if got := causeTitle(cmd, i); got != want {
			t.Errorf("causeTitle(%d) = %q, want %q", i, got, want)
		}
	}
	one := Issue{Fingerprints: []string{"a", "b"}, Titles: []string{"Testes da especificação sem implementação (backend)"}}
	if got := causeTitle(one, 1); got != one.Titles[0] {
		t.Errorf("a single title names every cause: %q", got)
	}
	bare := Issue{Location: "src/a.go:3", Description: "nil deref\ndetalhe"}
	if got := causeTitle(bare, 0); got != "src/a.go:3 nil deref" {
		t.Errorf("untitled issue = %q", got)
	}
}

func TestPendingCausesOfOneSourceAreOneItem(t *testing.T) {
	var causes []Cause
	for i := range 183 {
		causes = append(causes, Cause{Fingerprint: fmt.Sprintf("fp%03d", i), Title: fmt.Sprintf("Teste %03d: AggregateError", i), Source: "mag_system: test:e2e", Status: causePending})
	}
	causes[0].RootCause = "Postgres de teste inacessível"
	causes[1].Reason = "Depende do Postgres"
	causes = append(causes,
		Cause{Fingerprint: "acc1", Title: "Cenário de aceitação recusado: AC-6 · Registros preservados", Source: "aceitação", Status: causePending},
		Cause{Fingerprint: "rev1", Title: "service.ts:73 exige NF", Source: "service.ts:73", Status: causeBlocked},
		Cause{Fingerprint: "ok", Title: "corrigido", Source: "mag_system: test:e2e", Status: causeFixed},
	)
	groups := pendingCauseGroups(causes)
	if len(groups) != 3 || len(groups[0].causes) != 183 || groups[1].source != "aceitação" || groups[2].status != causeBlocked {
		t.Fatalf("groups = %d: %+v", len(groups), groups)
	}
	var b bytes.Buffer
	if n := writePendingCauses(&b, causes); n != 3 {
		t.Errorf("items = %d, want 3", n)
	}
	got := b.String()
	for _, want := range []string{
		"**mag_system: test:e2e: 183 falha(s)** (persistiu após as tentativas)",
		"Teste 000: AggregateError; Teste 001: AggregateError",
		"e mais 175",
		"Causa raiz: Postgres de teste inacessível",
		"Motivo: Depende do Postgres",
		"**Cenário de aceitação recusado: AC-6 · Registros preservados** (persistiu após as tentativas, `acc1`)",
		"**service.ts:73 exige NF** (bloqueada, `rev1`)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("faltou %q em:\n%s", want, got)
		}
	}
	if strings.Count(got, "Causa raiz") != 1 || strings.Contains(got, "corrigido") {
		t.Errorf("a group repeats its fields or lists a fixed cause:\n%s", got)
	}
}

func TestPendingCountIsTheSameEverywhere(t *testing.T) {
	st := StoryRun{ID: "US-1", Status: StepWarning,
		Steps: []Step{{Title: "Verificação — rodada 2", Status: StepWarning, Issues: []Issue{
			{Description: "e2e falhou", Location: "e2e", Fingerprints: []string{"f1", "f2"}},
			{Description: "sem NF", Location: "a.ts:1", Fingerprints: []string{"f3"}},
		}}},
		Causes: []Cause{
			{Fingerprint: "f1", Source: "e2e", Status: causePending},
			{Fingerprint: "f2", Source: "e2e", Status: causePending},
			{Fingerprint: "f3", Source: "a.ts:1", Status: causePending},
		},
	}
	if n := pendingCount(st); n != 2 {
		t.Errorf("pendingCount = %d, want 2", n)
	}
	if m := storyMetrics(st, nil); m.Pending != 2 {
		t.Errorf("metrics pending = %d", m.Pending)
	}
	now := time.Now()
	got := renderPendencies(Run{FinishedAt: &now, Stories: []StoryRun{st}})
	// Two cause items and the warned step: the step's issues are not repeated.
	if !strings.Contains(got, "3 item(ns)") || strings.Contains(got, "- e2e falhou") || strings.Contains(got, "- sem NF") {
		t.Errorf("pendências:\n%s", got)
	}
	// Without causes, the issues of the warned steps are what is pending.
	st.Causes = nil
	if n := pendingCount(st); n != 2 {
		t.Errorf("pendingCount without causes = %d", n)
	}
}

func TestPersistentFailuresKeepTheirTitlesAndOneCount(t *testing.T) {
	const jest = "FAIL test/cart.spec.ts\n  ● Cart › soma\n\n    expected 2, received 3\n\n  ● Cart › total\n\n    expected 5, received 4\n\n  ● Cart › frete\n\n    expected 10, received 0\n"
	o, _ := newVerifierTest(t, &fakeExecutor{}, func(int) quality.Result {
		return quality.Result{ExitCode: 1, Output: jest}
	})
	_ = o.Start(realStory("Carrinho"))
	run := waitDone(t, o)

	st := run.Stories[0]
	var titles []string
	for _, c := range st.Causes {
		if c.Source != "unit" {
			t.Errorf("cause source = %q", c.Source)
		}
		titles = append(titles, c.Title)
	}
	if got := strings.Join(titles, " | "); !strings.Contains(got, "Cart › soma") || !strings.Contains(got, "Cart › total") || !strings.Contains(got, "Cart › frete") {
		t.Fatalf("each failure must keep its own title: %s", got)
	}
	if n := pendingCount(st); n != 1 {
		t.Errorf("pendingCount = %d, want 1 (one command, three failures)", n)
	}
	if m := storyMetrics(st, nil); m.Pending != 1 {
		t.Errorf("metrics pending = %d", m.Pending)
	}
	if !hasLog(run, "entregue com 1 pendência(s)") {
		t.Errorf("log count differs: %+v", run.Logs)
	}
	got := renderPendencies(run)
	if !strings.Contains(got, "**unit: 3 falha(s)**") {
		t.Errorf("pendências:\n%s", got)
	}
	warned := false
	for _, s := range st.Steps {
		warned = warned || s.Status == StepWarning && len(s.Issues) > 0
	}
	if !warned {
		t.Errorf("the settled step must carry its issues: %s", stepIDs(st.Steps))
	}
}

func TestRetryFinishNamesWhatWasResumed(t *testing.T) {
	if got := retryFinish(-1); !strings.Contains(got, "decisões de produto respondidas") {
		t.Errorf("answered decisions = %q", got)
	}
	if got := retryFinish(3); !strings.Contains(got, "refazer a etapa com falha") {
		t.Errorf("failed stage = %q", got)
	}
}

func TestResultLabelDoesNotCallPendingWorkASuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  Run
		want string
	}{
		{"validated", Run{Result: ResultSucceeded, Validation: ValidationPassed, Stories: []StoryRun{{Status: StepDone}}}, "concluída"},
		{"story pending", Run{Result: ResultSucceeded, Stories: []StoryRun{{Status: StepWarning}}}, "concluída com pendências"},
		{"validation failed", Run{Result: ResultSucceeded, Validation: ValidationFailed, Stories: []StoryRun{{Status: StepDone}}}, "concluída com pendências"},
		{"waiting", Run{Result: ResultSucceeded, Stories: []StoryRun{{Status: StepWaiting}}}, "concluída, aguardando decisão de produto"},
		{"failed", Run{Result: ResultFailed}, "falhou"},
		{"canceled", Run{Result: ResultCanceled}, "cancelada"},
		{"running", Run{}, ""},
	} {
		if got := resultLabel(tc.run); got != tc.want {
			t.Errorf("%s: resultLabel = %q, want %q", tc.name, got, tc.want)
		}
	}
	now := time.Now()
	got := renderPendencies(Run{Result: ResultSucceeded, Validation: ValidationFailed, FinishedAt: &now, Stories: []StoryRun{{ID: "US-1", Status: StepWarning, Steps: []Step{{Title: "QA", Status: StepWarning}}}}})
	if !strings.Contains(got, "Resultado: **concluída com pendências** · validação **reprovada**") {
		t.Errorf("header:\n%s", got)
	}
}
