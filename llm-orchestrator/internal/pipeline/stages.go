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
		Focus: "Cada requisito, critério de aceitação, contrato, estado e código de erro da US/EDD foi implementado exatamente como especificado? " +
			"Defenda o usuário: aponte todo requisito ausente, parcial ou alterado.",
	},
	{
		ID:    "simplicity",
		Title: "Simplicidade e legibilidade",
		Focus: "Código mínimo e legível: duplicação, código morto, funções longas, nomes ruins, abstrações ou try/catch desnecessários, " +
			"código existente que deveria ter sido reutilizado e qualquer trecho minificado ou compactado.",
	},
	{
		ID:    "tests",
		Title: "Testes",
		Focus: "Os testes cobrem cada critério de aceitação e caso de teste da especificação, com o ID no nome? " +
			"Aponte expectativas que contrariam a especificação, testes frágeis e testes que não verificam nada.",
	},
	{
		ID:    "ux",
		Title: "Interface e acessibilidade",
		Focus: "Acessibilidade (WCAG 2.2 AA: rótulos, foco, ARIA válido, contraste), responsividade no celular, elementos sobrepondo controles, " +
			"estados de carregamento, erro e vazio, e consistência visual.",
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

const planInstructions = "\n\nAo final da resposta, inclua obrigatoriamente um bloco ```json com o plano de execução:\n" +
	"```json\n{\"agents\": [\"frontend\", \"backend\", \"security\", \"e2e\"], \"reason\": \"por que cada agente é (ou não é) necessário\"}\n```\n" +
	"Inclua em \"agents\" somente os agentes realmente necessários para esta US: \"frontend\" (interface), \"backend\" (APIs, dados, regras), " +
	"\"security\" (auditoria OWASP e testes unitários: autenticação, dados sensíveis, pagamentos, entradas externas) e \"e2e\" (fluxos de tela com Playwright). " +
	"Uma mudança só de frontend não chama backend; uma mudança simples não precisa de security nem e2e. O code review sempre roda."

const specInstructions = "\n\nAo final da resposta, inclua obrigatoriamente um bloco ```json com os casos de teste exigidos:\n" +
	"```json\n{\"tests\": [{\"id\": \"UT-CART-01\", \"description\": \"o que o teste verifica\", \"kind\": \"unit\" | \"integration\" | \"e2e\"}]}\n```\n" +
	"Se o documento já define IDs de casos de teste, reutilize exatamente os mesmos IDs. " +
	"O orquestrador verifica ao final que cada ID aparece nos arquivos de teste do repositório."

const verdictInstructions = "\n\nAo final da resposta, inclua obrigatoriamente um bloco ```json com o veredito:\n" +
	"```json\n{\"status\": \"approved\" | \"changes_requested\", \"issues\": [{\"agent\": \"frontend\" | \"backend\", \"severity\": \"alta\" | \"média\" | \"baixa\", " +
	"\"description\": \"o que está errado e o que deve ser feito\", \"location\": \"arquivo:linha\"}]}\n```\n" +
	"Use \"changes_requested\" apenas para problemas que impedem a entrega; atribua cada problema ao agente que deve corrigi-lo. " +
	"Sugestões opcionais ficam fora de \"issues\"."

// DiscoveryStage maps the repository before any implementation. Its report is
// cached per repository for the session and shared with every other stage.
var DiscoveryStage = Stage{
	ID:    "discovery",
	Title: "Discovery do repositório",
	Role:  "Staff Engineer (Discovery)",
	Tier:  provider.TierStandard,
	System: "Você é um Staff Engineer fazendo o discovery de um repositório existente antes de qualquer desenvolvimento. " +
		"Apenas leia e analise; não modifique, crie nem apague arquivos.",
	Task: "Analise o repositório no diretório atual e produza um relatório em Markdown com, para cada seção/módulo " +
		"(raiz, frontend, backend, serviços, infra etc.):\n" +
		"1. Linguagens, frameworks e versões relevantes.\n" +
		"2. Comandos de instalação, build, testes, lint e execução local.\n" +
		"3. Arquitetura, organização de pastas e convenções de código observadas.\n" +
		"4. Onde ficam e como são escritos os testes (unitários, integração, E2E).\n" +
		"5. Riscos ou pontos de atenção para quem for implementar novas funcionalidades.\n" +
		"Seja objetivo; este relatório será o contexto fixo de todos os agentes desta sessão.",
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
		System:   "Você mapeia o código existente antes de qualquer alteração. Apenas leia e pesquise; não modifique arquivos.",
		Task: "Encontre no repositório o que é relevante para esta US e produza um relatório curto:\n" +
			"1. Até 15 arquivos relevantes, com o caminho completo e, em uma linha, por que importam.\n" +
			"2. Funções, componentes e utilitários existentes que devem ser reutilizados.\n" +
			"3. Testes existentes relacionados e onde os novos testes devem ficar.\n" +
			"4. Convenções de código a seguir.\n" +
			"Não proponha a implementação; outros agentes farão isso com base no seu relatório.",
	},
	{
		ID:    "spec",
		Title: "Especificação EDD e contratos TDD",
		Role:  "Staff Spec Engineer",
		Tier:  provider.TierStandard,
		System: "Você é um Staff Software Engineer especialista em EDD (Engineering Design Document) e TDD. " +
			"Gere a especificação técnica: diagramas textuais, schemas de dados, endpoints e casos de teste unitários e de integração.",
		Task:      "Converta a User Story em contratos técnicos (EDD) e plano de testes (TDD), com um ID para cada caso de teste.",
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
		System: "Você é o Arquiteto Principal de Soluções. " +
			"Analise o EDD e detalhe a divisão de tarefas e dependências para os subagentes de Frontend, Backend, Data Science, Security e Testes.",
		Task: "Distribua as responsabilidades técnicas e dependências entre os subagentes.",
	},
	{
		ID:          "frontend",
		Title:       "Frontend e UI/UX",
		Role:        "Frontend Specialist",
		Tier:        provider.TierStandard,
		Selectable:  true,
		Implementer: true,
		System: "Você é o subagente de Frontend e UI/UX. " +
			"Produza código moderno, limpo, responsivo e acessível (WCAG 2.2 AA).",
		Task: "Implemente a camada visual, os componentes e a integração de interface conforme o EDD e o plano do arquiteto.",
	},
	{
		ID:          "backend",
		Title:       "Backend e dados",
		Role:        "Backend & ML Engineer",
		Tier:        provider.TierStandard,
		Selectable:  true,
		Implementer: true,
		System: "Você é o subagente de Backend e Engenharia de Dados. " +
			"Implemente APIs resilientes, regras de negócio determinísticas e estruturas de dados performáticas.",
		Task: "Implemente endpoints, serviços e algoritmos de forma que todos os testes do plano TDD passem.",
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
		System: "Você é o subagente de Segurança (OWASP Top 10) e testes automatizados. " +
			"Verifique sanitização de entradas, headers seguros, autenticação/autorização e a qualidade dos testes unitários. " +
			"Os comandos de teste já são executados pelo orquestrador; você apenas lê e analisa.",
		Task: "Analise vulnerabilidades e avalie a suíte de testes unitários da implementação.",
	},
	{
		ID:       "review",
		Title:    "Code review",
		Role:     "Staff Reviewer",
		Tier:     provider.TierHeavy,
		Gate:     true,
		ReadOnly: true,
		Lenses:   reviewLenses,
		System: "Você é um revisor de código sênior e rigoroso. Você não altera arquivos: apenas lê e aponta problemas. " +
			"Avalie SOLID, DRY, Clean Code, concorrência, tratamento de erros e legibilidade com critérios objetivos.",
		Task: "Faça o code review das implementações aplicando esta rubrica; cada violação vai para \"issues\" com arquivo:linha:\n" +
			"1. Legibilidade: respeita o limite de linha, nada minificado ou compactado, funções curtas com uma responsabilidade, nomes descritivos.\n" +
			"2. Aderência ao EDD: contratos, estados, códigos de erro e estrutura de pastas exatamente como especificados.\n" +
			"3. Testes: cada critério de aceitação coberto, com o ID do caso de teste no nome; nenhum teste com expectativa contrária à especificação.\n" +
			"4. Robustez: entradas validadas, erros tratados, sem duplicação relevante.\n" +
			"5. Interface (quando houver): acessível, responsiva e sem elementos sobrepondo controles.\n" +
			"Inclua um scorecard por critério.",
	},
	{
		ID:         "e2e",
		Title:      "QA E2E com Playwright",
		Role:       "E2E QA Lead",
		Tier:       provider.TierStandard,
		Selectable: true,
		Gate:       true,
		System: "Você é um engenheiro de QA especialista em Playwright e testes E2E. " +
			"Valide o comportamento da aplicação em navegador headless simulando fluxos reais.",
		Task: "Suba a aplicação localmente, execute os testes Playwright e verifique as asserções de tela e rede.",
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
	b.WriteString("## Regras de entrega (verificadas automaticamente pelo orquestrador)\n\n")
	if len(r.Protected) > 0 {
		fmt.Fprintf(b, "- Arquivos de entrada, somente leitura: não edite, mova nem apague %s. Alterá-los reprova a etapa.\n",
			strings.Join(quoteAll(r.Protected), ", "))
	}
	if len(r.Checks) > 0 {
		b.WriteString("- Antes de concluir, rode e faça passar os comandos do projeto (o orquestrador os executa e reprova falhas):\n")
		for _, c := range r.Checks {
			fmt.Fprintf(b, "  - %s: `%s`\n", c.Name, c.Run)
		}
	}
	if r.MaxLine > 0 {
		fmt.Fprintf(b, "- Código legível: linhas de no máximo %d caracteres, nada minificado ou compactado, "+
			"funções curtas e nomes descritivos.\n", r.MaxLine)
	}
	b.WriteString("- Cada teste automatizado cita no nome o ID do caso de teste da especificação (ex.: `UT-CART-01`).\n")
	b.WriteString("- Não declare nada como concluído sem executar; termine com a saída resumida dos testes.\n\n")
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
		fmt.Fprintf(&b, "## Contexto do repositório (discovery da sessão)\n\nRepositório: `%s`\n\nStacks detectadas:\n%s\n\n",
			discovery.Path, workspace.FormatStacks(discovery.Stacks))
		if summary := strings.TrimSpace(discovery.Summary); summary != "" {
			fmt.Fprintf(&b, "### Relatório de discovery\n\n%s\n\n", clip(summary, maxArtifactBytes))
		}
		b.WriteString("Use este contexto; não refaça o discovery. Siga as stacks e convenções existentes.\n\n")
	}
	writeInstructions(&b, sc.Instructions)

	if sc.Preamble != "" {
		fmt.Fprintf(&b, "## Contexto do documento\n\n%s\n\n", clip(sc.Preamble, maxArtifactBytes))
	}
	if sc.Total > 1 {
		fmt.Fprintf(&b, "## Sequência de desenvolvimento\n\nEsta é a US %d de %d.", sc.Position, sc.Total)
		if len(sc.Delivered) > 0 {
			b.WriteString(" As US abaixo já foram implementadas no repositório nesta sequência; reutilize e preserve o que foi entregue:\n")
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
		b.WriteString("## Artefatos das etapas anteriores desta US\n\n")
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
		fmt.Fprintf(b, "## Contexto do repositório\n\nRepositório: `%s`\n\nStacks detectadas:\n%s\n\n",
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
		fmt.Fprintf(b, "## Instruções do projeto (obrigatórias)\n\n%s\n\n", text)
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
	fmt.Fprintf(&b, "\n## Sua tarefa\n\nComo %s, corrija no repositório exclusivamente os problemas listados acima, "+
		"sem alterar comportamentos não relacionados. Outros problemas do relatório estão atribuídos a outros agentes; não os corrija. "+
		"Rode os testes afetados e, ao final, liste o que foi alterado em cada item.", stage.Role)
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
	fmt.Fprintf(&b, "## Sua tarefa\n\nVerificação (rodada %d): confirme no repositório se cada problema acima foi resolvido e se as correções "+
		"não introduziram novas falhas. %s", round, gate.Task)
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
