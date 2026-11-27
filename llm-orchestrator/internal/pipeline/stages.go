package pipeline

import (
	"cmp"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/skills"
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
	// Rules appends the shared engineering rules to System.
	Rules bool

	// Assessor marks the stage that checks what the repository already
	// implements (Passo 0); its decision may close or block the story.
	Assessor bool
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
	// Acceptor marks the functional QA gate: it reads the executed evidence
	// and its scenarios are held to it.
	Acceptor bool
	// Conditional selectable stages run only when the plan names them or the
	// risk requires them: neither the default nor the fallback plan does.
	Conditional bool
	// Specialist stages answer with the specialty contract (requirements and
	// how each is verified), which the implementers and QA receive.
	Specialist bool
	// Measured gates get the benchmark commands' output first and must judge
	// from it.
	Measured bool
	// Commands names the policy commands a verifier runs instead of the
	// project checks: "migrations" for MigrationChecks.
	Commands string
	// Verifier stages call no model: the orchestrator runs the project's
	// commands and static checks, and failures go back to the implementers.
	Verifier bool
	// ReadOnly stages run the CLI in a mode that cannot change files, so
	// planners and reviewers are kept from editing by the toolset, not only
	// by the prompt.
	ReadOnly bool
	// Diagnose stages stay unable to edit files but may run the listed shell
	// commands, to reproduce a failure.
	Diagnose bool
	// Lenses split the stage into parallel reviews, each with its own focus;
	// their verdicts are merged.
	Lenses []Lens
	// Skills are the bundled playbooks (internal/skills) the stage reads.
	Skills []string
	// Inputs are the earlier stages whose output the prompt carries; an
	// implementer's ID also covers its streams. Nil forwards every step.
	Inputs []string
}

// Lens is one focus of a parallel review.
type Lens struct {
	ID    string
	Title string
	Focus string
	// Requires names an agent the plan must include for the lens to run.
	Requires string
	// Skills are the playbooks this reviewer reads instead of the stage's:
	// each lens reads only its own, since every lens is a separate agent.
	Skills []string
}

// reviewLenses are the perspectives of the code review, run in parallel.
var reviewLenses = []Lens{
	{
		ID:    "spec",
		Title: "Aderência à especificação",
		Focus: "Todo requisito, critério, contrato, estado e código de erro da US/EDD implementado exatamente? " +
			"Defenda o usuário: aponte requisito ausente, parcial ou alterado.",
		Skills: []string{"code-review-and-quality"},
	},
	{
		ID:    "simplicity",
		Title: "Simplicidade e legibilidade",
		Focus: "Duplicação, código morto, abstração ou try/catch sobrando, helper existente não reutilizado, " +
			"import faltando, trecho apagado sem motivo, estilo diferente do projeto, código minificado. " +
			"Padrão de projeto do plano ignorado; componente acoplado. Nome que não revela intenção ou abreviado; " +
			"boolean sem forma de pergunta (is/has), lista no singular, função sem verbo. " +
			"Função com mais de uma responsabilidade, if/else aninhado sem guard clause. Tamanho de função e número de argumentos: " +
			"a convenção do projeto manda; sem convenção, ~20 linhas e 3 argumentos são referência, não regra. " +
			"Estilo e nomes vão com category \"estilo\".",
		Skills: []string{"code-simplification"},
	},
	{
		ID:    "structure",
		Title: "Estrutura",
		Focus: "Modo revisão da skill `structural-health`: ligação escondida, ligação desnecessária, sobra e caso especial " +
			"nos arquivos desta US e em quem depende deles. Estilo e nomes são de outro revisor.",
		Skills: []string{"structural-health"},
	},
	{
		ID:     "tests",
		Title:  "Testes",
		Focus:  "Cada critério e caso de teste da especificação coberto, com ID no nome? Expectativa contrária à especificação, teste frágil ou vazio.",
		Skills: []string{"test-driven-development", "test-quality"},
	},
	{
		ID:       "ux",
		Title:    "Interface e acessibilidade",
		Focus:    "WCAG 2.2 AA (rótulos, foco, ARIA válido, contraste), celular, elemento cobrindo controle, estados de carregamento/erro/vazio, consistência visual.",
		Requires: "frontend",
		Skills:   []string{"frontend-ui-engineering"},
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
	"```json\n{\"agents\": [\"frontend\", \"backend\", \"security\", \"e2e\", \"docs\"], " +
	"\"pattern\": \"padrão de projeto e por quê\", " +
	"\"streams\": [{\"id\": \"api-carrinho\", \"agent\": \"backend\", \"title\": \"API do carrinho\", \"scope\": \"o que entrega\", " +
	"\"files\": [\"src/cart/\"], \"depends_on\": [], \"resources\": [\"contrato POST /cart\"]}], " +
	"\"impact\": {\"contracts\": [\"API, evento ou schema alterado ou usado\"], \"consumers\": [\"quem usa o que muda\"], " +
	"\"migrations\": [\"mudança de schema ou dados\"], \"shared_resources\": [\"banco, fila, serviço, porta\"], \"rollback\": \"como desfazer ou recuperar\"}, " +
	"\"acceptance\": [{\"criterion\": \"critério da US\", \"scenario\": \"dado/quando/então\", \"method\": \"teste unitário | integração | e2e | inspeção\", \"negative\": false}], " +
	"\"non_functional\": [{\"kind\": \"desempenho\", \"requirement\": \"o que a US exige\", \"slo\": \"meta mensurável ou vazio se a US não define\"}], " +
	"\"reason\": \"motivo de cada escolha\"}\n```\n" +
	"\"agents\": só o necessário. frontend = interface; backend = API, dados, regras; " +
	"security = auth, dados sensíveis, pagamento, entrada externa; e2e = fluxo de tela; " +
	"ux = jornada visual nova ou ambígua (protótipo antes da implementação); data = mudança de schema ou de dados; " +
	"performance = requisito de desempenho com meta; " +
	"docs = documentação para cliente final (uso mudou) ou mantenedor (fluxo, integração, configuração novos). " +
	"Só interface: sem backend. Mudança simples: sem security, e2e nem docs. Code review sempre roda.\n" +
	"\"streams\": frentes de implementação (agent frontend ou backend). Frentes sem depends_on entre si e com files disjuntos " +
	"rodam em paralelo, cada uma em cópia isolada do repositório, e são integradas no fim. " +
	"depends_on só se a frente precisa do código pronto de outra; contrato do EDD basta para frontend e backend andarem juntos. " +
	"resources: contrato, migration, banco ou serviço que a frente altera; frentes com o mesmo recurso não rodam juntas. " +
	"impact: consumidores e contratos além dos arquivos editados; com migration, diga como reverter. " +
	"acceptance: um cenário por critério, inclusive negativos, com o método de comprovação. " +
	"non_functional: só o que a US pede; sem meta na US, slo vazio (não invente número). " +
	"Nada a paralelizar: omita streams."

const assessInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"decision\": \"reuse\" | \"extend\" | \"refactor\" | \"implement\" | \"block\", \"reason\": \"evidência curta\", " +
	"\"criteria\": [{\"criterion\": \"critério de aceite\", \"status\": \"existente\" | \"parcial\" | \"ausente\" | \"ambiguo\", \"evidence\": \"arquivo:linha ou por que é ambíguo\"}], " +
	"\"pending_decisions\": [{\"question\": \"decisão de produto que a US não define\", \"options\": [\"alternativa\"], \"impact\": \"o que muda conforme a resposta\"}], " +
	"\"change_type\": \"bug\" | \"feature\" | \"refactor\" | \"trivial\", \"change_reason\": \"obrigatório para trivial: por que não muda comportamento\"}\n```\n" +
	"reuse = existe e atende todos os critérios de aceite (US encerrada sem implementar); " +
	"extend = existe em parte (só o delta); refactor = existe com modelo incompatível; " +
	"implement = não existe e dependências prontas; block = dependência ausente. " +
	"Um item em criteria por critério de aceite. pending_decisions só para decisão de negócio que o código e a US não respondem; " +
	"não invente a resposta nem reescreva a US. reuse e block só com evidência no código (arquivo:linha). " +
	"change_type: bug = comportamento errado a corrigir; refactor = mesma conduta, outra estrutura; " +
	"trivial = texto, configuração ou ajuste sem mudança de comportamento; feature = o resto."

const specInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"tests\": [{\"id\": \"UT-CART-01\", \"description\": \"o que verifica\", \"kind\": \"unit\" | \"integration\" | \"e2e\"}]}\n```\n" +
	"Documento já tem IDs de teste: use os mesmos. Caso novo ganha ID novo: nunca repita um ID que os testes do repositório já usam " +
	"(confira nos arquivos de teste). Orquestrador confere cada ID nos arquivos de teste, e caso que já passava antes da US não comprova ID repetido."

const verdictInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"status\": \"approved\" | \"changes_requested\", \"issues\": [{\"agent\": \"frontend\" | \"backend\", \"severity\": \"alta\" | \"média\" | \"baixa\", " +
	"\"category\": \"comportamento\" | \"segurança\" | \"teste\" | \"estrutura\" | \"estilo\", " +
	"\"description\": \"problema e correção\", \"location\": \"arquivo:linha\"}]}\n```\n" +
	"changes_requested só para o que bloqueia entrega. Cada issue com o agente que corrige. Sugestão opcional fora de issues. " +
	"estilo (nome, formatação, tamanho sem convenção do projeto) é registrado como recomendação, não bloqueia."

// engineeringRules are the implementers' working rules, adapted from the
// Codebuff/Freebuff orchestrator guidelines.
const engineeringRules = "Regras:\n" +
	"- Leia código vizinho, testes e config antes de editar. Siga convenções existentes.\n" +
	designRules +
	"- Não assuma biblioteca: confirme no manifesto (package.json, go.mod…) antes de usar.\n" +
	"- Mudança mínima, solução simples.\n" +
	"- Reutilize helpers e componentes existentes. Não reimplemente.\n" +
	"- Passo 0 diz o que já existe: implemente só o que falta, não reescreva o que já atende.\n" +
	"- Bug: primeiro um teste que falhe pelo comportamento errado, depois a correção; o orquestrador roda esse teste " +
	"no código anterior à correção e exige que falhe lá. Funcionalidade: cenários relevantes, inclusive negativos. " +
	"Refatoração: testes existentes continuam e passam. Edição trivial sem mudança de comportamento: sem teste novo.\n" +
	"- Não faça varreduras cegas ou recursivas com grep/find no repositório inteiro. Use o grafo graphify e inspecione apenas arquivos relevantes.\n" +
	"- Mudou símbolo exportado: atualize todas as referências.\n" +
	"- Sem git commit/push/branch (orquestrador versiona), sem instalação global, sem script de produção.\n" +
	"- Orquestrador roda testes, lint e build depois de você e devolve só as falhas. " +
	"Se rodar teste, só o da área alterada e com saída curta (`| tail -n 50`).\n" +
	"- Shell: só os comandos de verificação e os liberados pela política. Precisou de outro (ex.: instalar dependência)? " +
	"Diga no resumo; não contorne.\n" +
	browserRule + processRule +
	"- Resumo final curto: poucas palavras por mudança."

// designRules state the design pattern and clean-code conventions the
// implementers follow, in the idiom of the project's language.
const designRules = "- Padrão de projeto: o do projeto; sem padrão, o do plano do arquiteto. " +
	"Componentes e módulos independentes, reutilizáveis, desacoplados; regra de negócio não depende de UI nem de banco.\n" +
	"- Nomes revelam intenção, sem abreviação (userRepository, não uRep), no estilo da linguagem. " +
	"Variável = substantivo; boolean = pergunta (isActive, hasPermission); coleção no plural (products, userIds); " +
	"função = verbo + substantivo (calculateTotal). Um verbo por conceito no projeto (get, não get/fetch/retrieve).\n" +
	"- Funções: uma responsabilidade (precisou de \"e\" para descrever, divida), até ~20 linhas, até 3 argumentos " +
	"(mais que isso: objeto de configuração). Guard clauses e retorno antecipado em vez de if/else aninhado.\n"

// browserRule keeps agents off the system Chrome: launched inside the CLI
// sandbox it aborts while registering with the window server, and macOS shows
// a "Google Chrome quit unexpectedly" dialog for every attempt.
const browserRule = "- Navegador de teste: Chromium do Playwright em headless (`npx playwright install chromium`). " +
	"Nunca `channel: 'chrome'`/`msedge` nem Chrome do sistema; se a config usar, troque para o Chromium.\n"

// processRule keeps agents from hanging the run: a foreground dev server or
// watch-mode test never returns, and the orchestrator kills the CLI at its
// time limit. Killing by name is banned: other stories' agents run on the
// same machine and `pkill -f` matched one of them.
const processRule = "- Servidor (ng serve, npm start, vite…) só em background, com espera por porta e encerrado no fim. " +
	"Testes sem modo watch (`CI=1`, `--watch=false`, `--run`) e com timeout.\n" +
	"- Encerrar processo só pelo PID que você iniciou (`cmd & echo $! > pidfile` … `kill $(cat pidfile)`) " +
	"ou pela porta (`lsof -ti tcp:PORTA -sTCP:LISTEN | xargs kill`). Nunca `pkill`, `killall` nem kill por nome: " +
	"outros agentes rodam na mesma máquina.\n"

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
		"3. Arquitetura e padrão de projeto em uso (componentes, Clean Architecture, MVC, microsserviços…), pastas, " +
		"convenções de nomes. Sem padrão claro: diga.\n" +
		"4. Testes: onde ficam, como são escritos.\n" +
		"5. Riscos para quem for implementar.\n" +
		"Será o contexto fixo de todos os agentes.",
}

// implementerInputs and reviewInputs are what each kind of stage reads from
// the earlier ones: implementers the findings and the plan; reviewers the plan
// and what was implemented. Project check reports never go forward.
var (
	implementerInputs = []string{"context", "spec", "threat-model", "arch", "ux", "data"}
	reviewInputs      = []string{"spec", "threat-model", "arch", "frontend", "backend"}
)

// DefaultStages is the Passo 0 → EDD → architecture → implementation →
// executable checks → review → E2E → documentation flow. The architect may
// split the implementation into fronts that run in parallel. Fixes requested
// by the reviewers re-run the project checks inside their own fix loop.
var DefaultStages = []Stage{
	{
		ID:       "context",
		Title:    "Passo 0: o que já existe",
		Role:     "Context Scout",
		Tier:     provider.TierStandard,
		Assessor: true,
		ReadOnly: true,
		Inputs:   []string{},
		Skills:   []string{"story-readiness"},
		System:   "Verifica o que o repositório já implementa antes de qualquer mudança. Só leitura.",
		Task: "Relatório curto:\n" +
			"1. Busque entidades, módulos, rotas, migrations e testes com o nome do domínio da US.\n" +
			"2. Para cada critério de aceite: já atendido, parcial ou ausente, com arquivo:linha.\n" +
			"3. Até 15 arquivos relevantes: caminho + por que importa (uma linha).\n" +
			"4. Funções, componentes, utilitários a reutilizar; testes relacionados e onde ficam os novos.\n" +
			"5. Dependências da US prontas? Decisão de produto em aberto?\n" +
			"Não proponha implementação.",
	},
	{
		ID:     "spec",
		Title:  "Especificação EDD e contratos TDD",
		Role:   "Staff Spec Engineer",
		Tier:   provider.TierStandard,
		System: "Engenheiro de especificação (EDD + TDD). Só leitura.",
		Task: "US vira EDD só do que falta segundo o Passo 0: contratos, schemas, endpoints, estados, erros. " +
			"Plano TDD: um ID por caso de teste. Sem código de implementação.",
		Specifier: true,
		ReadOnly:  true,
		Skills:    []string{"spec-driven-development"},
		Inputs:    []string{"context"},
	},
	{
		ID:          "threat-model",
		Title:       "Modelagem de ameaças",
		Role:        "Threat Modeler",
		Tier:        provider.TierHeavy,
		Selectable:  true,
		Conditional: true,
		Specialist:  true,
		ReadOnly:    true,
		Inputs:      []string{"context", "spec"},
		System:      "Modelagem de ameaças (STRIDE) de mudança sensível, antes do plano. Só leitura; não implementa.",
		Task: "Ativos, fronteiras de confiança e fluxos de dados que a US toca. Para cada ameaça relevante (falsificação, " +
			"adulteração, repúdio, vazamento, negação de serviço, elevação de privilégio): o controle que a implementação precisa ter " +
			"e o teste negativo que o comprova (ex.: acesso sem permissão recusado). Só o que esta US muda.",
		Skills: []string{"security-and-hardening"},
	},
	{
		ID:       "arch",
		Title:    "Arquitetura e plano de delegação",
		Role:     "Chief Architect",
		Tier:     provider.TierHeavy,
		Planner:  true,
		ReadOnly: true,
		System:   "Arquiteto principal. Só leitura.",
		Task: "Curto e concreto:\n" +
			"1. Padrão de projeto: o do projeto (Discovery); sem padrão, o mais adequado — componentes independentes e reutilizáveis " +
			"no frontend, Clean Architecture (regra de negócio no centro, UI e banco na borda) no backend; " +
			"microfrontends/microsserviços só se o projeto já separa deployables por domínio. Diga onde cada peça fica.\n" +
			"2. Mapeie as atividades e quais podem rodar em paralelo: frentes com arquivos disjuntos e sem depender do código da outra.\n" +
			"3. Por frente: agente, o que faz, arquivos, dependências.\n" +
			"4. Documentação necessária (cliente final ou mantenedor)? Se sim, inclua docs.",
		Skills: []string{"planning-and-task-breakdown", "api-and-interface-design", "change-impact"},
		Inputs: []string{"context", "spec"},
	},
	{
		ID:          "ux",
		Title:       "UX e protótipo",
		Role:        "UX Specialist",
		Tier:        provider.TierStandard,
		Selectable:  true,
		Conditional: true,
		Specialist:  true,
		ReadOnly:    true,
		Inputs:      []string{"context", "spec", "arch"},
		System:      "Especialista de UX antes da implementação, quando a jornada visual é nova ou ambígua. Só leitura; não implementa.",
		Task: "Protótipo em texto da jornada: telas, hierarquia, estados (carregando, vazio, erro, sucesso), mensagens e foco do teclado, " +
			"no padrão visual do projeto. Cada requisito com a verificação (e2e, teste de componente ou inspeção visual). " +
			"Ambiguidade que só o produto decide: diga, não invente.",
		Skills: []string{"frontend-ui-engineering"},
	},
	{
		ID:          "data",
		Title:       "Dados e migrations",
		Role:        "Data & Migrations Specialist",
		Tier:        provider.TierHeavy,
		Selectable:  true,
		Conditional: true,
		Specialist:  true,
		ReadOnly:    true,
		Inputs:      []string{"context", "spec", "arch"},
		System:      "Especialista de dados. Planeja a mudança de schema ou de dados antes da implementação. Só leitura; não implementa.",
		Task: "Para cada migration: compatibilidade com o código em produção durante o deploy (expandir e contrair), integridade " +
			"(constraints, dados existentes, nulos), volume e bloqueio de tabela, e como reverter ou recuperar. " +
			"Requisito com a verificação que o runner faz em banco descartável (aplicar, conferir, reverter, aplicar de novo).",
		Skills: []string{"safe-migrations"},
	},
	{
		ID:          "frontend",
		Title:       "Frontend e UI/UX",
		Role:        "Frontend Specialist",
		Tier:        provider.TierStandard,
		Selectable:  true,
		Implementer: true,
		System: "Agente de frontend. Interface acessível (WCAG 2.2 AA), responsiva e caprichada: hierarquia, contraste, " +
			"estados de hover/foco/erro/vazio, transições discretas.",
		Rules:  true,
		Task:   "Implemente interface e componentes conforme EDD e plano do arquiteto.",
		Skills: []string{"incremental-implementation", "test-driven-development", "frontend-ui-engineering"},
		Inputs: implementerInputs,
	},
	{
		ID:          "backend",
		Title:       "Backend e dados",
		Role:        "Backend & ML Engineer",
		Tier:        provider.TierStandard,
		Selectable:  true,
		Implementer: true,
		System:      "Agente de backend e dados. APIs resilientes, regras determinísticas, entrada validada.",
		Rules:       true,
		Task:        "Implemente endpoints, serviços e algoritmos até todos os testes do plano TDD passarem.",
		Skills:      []string{"incremental-implementation", "test-driven-development", "api-and-interface-design"},
		Inputs:      implementerInputs,
	},
	{
		ID:       "checks",
		Title:    "Verificação executável",
		Role:     "Portão automático",
		Verifier: true,
		Task:     "Executa os comandos do projeto, checagens de sintaxe e legibilidade e a rastreabilidade dos testes.",
	},
	{
		ID:          "migrations",
		Title:       "Verificação das migrations",
		Role:        "Portão automático",
		Verifier:    true,
		Selectable:  true,
		Conditional: true,
		Commands:    "migrations",
		Task:        "Executa os comandos de migration da política em banco descartável: aplicar, conferir integridade, reverter ou recuperar.",
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
		Skills:     []string{"security-and-hardening"},
		Inputs:     reviewInputs,
	},
	{
		ID:          "performance",
		Title:       "Desempenho",
		Role:        "Performance Engineer",
		Tier:        provider.TierHeavy,
		Selectable:  true,
		Conditional: true,
		Gate:        true,
		Measured:    true,
		ReadOnly:    true,
		Inputs:      []string{"spec", "arch", "frontend", "backend"},
		System:      "Especialista de desempenho. Só leitura. Julga pela medição que o orquestrador executou, nunca por intuição.",
		Task: "Compare cada medição com o requisito não funcional da US. Gargalo só com a medição que o mostra (consulta, " +
			"concorrência, alocação, I/O) e a correção. Sem medição: não aprove.",
	},
	{
		ID:       "review",
		Title:    "Code review",
		Role:     "Staff Reviewer",
		Tier:     provider.TierStandard,
		Gate:     true,
		ReadOnly: true,
		Lenses:   reviewLenses,
		System:   "Revisor sênior rigoroso. Só leitura: aponta, não corrige. Só crítica, sem elogios. Nada a apontar: diga em uma frase.",
		Task: "Revise as mudanças desta US. Cada violação vai em issues com arquivo:linha.\n" +
			"1. Legível: limite de linha, nada minificado, funções curtas de uma responsabilidade, nomes claros, padrão de projeto do plano.\n" +
			"2. EDD: contratos, estados, erros e pastas como especificado.\n" +
			"3. Testes: cada critério coberto, ID no nome, expectativas corretas.\n" +
			"4. Robustez: entrada validada, erro tratado, sem duplicação.\n" +
			"5. Interface: acessível, responsiva, nada cobrindo controle.\n" +
			"6. Estrutura: modo revisão da skill `structural-health` (arquivos desta US e quem depende deles).",
		Skills: []string{"code-review-and-quality", "code-simplification", "structural-health", "test-quality"},
		Inputs: reviewInputs,
	},
	{
		ID:         "e2e",
		Title:      "QA E2E com Playwright",
		Role:       "E2E QA Lead",
		Tier:       provider.TierStandard,
		Selectable: true,
		Gate:       true,
		System:     "QA E2E com Playwright em navegador headless. Escreva testes E2E; não altere código da aplicação.\n\n" + browserRule + processRule,
		Task: "Escreva e rode os testes Playwright dos fluxos da US (ID do caso no nome) e verifique tela e rede. " +
			"A aplicação sobe pelo `webServer` do playwright.config (rode com `npx playwright test`).",
		Inputs: reviewInputs,
	},
	{
		ID:       "acceptance",
		Title:    "QA funcional e aceitação",
		Role:     "Functional QA",
		Tier:     provider.TierStandard,
		Gate:     true,
		Acceptor: true,
		ReadOnly: true,
		System: "QA funcional. Só leitura: não altera código nem testes. Julga a jornada e as regras de negócio pelos critérios " +
			"originais da US, não pela descrição da implementação. A evidência executada vem do orquestrador; você a interpreta.",
		Task: "Para cada critério de aceite e cenário do plano, inclusive negativos e estados de erro e vazio: " +
			"aceito, recusado ou sem evidência, citando o caso executado que comprova (ID em tests) ou, para inspeção, arquivo:linha. " +
			"Regra de negócio sem caso que a exercite é sem evidência. Cenário que a implementação não atende é recusado, com a correção.",
		Inputs: []string{"context", "spec", "arch", "e2e"},
		Skills: []string{"functional-acceptance"},
	},
	{
		ID:         "docs",
		Title:      "Documentação do fluxo",
		Role:       "Technical Writer",
		Tier:       provider.TierStandard,
		Selectable: true,
		System: "Documenta o fluxo entregue. Edita só documentação (README, docs/, CHANGELOG, guia de uso); não altera código. " +
			"Texto curto, exemplos reais do código, nada inventado. Sem git commit/push/branch (orquestrador versiona).",
		Task: "Documente o que esta US entregou, só para quem precisa:\n" +
			"1. Cliente final (uso, tela ou fluxo mudou): passo a passo de uso, mensagens de erro e o que fazer.\n" +
			"2. Desenvolvedor de manutenção: fluxo ponta a ponta (entrada, camadas, saída), padrão de projeto, " +
			"contratos, configuração, pontos de extensão, como testar.\n" +
			"Siga onde o projeto já documenta (README, docs/, ADR, CHANGELOG); sem lugar, crie docs/<id-da-us>.md. " +
			"Nada a documentar: diga em uma frase e não crie arquivo.",
		Skills: []string{"documentation-and-adrs"},
		Inputs: reviewInputs,
	},
}

// auditNotice replaces "the changes of this story" for code already written.
const auditNotice = "## Modo auditoria\n\nEsta US já foi desenvolvida: não há mudança nova a revisar. " +
	"Localize o código que a implementa (grafo e Discovery), revise-o por inteiro e só aponte o que ainda é problema. " +
	"Ninguém vai corrigir nesta execução: cada issue vira pendência no relatório, então seja específico (arquivo:linha e correção).\n\n"

// maxArtifactBytes caps how much of each previous stage's output is forwarded.
const maxArtifactBytes = 4_000

// maxChangedListed bounds the changed files a gate's prompt lists.
const maxChangedListed = 60

func (sc storyContext) artifactBytes() int {
	if sc.ArtifactBytes > 0 {
		return sc.ArtifactBytes
	}
	return maxArtifactBytes
}

// writeChanged points a gate at what the story changed. A longer list is
// cut and says so: the gate still reviews the whole change.
func writeChanged(b *strings.Builder, files []string) {
	if len(files) == 0 {
		return
	}
	b.WriteString("## Arquivos alterados nesta US\n\n")
	for _, f := range files[:min(len(files), maxChangedListed)] {
		fmt.Fprintf(b, "- `%s`\n", f)
	}
	if len(files) > maxChangedListed {
		fmt.Fprintf(b, "- … e mais %d (lista truncada; revise a mudança inteira)\n", len(files)-maxChangedListed)
	}
	b.WriteString("\n")
}

// maxPreambleBytes caps how much document context is forwarded to stages.
const maxPreambleBytes = 2_000

// buildDiscoveryPrompt gives the discovery agent the deterministic scan as a
// starting point.
func buildDiscoveryPrompt(task, repo string, stacks []workspace.Stack, graph, instructions string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Repositório\n\n`%s`\n\n## Manifestos detectados automaticamente\n\n%s\n\n",
		repo, workspace.FormatStacks(stacks))
	writeGraph(&b, graph)
	if report := strings.TrimSpace(workspace.GraphReport(graph)); report != "" {
		fmt.Fprintf(&b, "Relatório do grafo (hubs e comunidades):\n\n%s\n\n", clip(report, maxArtifactBytes))
	}
	writeInstructions(&b, instructions)
	fmt.Fprintf(&b, "## Sua tarefa\n\n%s", task)
	return b.String()
}

// buildIncrementalDiscoveryPrompt turns a full discovery prompt into an
// update of the previous report: the agent reads only what changed.
func buildIncrementalDiscoveryPrompt(full, previous string, changes []workspace.FileChange) string {
	var b strings.Builder
	b.WriteString(full)
	fmt.Fprintf(&b, "\n\n## Atualização incremental\n\nJá existe um discovery deste repositório:\n\n%s\n\n", clip(strings.TrimSpace(previous), maxArtifactBytes))
	b.WriteString("Desde então, mudaram estes arquivos de manifesto, dependência, instrução, contrato ou arquitetura:\n\n")
	for _, c := range changes {
		fmt.Fprintf(&b, "- `%s` (%s)\n", c.Path, c.Change)
	}
	b.WriteString("\nLeia só esses arquivos e o que eles afetarem. Mantenha o que continua valendo, corrija o que mudou e devolva o discovery completo atualizado, no mesmo formato.")
	return b.String()
}

// writeGraph points agents at the code graph, cheaper than reading files to
// find where something lives or what a change affects.
func writeGraph(b *strings.Builder, graph string) {
	if graph == "" {
		return
	}
	fmt.Fprintf(b, "### Grafo do código (graphify)\n\n`%s`, atualizado pelo orquestrador (não reconstrua; caminhos relativos à raiz). "+
		"Antes de varrer arquivos, consulte: `graphify query \"<pergunta>\" --graph %[1]s`, "+
		"`graphify explain \"<símbolo>\" --graph %[1]s`, `graphify affected \"<símbolo>\" --graph %[1]s` (impacto de mudança).\n\n", graph)
}

// storyContext places a story within its document and sequence.
type storyContext struct {
	Preamble  string
	Story     stories.Story
	Delivered []stories.Story // stories of earlier waves, already implemented
	Parallel  []stories.Story // stories developed at the same time, on other branches
	Position  int             // 1-based
	Total     int
	Rules     rules
	// Instructions are the project's agent instruction files (AGENTS.md…).
	Instructions string
	// Audit marks a story already developed: the stages review the existing
	// code instead of a change.
	Audit bool
	// AuditNotice is the customized text of the audit notice, if any.
	AuditNotice string
	// Evidence is what the runner proved, given to the functional QA only.
	Evidence string
	// ArtifactBytes caps each earlier stage's output forwarded in the
	// prompt; 0 is maxArtifactBytes. Changed are the files the story changed
	// so far, given to the gates so they look where the change is.
	ArtifactBytes int
	Changed       []string
	// Profiles are the stack profiles the repository chose; Overrides the
	// project's own complements to bundled skills.
	Profiles  []string
	Overrides map[string]SkillOverride
	// PortBase starts the port range of a story run beside others; 0 alone.
	PortBase int
	// Answers are the user's answers to the product decisions the story left
	// open; they come after the story, which stays as written.
	Answers []PendingDecision
}

// Parallel stories share the machine: each gets its own port range so their
// dev servers neither collide nor get killed by another story's cleanup.
const (
	lanePortBase = 4400
	lanePortSpan = 20
)

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
	b.WriteString("- Termine com uma linha por arquivo alterado.\n\n")
}

func quoteAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "`" + s + "`"
	}
	return out
}

// buildPrompt gives a stage the repository discovery, the story (with its
// place in the sequence) and the artifacts of the stages it reads, so agents
// build on each other instead of starting blind. The shared context comes
// first and is the same for every stage of a story, so the CLI's prompt cache
// covers it; what changes per stage comes last.
func buildPrompt(stage Stage, sc storyContext, discovery *workspace.Report, previous []Step) string {
	var b strings.Builder
	writeContext(&b, sc, discovery)
	if previous = stageInputs(stage, previous); len(previous) > 0 {
		b.WriteString("## Etapas anteriores\n\n")
		for _, s := range previous {
			fmt.Fprintf(&b, "### %s (%s)\n\n%s\n\n", s.Title, s.Role, clip(strings.TrimSpace(s.Output), sc.artifactBytes()))
		}
	}
	writeChanged(&b, sc.Changed)
	b.WriteString(sc.Evidence)
	writeSkillsFor(&b, stage.Skills, sc)
	b.WriteString("## Sua tarefa\n\n")
	b.WriteString(stage.Task)
	b.WriteString(stageInstructions(stage))
	return b.String()
}

// stageInputs keeps the steps the stage reads (Stage.Inputs).
func stageInputs(stage Stage, steps []Step) []Step {
	if stage.Inputs == nil {
		return steps
	}
	var out []Step
	for _, s := range steps {
		if slices.Contains(stage.Inputs, s.ID) || (s.Kind == "stream" && slices.Contains(stage.Inputs, s.Agent)) {
			out = append(out, s)
		}
	}
	return out
}

// stageInstructions appends the machine-readable output contract.
func stageInstructions(stage Stage) string {
	switch {
	case stage.Assessor:
		return assessInstructions
	case stage.Specifier:
		return specInstructions
	case stage.Planner:
		return planInstructions
	case stage.Acceptor:
		return acceptanceInstructions
	case stage.Specialist:
		return specialistInstructions
	case stage.Measured:
		return performanceInstructions
	case stage.Gate:
		return verdictInstructions
	}
	return ""
}

// writeContext writes the context shared by every prompt of a story, from
// the most to the least stable: repository, discovery, project instructions
// and delivery rules, document, sequence and the story itself.
func writeContext(b *strings.Builder, sc storyContext, discovery *workspace.Report) {
	writeRepository(b, discovery)
	writeInstructions(b, sc.Instructions)
	writeRules(b, sc.Rules)
	if sc.Preamble != "" {
		fmt.Fprintf(b, "## Contexto do documento\n\n%s\n\n", clip(sc.Preamble, maxPreambleBytes))
	}
	writeSequence(b, sc)
	b.WriteString("## User Story\n\n")
	b.WriteString(strings.TrimSpace(sc.Story.Content))
	b.WriteString("\n\n")
	if len(sc.Answers) > 0 {
		b.WriteString("## Decisões de produto respondidas pelo usuário\n\nValem junto com a US; não as reabra.\n\n")
		for _, a := range sc.Answers {
			fmt.Fprintf(b, "- %s → %s\n", a.Question, a.Answer)
		}
		b.WriteString("\n")
	}
	if sc.Audit {
		b.WriteString(cmp.Or(sc.AuditNotice, auditNotice))
	}
}

// writeRepository writes the repository, its discovery and its code graph.
func writeRepository(b *strings.Builder, discovery *workspace.Report) {
	if discovery == nil {
		return
	}
	fmt.Fprintf(b, "## Repositório\n\n`%s`\n\nStacks:\n%s\n\n",
		discovery.Path, workspace.FormatStacks(discovery.Stacks))
	if summary := strings.TrimSpace(discovery.Summary); summary != "" {
		fmt.Fprintf(b, "### Discovery (não refaça)\n\n%s\n\n", clip(summary, maxArtifactBytes))
	}
	writeGraph(b, discovery.Graph)
}

// writeSequence places the story among the others of the document.
func writeSequence(b *strings.Builder, sc storyContext) {
	if sc.Total <= 1 {
		return
	}
	fmt.Fprintf(b, "## Sequência de desenvolvimento\n\nUS %d de %d.", sc.Position, sc.Total)
	if len(sc.Delivered) > 0 {
		b.WriteString(" Já entregues no repositório (reutilize, não quebre):\n")
		for _, d := range sc.Delivered {
			fmt.Fprintf(b, "- %s\n", d.Title)
		}
	}
	if len(sc.Parallel) > 0 {
		b.WriteString("\nEm desenvolvimento em paralelo, em outras branches (não implemente o escopo delas):\n")
		for _, p := range sc.Parallel {
			fmt.Fprintf(b, "- %s\n", p.Title)
		}
		fmt.Fprintf(b, "\nServidores locais desta US (dev server, API, E2E): só portas %d a %d; as outras US usam outras faixas.\n",
			sc.PortBase, sc.PortBase+lanePortSpan-1)
	}
	b.WriteString("\n\n")
}

// fixSkills guide fix rounds: find the root cause, prove it with a test.
var fixSkills = []string{"debugging-and-error-recovery", "test-driven-development"}

// skillsDir is where agents read the bundled skills; "" when they could not
// be written there. Tests replace it.
var skillsDir = func() string {
	dir, err := skills.Dir()
	if err != nil {
		slog.Warn("skills indisponíveis; prompts seguem sem elas", "err", err)
		return ""
	}
	return dir
}

// writeSkills points the agent at the playbooks of its task. They are files,
// not CLI skills, so every CLI reads them the same way and Claude keeps
// --safe-mode; the router grants read access to skillsDir.
func writeSkills(b *strings.Builder, names []string) { writeSkillsFor(b, names, storyContext{}) }

// writeSkillsFor lists the playbooks to read, each with the project's
// complement when it has one, and the stack profiles the repository chose.
// A complement adds to the playbook; it cannot lower what the playbook,
// the policy or the permissions require.
func writeSkillsFor(b *strings.Builder, names []string, sc storyContext) {
	dir := skillsDir()
	var lines []string
	complemented := false
	for _, n := range names {
		if dir == "" || !skills.Has(n) {
			continue
		}
		lines = append(lines, fmt.Sprintf("- `%s`: `%s`\n", n, skills.File(dir, n)))
		if o, ok := sc.Overrides[n]; ok {
			lines = append(lines, fmt.Sprintf("  - complemento do projeto: `%s`\n", o.Path))
			complemented = true
		}
	}
	for _, p := range sc.Profiles {
		if dir != "" && skills.HasProfile(p) {
			lines = append(lines, fmt.Sprintf("- perfil de stack `%s`: `%s`\n", p, skills.ProfileFile(dir, p)))
		}
	}
	if len(lines) == 0 {
		return
	}
	b.WriteString("## Skills\n\nLeia antes de começar e siga o que se aplica à tarefa (só leitura):\n")
	b.WriteString(strings.Join(lines, ""))
	if complemented {
		b.WriteString("Complemento do projeto acrescenta convenções; não reduz o que a skill, a política ou as permissões exigem.\n")
	}
	b.WriteString("Caminhos citados dentro delas são relativos ao próprio SKILL.md. Saída final continua no formato pedido abaixo.\n\n")
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

// fixTask is how a fix is done, whatever the failure.
const fixTask = "Trabalhe uma causa por vez, na ordem listada:\n" +
	"1. Reproduza a falha com o menor comando possível.\n" +
	"2. Bug sem teste que o prove: escreva o teste que falha.\n" +
	"3. Corrija a causa raiz com a menor mudança. Não mexa em comportamento não relacionado.\n" +
	"4. Prove: rode o teste e veja passar.\n" +
	"Proibido silenciar teste, suprimir erro ou repetir abordagem já refutada. Mude teste só se contrariar a especificação, com motivo. " +
	"Ninguém responde perguntas: sem saída, responda blocked."

// buildFixPrompt asks an implementer to fix what a gate reported.
func buildFixPrompt(task string, gate Stage, sc storyContext, discovery *workspace.Report, plan, report, brief string, issues []Issue, round int) string {
	var b strings.Builder
	writeContext(&b, sc, discovery)
	if plan != "" {
		fmt.Fprintf(&b, "## Plano do arquiteto\n\n%s\n\n", clip(plan, maxArtifactBytes))
	}
	if report = strings.TrimSpace(stripJSONBlocks(report)); report != "" {
		fmt.Fprintf(&b, "## Relatório de %s (rodada %d)\n\n%s\n\n", gate.Title, round, clip(report, sc.artifactBytes()))
	}
	writeSkillsFor(&b, fixSkills, sc)
	if brief != "" {
		b.WriteString(brief)
	}
	b.WriteString("## Correções obrigatórias atribuídas a você\n\n")
	writeIssues(&b, issues)
	fmt.Fprintf(&b, "\nCausas: %s.\n\n## Sua tarefa\n\n%s", causeIDs(issues), task)
	b.WriteString(fixInstructions)
	return b.String()
}

// verifyTask opens the re-evaluation of a gate after a fix.
const verifyTask = "cada problema acima resolvido? Correção quebrou algo?"

// buildVerifyPrompt re-runs a gate after fixes, pointing at what changed.
func buildVerifyPrompt(task string, gate Stage, sc storyContext, discovery *workspace.Report, issues []Issue, fixes []Step, round int) string {
	var b strings.Builder
	writeContext(&b, sc, discovery)
	fmt.Fprintf(&b, "## Problemas apontados na rodada %d\n\n", round-1)
	writeIssues(&b, issues)
	b.WriteString("\n## Correções aplicadas\n\n")
	for _, f := range fixes {
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", f.Title, clip(strings.TrimSpace(f.Output), sc.artifactBytes()))
	}
	writeChanged(&b, sc.Changed)
	b.WriteString(sc.Evidence)
	writeSkillsFor(&b, gate.Skills, sc)
	fmt.Fprintf(&b, "## Sua tarefa\n\nVerificação, rodada %d: %s %s", round, task, gate.Task)
	b.WriteString(stageInstructions(gate))
	return b.String()
}

// buildFormatPrompt asks a gate only for the verdict its previous answer
// lacked: the report is quoted back and nothing is reviewed or changed again.
func buildFormatPrompt(problem, report string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sua resposta anterior não trouxe um veredito válido (%s).\n"+
		"Não revise o código de novo e não altere arquivos. Com base só no relatório abaixo, "+
		"responda apenas com o bloco JSON do veredito. Problema de severidade alta exige changes_requested.\n\n", problem)
	fmt.Fprintf(&b, "## Relatório anterior\n\n%s\n", clip(strings.TrimSpace(report), maxArtifactBytes))
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
	return s[:n] + "\n\n" + truncationMark
}

// truncationMark ends an artifact cut to fit the context budget.
const truncationMark = "[… conteúdo truncado]"
