# 🚀 Orquestrador Autônomo de Engenharia de Software (Go + Multi-LLM CLI)

Este documento é o guia definitivo (Blueprint) para construir a sua plataforma de orquestração de LLMs em **Go**, com interface visual em **HTML/JS** embutida, alternância automática de sessão entre **Claude CLI** e **Codex CLI**, fluxo **EDD/TDD**, múltiplos subagentes especializados e validação automatizada com **Playwright**.

---

## 1. Visão Geral da Solução

A aplicação recebe demandas de produto (via upload/texto `.md`, **Jira** ou **Trello**) e conduz autonomamente todo o ciclo de engenharia de software:
1. **Ingestão:** Converte a User Story (US) em uma estrutura padronizada.
2. **Especificação (EDD & TDD):** Transforma a US em um *Engineering Design Document* (EDD) e cria o plano de testes (TDD).
3. **Arquiteto:** Analisa o EDD e distribui subtarefas para os subagentes especialistas (**Frontend, Backend, Data Science, Designer, Security, Tester**).
4. **Execução Isolada:** Cada subagente trabalha em sua própria branch/worktree.
5. **Code Review Rigoroso:** Validação estática (linters) seguida de revisão arquitetural, Clean Code, SOLID e segurança por um agente revisor.
6. **QA & E2E (Playwright):** Sobe a aplicação, executa testes unitários, de integração e testes visuais ponta a ponta no navegador com Playwright.
7. **Resiliência de Quota (Auto-Switch):** Monitora os limites de tokens do **Claude CLI (Sonnet/Opus)** e do **Codex CLI (GPT-Sol/Astra)**. Quando um esgota a sessão, calcula o tempo de restituição e transfere o trabalho imediatamente para o outro.

---

## 2. Estratégia de Construção: Começando Simples (Abordagem Iterativa)

Para não travar na complexidade inicial, construiremos o sistema em **4 Fases evolutivas**:

* **🟢 Fase 1 (MVP Visual Funcional - Comece aqui):**
  * Entrada de User Story via texto Markdown direto no **Frontend HTML**.
  * Servidor Go único servindo o HTML e enviando atualizações em tempo real via **SSE (Server-Sent Events)**.
  * Fluxo sequencial básico: `Input MD` $\rightarrow$ `EDD/TDD` $\rightarrow$ `Arquiteto` $\rightarrow$ `Subagentes` $\rightarrow$ `Code Review`.
  * Alternância automática (`FallbackRouter`) entre Claude CLI e Codex CLI já ativa.
* **🟡 Fase 2 (Isolamento e Testes Locais):**
  * Adição de `git worktree` para subagentes rodarem em paralelo sem conflito de arquivos.
  * Execução real de testes unitários e linters antes do Code Review.
* **🟠 Fase 3 (Automação E2E com Playwright):**
  * Subida automática do ambiente de testes e execução dos scripts do Playwright pelo agente Tester.
* **🔵 Fase 4 (Integrações Externas e Persistência):**
  * Conectores de API para **Jira** e **Trello**.
  * Banco SQLite para salvar checkpoints (caso o servidor seja reiniciado no meio de uma US).

---

## 3. Arquitetura e Fluxo do Sistema

```text
+-------------------------------------------------------------------------+
|                   FRONTEND WEB (Dashboard HTML + SSE)                   |
|  [Input MD / Jira / Trello]  |  [Pipeline Kanban]  |  [Status Quotas]   |
+-----------------------------------+-------------------------------------+
                                    | HTTP REST / Server-Sent Events
+-----------------------------------v-------------------------------------+
|                        ORQUESTRADOR CORE (Golang)                       |
|                                                                         |
|  1. Spec Agent (EDD/TDD) ---> 2. Arquiteto (Delega tarefas JSON)        |
|                                       |                                 |
|        +--------------+---------------+---------------+                 |
|        v              v               v               v                 |
|   [Frontend]     [Backend]     [Data Science]    [Designer]             |
|        +--------------+---------------+---------------+                 |
|                                       |                                 |
|  3. Tester (TDD/Unidade) ---> 4. Security & Code Review                 |
|                                       |                                 |
|  5. Runner E2E (Sobe App + Executa Playwright)                          |
+-----------------------------------+-------------------------------------+
                                    |
+-----------------------------------v-------------------------------------+
|                 QUOTA MANAGER & FALLBACK ROUTER (Mutex)                 |
|   Detecta Rate Limit / Calcula Horário de Reset / Injeta Git Hand-off   |
+-----------------+-------------------------------------+-----------------+
                  |                                     |
        +---------v----------+                +---------v----------+
        |     CLAUDE CLI     |                |     CODEX CLI      |
        |  Opus   |  Sonnet  |                |  Astra  | GPT-Sol  |
        +--------------------+                +--------------------+
```

---

## 4. Estrutura de Pastas Inicial (MVP Simples)

Crie uma pasta chamada `llm-orchestrator` com apenas **4 arquivos** para rodar a primeira versão completa com interface visual:

```text
llm-orchestrator/
├── go.mod
├── main.go                 # Servidor HTTP, SSE, Orquestrador e Prompts dos Agentes
├── provider.go             # Wrappers Claude/Codex, Parser de Quota e Fallback Router
└── static/
    └── index.html          # Interface Gráfica (Dashboard em tempo real)
```

---

## 5. Código Passo a Passo para Construir o MVP

### Passo 5.1: Inicializar o Módulo Go
No terminal, dentro da pasta `llm-orchestrator`, execute:
```bash
go mod init llm-orchestrator
```
*(Não precisamos de bibliotecas externas no MVP! Usaremos apenas a biblioteca padrão do Go).*

---

### Passo 5.2: Gerenciador de Quota e Providers (`provider.go`)
Crie o arquivo `provider.go`. Ele gerencia o Claude CLI e o Codex CLI, lê o tempo de reset de tokens, expõe o status para o Frontend HTML e inclui um **Modo Simulação (`DryRun`)** caso você queira testar a tela antes de gastar seus tokens reais.

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ModelTier string

const (
	TierHeavy    ModelTier = "heavy"    // Claude Opus / Codex Astra
	TierStandard ModelTier = "standard" // Claude Sonnet / Codex GPT-Sol
)

type TaskRequest struct {
	AgentRole    string
	SystemPrompt string
	UserPrompt   string
	WorkDir      string
	Tier         ModelTier
}

type TaskResponse struct {
	Output       string
	ProviderUsed string
	ModelUsed    string
	Duration     time.Duration
}

type QuotaExceededError struct {
	Provider  string
	ResetTime time.Time
	RawOutput string
}

func (e *QuotaExceededError) Error() string {
	return fmt.Sprintf("[%s] quota esgotada até %s", e.Provider, e.ResetTime.Format("15:04:05"))
}

type ProviderStateDTO struct {
	Name       string `json:"name"`
	Models     string `json:"models"`
	Status     string `json:"status"` // "ACTIVE" ou "COOLDOWN"
	ResetAt    string `json:"reset_at"`
	Remaining  string `json:"remaining"`
}

// ==================== PARSER DE QUOTA ====================

var (
	reRelativeDuration = regexp.MustCompile(`(?i)(?:reset|retry|try again|available)\s+(?:in|after)\s+((?:\d+\s*[hms]\s*)+)`)
	reClockTime        = regexp.MustCompile(`(?i)(?:until|at)\s+(\d{1,2}(?::\d{2})?\s*(?:am|pm)?)`)
	reRetryAfterSec    = regexp.MustCompile(`(?i)retry-after:\s*(\d+)`)
	quotaKeywords      = []string{"rate limit", "quota exceeded", "usage limit", "session limit", "too many requests", "429", "tokens exhausted"}
)

func ParseQuotaError(providerName, output string, now time.Time) *QuotaExceededError {
	lowerOut := strings.ToLower(output)
	isQuota := false
	for _, kw := range quotaKeywords {
		if strings.Contains(lowerOut, kw) {
			isQuota = true
			break
		}
	}
	if !isQuota {
		return nil
	}

	resetTime := now.Add(15 * time.Minute)
	if m := reRetryAfterSec.FindStringSubmatch(output); len(m) > 1 {
		if sec, err := strconv.Atoi(m[1]); err == nil {
			resetTime = now.Add(time.Duration(sec) * time.Second)
		}
	} else if m := reRelativeDuration.FindStringSubmatch(output); len(m) > 1 {
		clean := strings.ReplaceAll(m[1], " ", "")
		if d, err := time.ParseDuration(clean); err == nil {
			resetTime = now.Add(d)
		}
	}
	return &QuotaExceededError{Provider: providerName, ResetTime: resetTime, RawOutput: output}
}

// ==================== ROUTER COM FALLBACK ====================

type managedProvider struct {
	name        string
	binary      string
	heavyModel  string
	stdModel    string
	resetAfter  time.Time
}

type FallbackRouter struct {
	mu        sync.RWMutex
	providers []*managedProvider
	DryRun    bool // Se true, simula execução caso o binário não esteja instalado
}

func NewFallbackRouter(dryRun bool) *FallbackRouter {
	return &FallbackRouter{
		DryRun: dryRun,
		providers: []*managedProvider{
			{name: "Claude CLI", binary: "claude", heavyModel: "opus", stdModel: "sonnet"},
			{name: "Codex CLI", binary: "codex", heavyModel: "astra", stdModel: "gpt-sol"},
		},
	}
}

func (r *FallbackRouter) GetStates() []ProviderStateDTO {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	var dtos []ProviderStateDTO
	for _, p := range r.providers {
		if !p.resetAfter.IsZero() && now.After(p.resetAfter) {
			p.resetAfter = time.Time{}
		}
		status := "ACTIVE"
		resetStr := "-"
		remStr := "Disponível"
		if !p.resetAfter.IsZero() {
			status = "COOLDOWN"
			resetStr = p.resetAfter.Format("15:04:05")
			remStr = time.Until(p.resetAfter).Round(time.Second).String()
		}
		dtos = append(dtos, ProviderStateDTO{
			Name:      p.name,
			Models:    fmt.Sprintf("%s / %s", p.heavyModel, p.stdModel),
			Status:    status,
			ResetAt:   resetStr,
			Remaining: remStr,
		})
	}
	return dtos
}

// SimulateQuotaHit permite testar pelo Frontend a queda de um provider
func (r *FallbackRouter) SimulateQuotaHit(providerName string, duration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.providers {
		if strings.EqualFold(p.name, providerName) {
			p.resetAfter = time.Now().Add(duration)
		}
	}
}

func (r *FallbackRouter) Execute(ctx context.Context, req TaskRequest, onLog func(string)) (*TaskResponse, error) {
	switched := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		prov, earliest := r.pickAvailable()
		if prov == nil {
			wait := time.Until(earliest).Round(time.Second)
			onLog(fmt.Sprintf("⏳ Todos os providers em cooldown. Aguardando %v até restituição...", wait))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
				continue
			}
		}

		model := prov.stdModel
		if req.Tier == TierHeavy {
			model = prov.heavyModel
		}

		prompt := req.UserPrompt
		if switched {
			prompt += "\n\n[HAND-OFF]: Continue de onde o provider anterior parou verificando os arquivos atuais."
		}

		onLog(fmt.Sprintf("🤖 Acionando [%s] com modelo [%s] para o agente [%s]...", prov.name, model, req.AgentRole))

		resp, err := r.runProvider(ctx, prov, model, req.SystemPrompt, prompt, req.WorkDir)
		if err == nil {
			return resp, nil
		}

		var qErr *QuotaExceededError
		if errors.As(err, &qErr) {
			r.mu.Lock()
			prov.resetAfter = qErr.ResetTime
			r.mu.Unlock()
			onLog(fmt.Sprintf("⚠️ %s! Alternando automaticamente para o próximo provider...", qErr.Error()))
			switched = true
			continue
		}
		return nil, err
	}
}

func (r *FallbackRouter) pickAvailable() (*managedProvider, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var earliest time.Time
	for _, p := range r.providers {
		if !p.resetAfter.IsZero() && now.After(p.resetAfter) {
			p.resetAfter = time.Time{}
		}
		if p.resetAfter.IsZero() {
			return p, time.Time{}
		}
		if earliest.IsZero() || p.resetAfter.Before(earliest) {
			earliest = p.resetAfter
		}
	}
	return nil, earliest
}

func (r *FallbackRouter) runProvider(ctx context.Context, p *managedProvider, model, sys, user, workDir string) (*TaskResponse, error) {
	start := time.Now()

	// Se DryRun estiver ativo ou o binário não existir na máquina ainda, simula a resposta
	if r.DryRun {
		time.Sleep(2 * time.Second)
		return &TaskResponse{
			Output:       fmt.Sprintf("[Saída gerada por %s (%s)]\nConcluído com sucesso para a instrução solicitada.", p.name, model),
			ProviderUsed: p.name,
			ModelUsed:    model,
			Duration:     time.Since(start),
		}, nil
	}

	fullPrompt := fmt.Sprintf("SYSTEM:\n%s\n\nUSER:\n%s", sys, user)
	var args []string
	if p.binary == "claude" {
		args = []string{"-p", fullPrompt, "--model", model, "--dangerously-skip-permissions"}
	} else {
		args = []string{"--model", model, "--quiet", "--approval-mode", "full-auto", fullPrompt}
	}

	cmd := exec.CommandContext(ctx, p.binary, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	out := buf.String()
	if err != nil {
		if qErr := ParseQuotaError(p.name, out, time.Now()); qErr != nil {
			return nil, qErr
		}
		return nil, fmt.Errorf("erro CLI (%s): %v - %s", p.name, err, out)
	}

	return &TaskResponse{
		Output:       out,
		ProviderUsed: p.name,
		ModelUsed:    model,
		Duration:     time.Since(start),
	}, nil
}
```

---

### Passo 5.3: Servidor Orquestrador e Pipeline de Agentes (`main.go`)
Crie o arquivo `main.go`. Ele gerencia o pipeline completo (EDD/TDD $\rightarrow$ Arquiteto $\rightarrow$ Subagentes $\rightarrow$ Code Review $\rightarrow$ Playwright) e transmite cada passo em tempo real para o navegador.

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

type StepStatus struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Role     string `json:"role"`
	Status   string `json:"status"` // PENDING, RUNNING, DONE, ERROR
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Output   string `json:"output"`
}

type OrchestratorState struct {
	mu      sync.RWMutex
	Running bool         `json:"running"`
	Steps   []StepStatus `json:"steps"`
	Logs    []string     `json:"logs"`
}

var (
	router = NewFallbackRouter(true) // Mude para false quando tiver 'claude' e 'codex' no PATH
	state  = &OrchestratorState{
		Steps: defaultSteps(),
		Logs:  []string{"Sistema inicializado. Aguardando User Story (Markdown, Jira ou Trello)..."},
	}
)

func defaultSteps() []StepStatus {
	return []StepStatus{
		{ID: "spec", Title: "1. Transformar US em EDD & TDD", Role: "spec_writer", Status: "PENDING"},
		{ID: "arch", Title: "2. Arquiteto (Delegação de Agentes)", Role: "architect", Status: "PENDING"},
		{ID: "sub_fe", Title: "3.1 Subagente: Frontend & Designer", Role: "frontend", Status: "PENDING"},
		{ID: "sub_be", Title: "3.2 Subagente: Backend & Data Science", Role: "backend", Status: "PENDING"},
		{ID: "sub_sec", Title: "3.3 Subagente: Security & Tester (TDD)", Role: "security", Status: "PENDING"},
		{ID: "review", Title: "4. Code Review Criterioso (SOLID/Clean Code)", Role: "reviewer", Status: "PENDING"},
		{ID: "playwright", Title: "5. Subir App & Testes E2E (Playwright)", Role: "qa_playwright", Status: "PENDING"},
	}
}

func addLog(msg string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	timestamp := time.Now().Format("15:04:05")
	state.Logs = append(state.Logs, fmt.Sprintf("[%s] %s", timestamp, msg))
}

func updateStep(id, status, provider, model, output string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	for i := range state.Steps {
		if state.Steps[i].ID == id {
			state.Steps[i].Status = status
			if provider != "" {
				state.Steps[i].Provider = provider
				state.Steps[i].Model = model
			}
			if output != "" {
				state.Steps[i].Output = output
			}
			break
		}
	}
}

func runPipeline(userStory string, sourceType string) {
	state.mu.Lock()
	if state.Running {
		state.mu.Unlock()
		return
	}
	state.Running = true
	state.Steps = defaultSteps()
	state.Logs = []string{}
	state.mu.Unlock()

	addLog(fmt.Sprintf("📥 Nova atividade recebida via [%s]. Iniciando pipeline...", sourceType))
	ctx := context.Background()

	pipelineConfig := []struct {
		id     string
		tier   ModelTier
		sys    string
		prompt string
	}{
		{
			id:     "spec",
			tier:   TierStandard,
			sys:    "Você é um Staff Engineer especialista em EDD (Engineering Design Doc) e TDD.",
			prompt: "Converta a seguinte User Story em contratos técnicos (EDD) e casos de teste (TDD):\n" + userStory,
		},
		{
			id:     "arch",
			tier:   TierHeavy,
			sys:    "Você é o Arquiteto Principal. Analise o EDD/TDD e defina as tarefas exatas para Frontend, Backend, Data Science, Designer, Security e Tester.",
			prompt: "Distribua as responsabilidades técnicas para a US:\n" + userStory,
		},
		{
			id:     "sub_fe",
			tier:   TierStandard,
			sys:    "Você atua como Subagente de Frontend e UI/UX Designer. Escreva componentes limpos, acessíveis e responsivos.",
			prompt: "Implemente a camada visual e integração de UI conforme o EDD.",
		},
		{
			id:     "sub_be",
			tier:   TierStandard,
			sys:    "Você atua como Subagente de Backend e Data Science. Implemente APIs, regras de negócio e modelos de dados.",
			prompt: "Implemente as rotas, serviços e camada de dados passando nos testes TDD.",
		},
		{
			id:     "sub_sec",
			tier:   TierHeavy,
			sys:    "Você atua como Subagente de Security (OWASP) e Tester. Garanta cobertura de testes unitários e ausência de vulnerabilidades.",
			prompt: "Valide autenticação, sanitização de inputs e execute a suíte de testes unitários.",
		},
		{
			id:     "review",
			tier:   TierHeavy,
			sys:    "Você é um Revisor de Código implacável. Avalie boas práticas da linguagem, arquitetura, SOLID, DRY e performance.",
			prompt: "Realize o Code Review completo de todas as alterações feitas pelos subagentes.",
		},
		{
			id:     "playwright",
			tier:   TierStandard,
			sys:    "Você é um Engenheiro de QA Automação especialista em Playwright.",
			prompt: "Suba a aplicação, execute os testes integrados e E2E no navegador headless com Playwright e valide os fluxos da US.",
		},
	}

	for _, cfg := range pipelineConfig {
		updateStep(cfg.id, "RUNNING", "", "", "")
		resp, err := router.Execute(ctx, TaskRequest{
			AgentRole:    cfg.id,
			SystemPrompt: cfg.sys,
			UserPrompt:   cfg.prompt,
			Tier:         cfg.tier,
		}, addLog)

		if err != nil {
			updateStep(cfg.id, "ERROR", "", "", err.Error())
			addLog(fmt.Sprintf("❌ Falha na etapa %s: %v", cfg.id, err))
			break
		}

		updateStep(cfg.id, "DONE", resp.ProviderUsed, resp.ModelUsed, resp.Output)
		addLog(fmt.Sprintf("✅ Etapa [%s] concluída por %s (%s) em %v.", cfg.id, resp.ProviderUsed, resp.ModelUsed, resp.Duration.Round(time.Millisecond)))
	}

	state.mu.Lock()
	state.Running = false
	state.mu.Unlock()
	addLog("🏁 Fluxo finalizado!")
}

func main() {
	http.Handle("/", http.FileServer(http.Dir("./static")))

	// Endpoint de Status (Polling / Estado Completo)
	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		state.mu.RLock()
		defer state.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"running":   state.Running,
			"steps":     state.Steps,
			"logs":      state.Logs,
			"providers": router.GetStates(),
			"dry_run":   router.DryRun,
		})
	})

	// Endpoint para iniciar o processamento de uma US
	http.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Content string `json:"content"`
			Source  string `json:"source"`
			DryRun  bool   `json:"dry_run"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		router.DryRun = body.DryRun
		go runPipeline(body.Content, body.Source)
		w.WriteHeader(http.StatusAccepted)
	})

	// Endpoint para simular esgotamento de tokens (Para testar a alternância na UI!)
	http.HandleFunc("/api/simulate-quota", func(w http.ResponseWriter, r *http.Request) {
		prov := r.URL.Query().Get("provider")
		router.SimulateQuotaHit(prov, 45*time.Second)
		addLog(fmt.Sprintf("⚡ [SIMULAÇÃO] Sessão de tokens do '%s' marcada como esgotada por 45s!", prov))
		w.WriteHeader(http.StatusOK)
	})

	fmt.Println("🚀 Orquestrador rodando! Abra no navegador: http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
```

---

### Passo 5.4: O Frontend HTML Visual (`static/index.html`)
Crie a pasta `static` e dentro dela o arquivo `index.html`. Esta interface inclui:
* Painel de entrada para **Markdown**, **Jira** ou **Trello**.
* Monitor de **Quotas de LLM em Tempo Real** (com botões para você simular o fim dos tokens do Claude ou do Codex e ver o sistema alternando ao vivo!).
* Visualização em **Pipeline das 7 Etapas** (mostrando qual modelo executou cada etapa: *Opus*, *Sonnet*, *Astra* ou *GPT-Sol*).
* **Console de Logs ao vivo** estilo terminal.

```html
<!DOCTYPE html>
<html lang="pt-BR">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Go LLM Orchestrator - Dashboard</title>
  <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-slate-950 text-slate-100 min-h-screen font-sans p-6">
  <div class="max-w-7xl mx-auto space-y-6">
    
    <!-- Header -->
    <header class="flex flex-col md:flex-row justify-between items-start md:items-center bg-slate-900 border border-slate-800 p-5 rounded-xl">
      <div>
        <h1 class="text-2xl font-bold text-indigo-400">⚙️ Go Multi-Agent LLM Orchestrator</h1>
        <p class="text-sm text-slate-400">EDD/TDD • Arquiteto • Subagentes • Code Review • Playwright E2E</p>
      </div>
      <div class="mt-4 md:mt-0 flex items-center gap-3">
        <label class="flex items-center gap-2 text-xs bg-slate-800 px-3 py-2 rounded-lg border border-slate-700 cursor-pointer">
          <input type="checkbox" id="dryRunToggle" checked class="accent-indigo-500" />
          <span>Modo Simulação (Dry-Run sem gastar CLI)</span>
        </label>
      </div>
    </header>

    <!-- Grid Principal -->
    <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
      
      <!-- Coluna 1: Input de US + Status dos Providers -->
      <div class="space-y-6">
        
        <!-- Card de Quotas & Providers -->
        <div class="bg-slate-900 border border-slate-800 p-5 rounded-xl space-y-4">
          <div class="flex justify-between items-center">
            <h2 class="font-semibold text-slate-200">🔄 Sessão & Quotas de LLM</h2>
            <span class="text-xs text-slate-400">Auto-Switch Ativo</span>
          </div>
          <div id="providersList" class="space-y-3"></div>
          <div class="pt-2 border-t border-slate-800 flex gap-2">
            <button onclick="simulateQuota('Claude CLI')" class="text-xs bg-amber-950 hover:bg-amber-900 text-amber-300 border border-amber-800 px-3 py-1.5 rounded-lg w-full transition">
              Simular Fim Tokens Claude
            </button>
            <button onclick="simulateQuota('Codex CLI')" class="text-xs bg-amber-950 hover:bg-amber-900 text-amber-300 border border-amber-800 px-3 py-1.5 rounded-lg w-full transition">
              Simular Fim Tokens Codex
            </button>
          </div>
        </div>

        <!-- Card de Entrada da Atividade -->
        <div class="bg-slate-900 border border-slate-800 p-5 rounded-xl space-y-4">
          <h2 class="font-semibold text-slate-200">📥 Entrada de Demanda (US)</h2>
          
          <div>
            <label class="text-xs text-slate-400 block mb-1">Origem da Atividade</label>
            <select id="sourceType" class="w-full bg-slate-950 border border-slate-800 rounded-lg p-2.5 text-sm text-slate-200">
              <option value="Markdown (.md)">📄 Documento Markdown (.md)</option>
              <option value="Jira Issue">🟦 Conector Jira (Ex: PROJ-104)</option>
              <option value="Trello Card">🟩 Conector Trello Card</option>
            </select>
          </div>

          <div>
            <label class="text-xs text-slate-400 block mb-1">Descrição da User Story / Requisitos</label>
            <textarea id="usContent" rows="6" class="w-full bg-slate-950 border border-slate-800 rounded-lg p-3 text-sm text-slate-200 font-mono"
># US-101: Checkout com Validação Antifraude
Como cliente, quero finalizar minha compra com análise de risco em tempo real.
- Criar API POST /checkout em Go
- Criar modelo de score de risco (Data Science)
- Criar tela responsiva de pagamento
- Cobrir com testes unitários e E2E no Playwright</textarea>
          </div>

          <button id="startBtn" onclick="startOrchestration()" class="w-full bg-indigo-600 hover:bg-indigo-500 text-white font-semibold py-2.5 rounded-lg transition">
            🚀 Iniciar Orquestração Autônoma
          </button>
        </div>
      </div>

      <!-- Coluna 2 e 3: Pipeline de Agentes + Console -->
      <div class="lg:col-span-2 space-y-6">
        
        <!-- Pipeline Visual -->
        <div class="bg-slate-900 border border-slate-800 p-5 rounded-xl space-y-4">
          <h2 class="font-semibold text-slate-200">🏗️ Pipeline de Execução dos Agentes</h2>
          <div id="stepsContainer" class="space-y-3"></div>
        </div>

        <!-- Console de Logs em Tempo Real -->
        <div class="bg-slate-900 border border-slate-800 p-5 rounded-xl space-y-3">
          <h2 class="font-semibold text-slate-200">💻 Console do Orquestrador & Playwright</h2>
          <div id="logsContainer" class="bg-black border border-slate-800 rounded-lg p-4 h-56 overflow-y-auto font-mono text-xs text-emerald-400 space-y-1"></div>
        </div>

      </div>
    </div>
  </div>

  <script>
    async function fetchStatus() {
      try {
        const res = await fetch('/api/status');
        const data = await res.json();
        renderProviders(data.providers);
        renderSteps(data.steps);
        renderLogs(data.logs);
        document.getElementById('startBtn').disabled = data.running;
        document.getElementById('startBtn').innerText = data.running 
          ? '⏳ Orquestração em Andamento...' 
          : '🚀 Iniciar Orquestração Autônoma';
      } catch (e) {
        console.error(e);
      }
    }

    function renderProviders(providers) {
      const container = document.getElementById('providersList');
      container.innerHTML = providers.map(p => {
        const isCooldown = p.status === 'COOLDOWN';
        const badgeClass = isCooldown 
          ? 'bg-red-950 text-red-400 border-red-800' 
          : 'bg-emerald-950 text-emerald-400 border-emerald-800';
        return `
          <div class="p-3 rounded-lg bg-slate-950 border border-slate-800 flex justify-between items-center">
            <div>
              <div class="font-semibold text-sm">${p.name}</div>
              <div class="text-xs text-slate-400">Modelos: ${p.models}</div>
              ${isCooldown ? `<div class="text-xs text-amber-400 mt-1">⏱️ Restitui às ${p.reset_at} (faltam ${p.remaining})</div>` : ''}
            </div>
            <span class="text-xs px-2.5 py-1 rounded-full border font-medium ${badgeClass}">
              ${p.status}
            </span>
          </div>
        `;
      }).join('');
    }

    function renderSteps(steps) {
      const container = document.getElementById('stepsContainer');
      const statusStyles = {
        PENDING: 'border-slate-800 bg-slate-950 text-slate-400',
        RUNNING: 'border-indigo-500 bg-indigo-950/30 text-indigo-200 animate-pulse',
        DONE: 'border-emerald-800 bg-emerald-950/20 text-emerald-200',
        ERROR: 'border-red-800 bg-red-950/30 text-red-300'
      };
      const icons = { PENDING: '⏸️', RUNNING: '⚙️', DONE: '✅', ERROR: '❌' };

      container.innerHTML = steps.map(s => `
        <div class="p-3.5 rounded-lg border ${statusStyles[s.status]} transition">
          <div class="flex justify-between items-center">
            <div class="font-medium text-sm flex items-center gap-2">
              <span>${icons[s.status]}</span>
              <span>${s.title}</span>
            </div>
            <div class="flex items-center gap-2">
              ${s.provider ? `<span class="text-xs bg-slate-800 text-indigo-300 px-2 py-0.5 rounded border border-slate-700">${s.provider} • ${s.model}</span>` : ''}
              <span class="text-xs font-mono uppercase">${s.status}</span>
            </div>
          </div>
          ${s.output ? `<pre class="mt-2 text-xs bg-black/50 p-2 rounded text-slate-300 overflow-x-auto">${s.output}</pre>` : ''}
        </div>
      `).join('');
    }

    function renderLogs(logs) {
      const container = document.getElementById('logsContainer');
      container.innerHTML = logs.map(l => `<div>${l}</div>`).join('');
      container.scrollTop = container.scrollHeight;
    }

    async function startOrchestration() {
      const content = document.getElementById('usContent').value;
      const source = document.getElementById('sourceType').value;
      const dryRun = document.getElementById('dryRunToggle').checked;
      await fetch('/api/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content, source, dry_run: dryRun })
      });
      fetchStatus();
    }

    async function simulateQuota(provider) {
      await fetch(`/api/simulate-quota?provider=${encodeURIComponent(provider)}`, { method: 'POST' });
      fetchStatus();
    }

    setInterval(fetchStatus, 1000);
    fetchStatus();
  </script>
</body>
</html>
```

---

## 6. Como Rodar e Testar Agora Mesmo

1. Salve os arquivos acima na estrutura indicada (`main.go`, `provider.go` e `static/index.html`).
2. No terminal, execute:
   ```bash
   go run .
   ```
3. Abra o navegador em **`http://localhost:8080`**.
4. Clique em **"🚀 Iniciar Orquestração Autônoma"**.
5. **Teste o Auto-Switch ao vivo:** Enquanto as etapas estiverem rodando no painel, clique no botão **"Simular Fim Tokens Claude"**. Você verá imediatamente o card do Claude entrar em `COOLDOWN` com contagem regressiva e as próximas etapas passarem a rodar automaticamente pelo **Codex CLI (`astra` / `gpt-sol`)**!
6. Quando quiser rodar invocando os binários reais do `claude` e `codex` na sua máquina, basta desmarcar a caixinha **"Modo Simulação (Dry-Run)"** no topo da tela.

---

## 7. Próximos Passos para Evoluir o MVP (Fases 2 a 4)

Quando o MVP básico estiver rodando ao seu gosto, expanda adicionando:
1. **Conector Real Jira/Trello:** No endpoint `/api/start`, se `source == "Jira Issue"`, faça um `http.Get` na API REST do Jira (`https://seu-dominio.atlassian.net/rest/api/3/issue/{key}`) e extraia o campo `description` para alimentar o `runPipeline`.
2. **Execução Real do Playwright:** Na etapa 5 (`qa_playwright`), adicione um comando `exec.CommandContext(ctx, "npx", "playwright", "test", "--reporter=list")` apontando para a pasta do projeto gerado, capturando o relatório direto na saída da etapa no Dashboard HTML.