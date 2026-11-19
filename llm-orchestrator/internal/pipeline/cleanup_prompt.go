package pipeline

import (
	"fmt"
	"strings"

	"llm-orchestrator/internal/workspace"
)

// cleanupPrompt is what the cleanup agent needs beyond the repository.
type cleanupPrompt struct {
	opts         CleanupOptions
	logPath      string // decision log, relative to the repository
	instructions string
	protected    []string
	commands     []string
}

// buildCleanupPrompt follows contracts/agent-prompt.md: the stable context
// first, for the prompt cache, then the cleanup's own settings and the task.
func buildCleanupPrompt(task string, discovery *workspace.Report, p cleanupPrompt) string {
	var b strings.Builder
	writeRepository(&b, discovery)
	writeInstructions(&b, p.instructions)
	writeSkills(&b, CleanupStage.Skills)
	b.WriteString("## Limpeza\n\n")
	b.WriteString("- Modo: limpeza da skill `structural-health`.\n")
	if p.opts.Area == "." {
		b.WriteString("- Área: o repositório inteiro.\n")
	} else {
		fmt.Fprintf(&b, "- Área: `%s`. Só arquivos dentro dela podem mudar; o orquestrador reprova qualquer outro. "+
			"Mudança que afetaria quem está fora vira apontamento no registro.\n", p.opts.Area)
	}
	fmt.Fprintf(&b, "- Rodadas: no máximo %d.\n", p.opts.Rounds)
	fmt.Fprintf(&b, "- Registro de decisões: escreva em `%s` (fica fora do git; o orquestrador lê ao fim).\n", p.logPath)
	fmt.Fprintf(&b, "- Comentários: %s.\n", yesNo(p.opts.Comments))
	fmt.Fprintf(&b, "- Testes: %s. Teste com ID de critério de aceite (ex.: `UT-CART-01`) nunca some.\n", yesNo(p.opts.Tests))
	if len(p.protected) > 0 {
		fmt.Fprintf(&b, "- Arquivos protegidos (não altere): %s.\n", strings.Join(quoteAll(p.protected), ", "))
	}
	if len(p.commands) > 0 {
		fmt.Fprintf(&b, "- Comandos para checar cada fatia (saída curta, `| tail -n 50`): %s.\n", strings.Join(quoteAll(p.commands), ", "))
	}
	b.WriteString("- O orquestrador roda lint e testes antes e depois e reprova qualquer falha nova.\n\n")
	b.WriteString("## Sua tarefa\n\n")
	b.WriteString(task)
	return b.String()
}

func yesNo(v bool) string {
	if v {
		return "sim (siga a seção da skill)"
	}
	return "não (fora do escopo)"
}
