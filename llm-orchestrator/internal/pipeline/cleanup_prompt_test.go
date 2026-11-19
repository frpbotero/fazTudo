package pipeline

import (
	"strings"
	"testing"

	"llm-orchestrator/internal/skills"
)

func TestCleanupPromptCarriesAreaOptionsAndSkills(t *testing.T) {
	p := cleanupPrompt{
		opts:      CleanupOptions{Area: "internal/pay", Rounds: 2},
		logPath:   "faztudo-out/saude-1.md",
		protected: []string{"US.md"},
		commands:  []string{"go test ./..."},
	}
	prompt := buildCleanupPrompt(CleanupStage.Task, nil, p)
	for _, want := range []string{
		skills.File("/skills", "structural-health"), "Modo: limpeza", "Área: `internal/pay`", "no máximo 2",
		"`faztudo-out/saude-1.md`", "Comentários: não", "Testes: não", "`US.md`", "`go test ./...`", CleanupStage.Task,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}

	p.opts.Comments = true
	prompt = buildCleanupPrompt("t", nil, p)
	if !strings.Contains(prompt, "Comentários: sim") || !strings.Contains(prompt, "Testes: não") {
		t.Errorf("only the chosen option is in scope:\n%s", prompt)
	}
	p.opts.Comments, p.opts.Tests, p.opts.Area = false, true, "."
	prompt = buildCleanupPrompt("t", nil, p)
	if !strings.Contains(prompt, "Comentários: não") || !strings.Contains(prompt, "Testes: sim") || !strings.Contains(prompt, "repositório inteiro") {
		t.Errorf("tests option and whole repository:\n%s", prompt)
	}
}
