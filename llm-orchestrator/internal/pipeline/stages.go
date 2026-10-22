package pipeline

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

// Stage is one agent in the engineering pipeline.
type Stage struct {
	ID     string
	Title  string
	Role   string
	Tier   provider.Tier
	System string
	Task   string

	// Specifier marks the stage that lists the required tests by ID.
	Specifier bool
	// Planner marks the stage whose output decides which agents run.
	Planner bool
	// Selectable stages run only when the planner selects them.
	Selectable bool
	// Implementer stages write code and receive fix requests.
	Implementer bool
	// Gate stages return a verdict; requested changes trigger fix rounds.
	Gate bool
	// Verifier stages call no model: the orchestrator runs the project's
	// commands and static checks, and failures go back to the implementers.
	Verifier bool
	// ReadOnly stages run the CLI in a mode that cannot change files, so
	// planners and reviewers are kept from editing by the toolset, not only
	// by the prompt.
	ReadOnly bool
	// Lenses split the stage into parallel reviews, each with its own focus;
	// their verdicts are merged.
	Lenses []Lens
}

// Lens is one focus of a parallel review.
type Lens struct {
	ID    string
	Title string
	Focus string
	// Requires names an agent the plan must include for the lens to run.
	Requires string
}

// reviewLenses are the perspectives of the code review, run in parallel.
var reviewLenses = []Lens{
	{
		ID:    "spec",
		Title: "Aderência à especificação",
		Focus: "Todo requisito, critério, contrato, estado e código de erro da US/EDD implementado exatamente? " +
			"Defenda o usuário: aponte requisito ausente, parcial ou alterado.",
	},
	{
		ID:    "simplicity",
		Title: "Simplicidade e legibilidade",
		Focus: "Duplicação, código morto, funções longas, nomes ruins, abstração ou try/catch sobrando, helper existente não reutilizado, " +
			"import faltando, trecho apagado sem motivo, estilo diferente do projeto, código minificado.",
	},
	{
		ID:    "tests",
		Title: "Testes",
		Focus: "Cada critério e caso de teste da especificação coberto, com ID no nome? Expectativa contrária à especificação, teste frágil ou vazio.",
	},
	{
		ID:       "ux",
		Title:    "Interface e acessibilidade",
		Focus:    "WCAG 2.2 AA (rótulos, foco, ARIA válido, contraste), celular, elemento cobrindo controle, estados de carregamento/erro/vazio, consistência visual.",
		Requires: "frontend",
	},
}

// activeLenses returns the lenses whose required agent the plan selected.
func activeLenses(lenses []Lens, plan Plan) []Lens {
	var out []Lens
	for _, l := range lenses {
		if l.Requires == "" || plan.Includes(l.Requires) {
			out = append(out, l)
		}
	}
	return out
}

// Output contracts. They stay explicit: the orchestrator parses them.
const planInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"agents\": [\"frontend\", \"backend\", \"security\", \"e2e\"], \"reason\": \"motivo de cada escolha\"}\n```\n" +
	"\"agents\": só o necessário. frontend = interface; backend = API, dados, regras; " +
	"security = auth, dados sensíveis, pagamento, entrada externa; e2e = fluxo de tela. " +
	"Só interface: sem backend. Mudança simples: sem security nem e2e. Code review sempre roda."

const specInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"tests\": [{\"id\": \"UT-CART-01\", \"description\": \"o que verifica\", \"kind\": \"unit\" | \"integration\" | \"e2e\"}]}\n```\n" +
	"Documento já tem IDs de teste: use os mesmos. Orquestrador confere cada ID nos arquivos de teste."

const verdictInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"status\": \"approved\" | \"changes_requested\", \"issues\": [{\"agent\": \"frontend\" | \"backend\", \"severity\": \"alta\" | \"média\" | \"baixa\", " +
	"\"description\": \"problema e correção\", \"location\": \"arquivo:linha\"}]}\n```\n" +
	"changes_requested só para o que bloqueia entrega. Cada issue com o agente que corrige. Sugestão opcional fora de issues."

// engineeringRules are the implementers' working rules, adapted from the
// Codebuff/Freebuff orchestrator guidelines.
const engineeringRules = "\n\nRegras:\n" +
	"- Leia código vizinho, testes e config antes de editar. Siga convenções existentes.\n" +
	"- Não assuma biblioteca: confirme no manifesto (package.json, go.mod…) antes de usar.\n" +
	"- Mudança mínima, solução simples.\n" +
	"- Reutilize helpers e componentes existentes. Não reimplemente.\n" +
	"- Mudou símbolo exportado: atualize todas as referências.\n" +
	"- Sem git commit/push/branch (orquestrador versiona), sem instalação global, sem script de produção.\n" +
	"- Valide: rode testes, typecheck e lint da área alterada antes de terminar.\n" +
	browserRule +
	"- Resumo final curto: poucas palavras por mudança."

// browserRule keeps agents off the system Chrome: launched inside the CLI
// sandbox it aborts while registering with the window server, and macOS shows
// a "Google Chrome quit unexpectedly" dialog for every attempt.
const browserRule = "- Navegador de teste: Chromium do Playwright em headless (`npx playwright install chromium`). " +
	"Nunca `channel: 'chrome'`/`msedge` nem Chrome do sistema; se a config usar, troque para o Chromium.\n"

// DiscoveryStage maps the repository before any implementation. Its report is
// cached per repository for the session and shared with every other stage.
var DiscoveryStage = Stage{
	ID:     "discovery",
	Title:  "Discovery do repositório",
	Role:   "Staff Engineer (Discovery)",
	Tier:   provider.TierStandard,
	System: "Discovery de repositório existente. Só leitura: não crie, edite nem apague arquivos.",
	Task: "Relatório Markdown curto, por módulo (raiz, frontend, backend, infra…):\n" +
		"1. Linguagens, frameworks, versões.\n" +
		"2. Comandos: instalar, build, teste, lint, rodar.\n" +
		"3. Arquitetura, pastas, convenções.\n" +
		"4. Testes: onde ficam, como são escritos.\n" +
		"5. Riscos para quem for implementar.\n" +
		"Será o contexto fixo de todos os agentes.",
}

// DefaultStages is the sequential context → EDD → architecture →
// implementation → executable checks → review → E2E → final checks flow.
var DefaultStages = []Stage{
	{
		ID:       "context",
		Title:    "Contexto da US",
		Role:     "Context Scout",
		Tier:     provider.TierStandard,
		ReadOnly: true,
		System:   "Mapeia código existente antes de qualquer mudança. Só leitura.",
		Task: "Relatório curto:\n" +
			"1. Até 15 arquivos relevantes: caminho + por que importa (uma linha).\n" +
			"2. Funções, componentes, utilitários a reutilizar.\n" +
			"3. Testes relacionados e onde ficam os novos.\n" +
			"4. Convenções a seguir.\n" +
			"Não proponha implementação.",
	},
	{
		ID:        "spec",
		Title:     "Especificação EDD e contratos TDD",
		Role:      "Staff Spec Engineer",
		Tier:      provider.TierStandard,
		System:    "Engenheiro de especificação (EDD + TDD). Só leitura.",
		Task:      "US vira EDD: contratos, schemas, endpoints, estados, erros. Plano TDD: um ID por caso de teste. Sem código de implementação.",
		Specifier: true,
		ReadOnly:  true,
	},
	{
		ID:       "arch",
		Title:    "Arquitetura e plano de delegação",
		Role:     "Chief Architect",
		Tier:     provider.TierHeavy,
		Planner:  true,
		ReadOnly: true,
		System:   "Arquiteto principal. Só leitura.",
		Task:     "Divida o trabalho entre os agentes: o que cada um faz, arquivos, dependências e ordem. Curto e concreto.",
	},
	{
		ID:          "frontend",
		Title:       "Frontend e UI/UX",
		Role:        "Frontend Specialist",
		Tier:        provider.TierStandard,
		Selectable:  true,
		Implementer: true,
		System: "Agente de frontend. Interface acessível (WCAG 2.2 AA), responsiva e caprichada: hierarquia, contraste, " +
			"estados de hover/foco/erro/vazio, transições discretas." + engineeringRules,
		Task: "Implemente interface e componentes conforme EDD e plano do arquiteto.",
	},
	{
		ID:          "backend",
		Title:       "Backend e dados",
		Role:        "Backend & ML Engineer",
		Tier:        provider.TierStandard,
		Selectable:  true,
		Implementer: true,
		System:      "Agente de backend e dados. APIs resilientes, regras determinísticas, entrada validada." + engineeringRules,
		Task:        "Implemente endpoints, serviços e algoritmos até todos os testes do plano TDD passarem.",
	},
	{
		ID:       "checks",
		Title:    "Verificação executável",
		Role:     "Portão automático",
		Verifier: true,
		Task:     "Executa os comandos do projeto, checagens de sintaxe e legibilidade e a rastreabilidade dos testes.",
	},
	{
		ID:         "security",
		Title:      "Segurança (OWASP) e testes unitários",
		Role:       "AppSec & QA Engineer",
		Tier:       provider.TierHeavy,
		Selectable: true,
		Gate:       true,
		ReadOnly:   true,
		System:     "Revisor de segurança (OWASP Top 10). Só leitura; orquestrador já roda os testes.",
		Task:       "Procure: entrada sem validação/sanitização, XSS, injeção, auth/autorização falha, segredo exposto, header inseguro. Avalie também os testes unitários.",
	},
	{
		ID:       "review",
		Title:    "Code review",
		Role:     "Staff Reviewer",
		Tier:     provider.TierHeavy,
		Gate:     true,
		ReadOnly: true,
		Lenses:   reviewLenses,
		System:   "Revisor sênior rigoroso. Só leitura: aponta, não corrige. Só crítica, sem elogios. Nada a apontar: diga em uma frase.",
		Task: "Revise as mudanças desta US. Cada violação vai em issues com arquivo:linha.\n" +
			"1. Legível: limite de linha, nada minificado, funções curtas, nomes claros.\n" +
			"2. EDD: contratos, estados, erros e pastas como especificado.\n" +
			"3. Testes: cada critério coberto, ID no nome, expectativas corretas.\n" +
			"4. Robustez: entrada validada, erro tratado, sem duplicação.\n" +
			"5. Interface: acessível, responsiva, nada cobrindo controle.",
	},
	{
		ID:         "e2e",
		Title:      "QA E2E com Playwright",
		Role:       "E2E QA Lead",
		Tier:       provider.TierStandard,
		Selectable: true,
		Gate:       true,
		System:     "QA E2E com Playwright em navegador headless. Escreva testes E2E; não altere código da aplicação.\n\n" + browserRule,
		Task:       "Suba a aplicação, escreva/rode os testes Playwright dos fluxos da US (ID do caso no nome) e verifique tela e rede.",
	},
	{
		ID:       "final",
		Title:    "Verificação final",
		Role:     "Portão automático",
		Verifier: true,
		Task:     "Repete os comandos e checagens depois das correções pedidas pelos revisores.",
	},
}

// maxArtifactBytes caps how much of each previous stage's output is forwarded.
const maxArtifactBytes = 12_000

// buildDiscoveryPrompt gives the discovery agent the deterministic scan as a
// starting point.
func buildDiscoveryPrompt(repo string, stacks []workspace.Stack) string {
	return fmt.Sprintf("## Repositório\n\n`%s`\n\n## Manifestos detectados automaticamente\n\n%s\n\n## Sua tarefa\n\n%s",
		repo, workspace.FormatStacks(stacks), DiscoveryStage.Task)
}

// storyContext places a story within its document and sequence.
type storyContext struct {
	Preamble  string
	Story     stories.Story
	Delivered []stories.Story // earlier stories of this sequence, already implemented
	Position  int             // 1-based
	Total     int
	Rules     rules
	// Instructions are the project's agent instruction files (AGENTS.md…).
	Instructions string
}

// rules are the delivery requirements the orchestrator enforces, stated up
// front so agents meet them instead of discovering them in fix rounds.
type rules struct {
	Checks    []quality.Command
	Protected []string
	MaxLine   int
}

func writeRules(b *strings.Builder, r rules) {
	b.WriteString("## Regras de entrega (orquestrador verifica)\n\n")
	if len(r.Protected) > 0 {
		fmt.Fprintf(b, "- Somente leitura: %s. Alterou, etapa reprovada.\n", strings.Join(quoteAll(r.Protected), ", "))
	}
	if len(r.Checks) > 0 {
		b.WriteString("- Estes comandos precisam passar (orquestrador roda e reprova falha):\n")
		for _, c := range r.Checks {
			fmt.Fprintf(b, "  - %s: `%s`\n", c.Name, c.Run)
		}
	}
	if r.MaxLine > 0 {
		fmt.Fprintf(b, "- Linha ≤ %d caracteres. Nada minificado.\n", r.MaxLine)
	}
	b.WriteString("- Teste cita no nome o ID do caso (ex.: `UT-CART-01`).\n")
	b.WriteString("- Só diga concluído depois de rodar. Termine com resumo da saída dos testes.\n\n")
}

func quoteAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "`" + s + "`"
	}
	return out
}

// buildPrompt gives a stage the repository discovery, the story (with its
// place in the sequence) and every artifact produced so far for that story,
// so agents build on each other instead of starting blind.
func buildPrompt(stage Stage, sc storyContext, discovery *workspace.Report, previous []Step) string {
	var b strings.Builder
	if discovery != nil {
		fmt.Fprintf(&b, "## Repositório\n\n`%s`\n\nStacks:\n%s\n\n",
			discovery.Path, workspace.FormatStacks(discovery.Stacks))
		if summary := strings.TrimSpace(discovery.Summary); summary != "" {
			fmt.Fprintf(&b, "### Discovery (não refaça)\n\n%s\n\n", clip(summary, maxArtifactBytes))
		}
	}
	writeInstructions(&b, sc.Instructions)

	if sc.Preamble != "" {
		fmt.Fprintf(&b, "## Contexto do documento\n\n%s\n\n", clip(sc.Preamble, maxArtifactBytes))
	}
	if sc.Total > 1 {
		fmt.Fprintf(&b, "## Sequência de desenvolvimento\n\nUS %d de %d.", sc.Position, sc.Total)
		if len(sc.Delivered) > 0 {
			b.WriteString(" Já entregues no repositório (reutilize, não quebre):\n")
			for _, d := range sc.Delivered {
				fmt.Fprintf(&b, "- %s\n", d.Title)
			}
		}
		b.WriteString("\n\n")
	}

	b.WriteString("## User Story\n\n")
	b.WriteString(strings.TrimSpace(sc.Story.Content))
	b.WriteString("\n\n")

	if len(previous) > 0 {
		b.WriteString("## Etapas anteriores\n\n")
		for _, s := range previous {
			fmt.Fprintf(&b, "### %s (%s)\n\n%s\n\n", s.Title, s.Role, clip(strings.TrimSpace(s.Output), maxArtifactBytes))
		}
	}

	writeRules(&b, sc.Rules)
	b.WriteString("## Sua tarefa\n\n")
	b.WriteString(stage.Task)
	b.WriteString(stageInstructions(stage))
	return b.String()
}

// stageInstructions appends the machine-readable output contract.
func stageInstructions(stage Stage) string {
	switch {
	case stage.Specifier:
		return specInstructions
	case stage.Planner:
		return planInstructions
	case stage.Gate:
		return verdictInstructions
	}
	return ""
}

// writeContext writes the discovery, document and story sections shared by
// every prompt of a story.
func writeContext(b *strings.Builder, sc storyContext, discovery *workspace.Report) {
	if discovery != nil {
		fmt.Fprintf(b, "## Repositório\n\n`%s`\n\nStacks:\n%s\n\n",
			discovery.Path, workspace.FormatStacks(discovery.Stacks))
	}
	writeInstructions(b, sc.Instructions)
	if sc.Preamble != "" {
		fmt.Fprintf(b, "## Contexto do documento\n\n%s\n\n", clip(sc.Preamble, maxArtifactBytes))
	}
	b.WriteString("## User Story\n\n")
	b.WriteString(strings.TrimSpace(sc.Story.Content))
	b.WriteString("\n\n")
	writeRules(b, sc.Rules)
}

func writeInstructions(b *strings.Builder, text string) {
	if text = strings.TrimSpace(text); text != "" {
		fmt.Fprintf(b, "## Instruções do projeto (obrigatório)\n\n%s\n\n", text)
	}
}

func writeIssues(b *strings.Builder, issues []Issue) {
	for i, is := range issues {
		fmt.Fprintf(b, "%d. ", i+1)
		if is.Severity != "" {
			fmt.Fprintf(b, "[%s] ", is.Severity)
		}
		b.WriteString(strings.TrimSpace(is.Description))
		if is.Location != "" {
			fmt.Fprintf(b, " (%s)", is.Location)
		}
		b.WriteString("\n")
	}
}

// buildFixPrompt asks an implementer to fix what a gate reported.
func buildFixPrompt(stage, gate Stage, sc storyContext, discovery *workspace.Report, plan string, report string, issues []Issue, round int) string {
	var b strings.Builder
	writeContext(&b, sc, discovery)
	if plan != "" {
		fmt.Fprintf(&b, "## Plano do arquiteto\n\n%s\n\n", clip(plan, maxArtifactBytes))
	}
	fmt.Fprintf(&b, "## Relatório de %s (rodada %d)\n\n%s\n\n", gate.Title, round, clip(stripJSONBlocks(report), maxArtifactBytes))
	b.WriteString("## Correções obrigatórias atribuídas a você\n\n")
	writeIssues(&b, issues)
	b.WriteString("\n## Sua tarefa\n\nCorrija só os itens acima. Não mexa em comportamento não relacionado. " +
		"Itens de outros agentes no relatório: ignore. Rode os testes afetados. Termine com uma linha por item: o que mudou.")
	return b.String()
}

// buildVerifyPrompt re-runs a gate after fixes, pointing at what changed.
func buildVerifyPrompt(gate Stage, sc storyContext, discovery *workspace.Report, issues []Issue, fixes []Step, round int) string {
	var b strings.Builder
	writeContext(&b, sc, discovery)
	fmt.Fprintf(&b, "## Problemas apontados na rodada %d\n\n", round-1)
	writeIssues(&b, issues)
	b.WriteString("\n## Correções aplicadas\n\n")
	for _, f := range fixes {
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", f.Title, clip(strings.TrimSpace(f.Output), maxArtifactBytes))
	}
	fmt.Fprintf(&b, "## Sua tarefa\n\nVerificação, rodada %d: cada problema acima resolvido? Correção quebrou algo? %s", round, gate.Task)
	b.WriteString(verdictInstructions)
	return b.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n\n[… conteúdo truncado]"
}
