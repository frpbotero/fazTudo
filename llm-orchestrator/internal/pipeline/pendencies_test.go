package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
