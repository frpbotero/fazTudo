//go:build !desktop

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"llm-orchestrator/internal/pipeline"
	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/server"
	"llm-orchestrator/internal/settings"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
	"llm-orchestrator/web"
)

type consoleRuntime struct {
	orch    *pipeline.Orchestrator
	handler http.Handler
	changed chan struct{}
	cancel  context.CancelFunc
	control *consoleControl
}

func runConsole(args []string, input io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprint(out, `FazTudo no console

  faztudo run --file US.md [--repo DIR] [--story ID | --stories ID,ID] [--real]
  faztudo stories parse --file US.md [--detect-model --real]
  faztudo status [--repo DIR] [--json]
  faztudo logs [--repo DIR]
  faztudo stop | resume | retry --story N --step ID [--repo DIR]
  faztudo repo show | set --path DIR | discovery-reset | branches | base --path . --id BRANCH
  faztudo policy show | apply --file POLITICA.json
  faztudo config show | apply --file CONFIG.json
  faztudo providers list | exhaust --id ID [--seconds N] | restore [--id ID]
  faztudo prompts list | show --id CHAVE | set --id CHAVE --file ARQUIVO | reset --id CHAVE
  faztudo commit show --id HASH
  faztudo tui [--repo DIR]

Use --config ARQUIVO para escolher a configuração. Use --file - para ler da entrada padrão.
run usa simulação por padrão; --real permite execução real e consumo de tokens.
`)
		return 0
	}
	switch args[0] {
	case "run":
		return consoleRun(args[1:], input, out, errOut)
	case "tui":
		return consoleTUI(args[1:], input, out, errOut)
	default:
		return consoleAction(args, input, out, errOut)
	}
}

func newConsoleRuntime(repo, configPath string, dry bool, providerID string, delay time.Duration, errOut io.Writer) (*consoleRuntime, error) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(errOut, &slog.HandlerOptions{Level: slog.LevelWarn}))
	providerOverride := providerID != "" || os.Getenv("DEFAULT_PROVIDER") != ""
	if providerID == "" {
		providerID = envOr("DEFAULT_PROVIDER", string(provider.Claude))
	}
	router := provider.NewRouter(dry, []provider.CLI{provider.ClaudeCLI, provider.CodexCLI, provider.GeminiCLI},
		provider.WithSimulationDelay(delay), provider.WithDefaultProvider(provider.ID(providerID)))
	if err := router.Validate(); err != nil {
		cancel()
		return nil, err
	}
	ws, err := workspace.NewManager(repo)
	if err != nil {
		cancel()
		return nil, err
	}
	store := settings.NewStore(configPath)
	ws.SetCacheDir(settings.DiscoveryDir(store.Path()))
	file, err := store.Load()
	if err != nil {
		fmt.Fprintf(errOut, "aviso: configuração salva ignorada: %v\n", err)
		file = settings.File{}
	}
	if problems := settings.Apply(file, router, ws, settings.Overrides{DefaultProvider: providerOverride, Repository: repo != ""}); len(problems) > 0 {
		fmt.Fprintf(errOut, "aviso: configuração ignorada: %v\n", problems[0])
	}
	changed := make(chan struct{}, 1)
	hub := server.NewHub()
	orch := pipeline.New(router, ws, pipeline.DefaultStages, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
		hub.Notify()
	}, logger)
	for _, problem := range orch.LoadPrompts(file.Prompts) {
		fmt.Fprintf(errOut, "aviso: prompt ignorado: %v\n", problem)
	}
	orch.SetJournalDir(settings.RunsDir(store.Path()))
	orch.SetUsageLog(settings.UsageLog(store.Path()))
	srv := server.New(ctx, server.Deps{Orchestrator: orch, Router: router, Workspace: ws, Settings: store,
		Hub: hub, Static: web.Static, Version: version, Logger: logger})
	go hub.Run(ctx, srv.SnapshotJSON)
	return &consoleRuntime{orch: orch, handler: srv.Handler(), changed: changed, cancel: cancel,
		control: newConsoleControl(store.Path(), ws.Path())}, nil
}

func (c *consoleRuntime) close() {
	if c.control != nil {
		c.control.stop()
	}
	c.cancel()
	c.orch.FlushJournal()
}

func (c *consoleRuntime) request(method, path string, body any, dst any) error {
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	var response consoleResponse
	var err error
	if c.control != nil && !c.control.serving() {
		response, err = c.control.remote(method, path, encoded)
	}
	if err != nil {
		return err
	}
	if response.Status == 0 {
		response = c.requestLocal(method, path, encoded)
	}
	return decodeConsoleResponse(response, dst)
}

func decodeConsoleResponse(response consoleResponse, dst any) error {
	if response.Status >= 400 {
		var result struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(response.Body, &result)
		if result.Error != "" {
			return errors.New(result.Error)
		}
		return fmt.Errorf("HTTP %d", response.Status)
	}
	if dst != nil {
		return json.Unmarshal(response.Body, dst)
	}
	return nil
}

func (c *consoleRuntime) requestLocal(method, path string, body []byte) consoleResponse {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return consoleResponse{Status: resp.StatusCode, Body: data}
}

func consoleRun(args []string, input io.Reader, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(errOut)
	repo := fs.String("repo", os.Getenv("REPO_PATH"), "repositório alvo")
	file := fs.String("file", "", "arquivo Markdown com User Stories")
	config := fs.String("config", envOr("FAZTUDO_CONFIG", settings.DefaultPath()), "arquivo de configuração")
	providerID := fs.String("provider", "", "provedor padrão desta execução")
	source := fs.String("source", "markdown", "origem: markdown, jira ou trello")
	storyID := fs.String("story", "", "ID de uma User Story")
	storyIDs := fs.String("stories", "", "IDs de várias User Stories separados por vírgula")
	real := fs.Bool("real", false, "execução real; o padrão é simulação")
	detectModel := fs.Bool("detect-model", false, "identifica histórias pelo modelo quando necessário")
	rediscover := fs.Bool("rediscover", false, "refaz o discovery")
	delay := fs.Duration("sim-delay", 1400*time.Millisecond, "duração de cada etapa simulada")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		fmt.Fprintln(errOut, "informe --file")
		return 2
	}
	if *detectModel && !*real {
		fmt.Fprintln(errOut, "--detect-model requer --real")
		return 2
	}
	if *storyID != "" && *storyIDs != "" {
		fmt.Fprintln(errOut, "use --story ou --stories, não ambos")
		return 2
	}
	content, err := readConsoleFile(*file, input)
	if err != nil {
		fmt.Fprintln(errOut, "erro ao ler", *file+":", err)
		return 1
	}
	c, err := newConsoleRuntime(*repo, *config, !*real, *providerID, *delay, errOut)
	if err != nil {
		fmt.Fprintln(errOut, "erro:", err)
		return 1
	}
	defer c.close()
	selected := stories.Parse(string(content))
	if *detectModel {
		body, _ := json.Marshal(map[string]any{"content": string(content), "use_model": true})
		if err := decodeConsoleResponse(c.requestLocal(http.MethodPost, "/api/stories/parse", body), &selected); err != nil {
			fmt.Fprintln(errOut, "erro:", err)
			return 1
		}
	}
	if len(selected.Stories) == 0 {
		fmt.Fprintln(errOut, "nenhuma User Story encontrada")
		return 2
	}
	if *storyID != "" || *storyIDs != "" {
		requested := map[string]bool{}
		if *storyID != "" {
			requested[*storyID] = true
		} else {
			for _, id := range strings.Split(*storyIDs, ",") {
				id = strings.TrimSpace(id)
				if id == "" || requested[id] {
					fmt.Fprintln(errOut, "lista de US contém ID vazio ou duplicado")
					return 2
				}
				requested[id] = true
			}
		}
		var matches []stories.Story
		for _, st := range selected.Stories {
			if requested[st.ID] {
				matches = append(matches, st)
			}
		}
		if len(matches) != len(requested) {
			fmt.Fprintf(errOut, "IDs solicitados não correspondem a User Stories únicas\n")
			return 2
		}
		selected.Stories = matches
	}
	name := ""
	if *file != "-" {
		name = filepath.Base(*file)
	}
	startBody, _ := json.Marshal(map[string]any{
		"stories": selected.Stories, "preamble": selected.Preamble, "source": *source,
		"dry_run": !*real, "rediscover": *rediscover, "document_name": name,
	})
	err = decodeConsoleResponse(c.requestLocal(http.MethodPost, "/api/pipeline/start", startBody), nil)
	if err != nil {
		fmt.Fprintln(errOut, "erro:", err)
		return 1
	}
	return c.waitRun(out, errOut)
}

func (c *consoleRuntime) waitRun(out, errOut io.Writer) int {
	if c.control != nil {
		if err := c.control.start(c); err != nil {
			fmt.Fprintln(errOut, "erro ao iniciar controle local:", err)
			c.orch.Stop()
			ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
			defer cancel()
			_ = c.orch.Wait(ctx)
			return 1
		}
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	done := make(chan error, 1)
	go func() { done <- c.orch.Wait(context.Background()) }()
	seen := 0
	for {
		select {
		case <-interrupt:
			c.orch.Stop()
		case <-c.changed:
			seen = printNewLogs(out, c.orch.Snapshot(), seen)
		case err := <-done:
			run := c.orch.Snapshot()
			printNewLogs(out, run, seen)
			for _, story := range run.Stories {
				fmt.Fprintf(out, "%s: %s (%s)\n", story.ID, story.Title, story.Status)
			}
			if err != nil {
				fmt.Fprintln(errOut, "erro:", err)
				return 1
			}
			switch run.Result {
			case pipeline.ResultSucceeded:
				fmt.Fprintf(out, "Execução concluída: %d User Story(s).\n", len(run.Stories))
				return 0
			case pipeline.ResultCanceled:
				fmt.Fprintln(out, "Execução cancelada.")
				return 130
			default:
				fmt.Fprintln(errOut, "Execução falhou.")
				return 1
			}
		}
	}
}

func printNewLogs(out io.Writer, run pipeline.Run, seen int) int {
	if seen > len(run.Logs) {
		seen = 0
	}
	for _, entry := range run.Logs[seen:] {
		fmt.Fprintf(out, "%s [%s] %s\n", entry.Time.Format("15:04:05"), entry.Level, entry.Message)
	}
	return len(run.Logs)
}
