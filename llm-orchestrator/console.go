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
	"path/filepath"
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
}

func isConsoleCommand(s string) bool {
	switch s {
	case "run", "status", "resume", "stop", "retry", "logs", "stories", "repo", "policy", "config", "providers", "prompts", "commit", "tui", "help":
		return true
	}
	return false
}

func runConsole(args []string, input io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprintln(out, "Uso: faztudo <run|status|resume|stop|retry|logs|stories|repo|policy|config|providers|prompts|commit|tui> [opções]")
		return 0
	}
	switch args[0] {
	case "run":
		return consoleRun(args[1:], input, out, errOut)
	default:
		fmt.Fprintf(errOut, "comando %q ainda não implementado\n", args[0])
		return 2
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
		cancel()
		return nil, err
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
	return &consoleRuntime{orch: orch, handler: srv.Handler(), changed: changed, cancel: cancel}, nil
}

func (c *consoleRuntime) close() {
	c.cancel()
	c.orch.FlushJournal()
}

func (c *consoleRuntime) request(method, path string, body any, dst any) error {
	var data io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		data = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, data)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var result struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&result)
		if result.Error != "" {
			return errors.New(result.Error)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}

func consoleRun(args []string, input io.Reader, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(errOut)
	repo := fs.String("repo", "", "repositório alvo")
	file := fs.String("file", "", "arquivo Markdown com User Stories")
	config := fs.String("config", settings.DefaultPath(), "arquivo de configuração")
	providerID := fs.String("provider", "", "provedor padrão desta execução")
	source := fs.String("source", "markdown", "origem: markdown, jira ou trello")
	storyID := fs.String("story", "", "ID de uma User Story")
	real := fs.Bool("real", false, "execução real; o padrão é simulação")
	rediscover := fs.Bool("rediscover", false, "refaz o discovery")
	delay := fs.Duration("sim-delay", 1400*time.Millisecond, "duração de cada etapa simulada")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		fmt.Fprintln(errOut, "informe --file")
		return 2
	}
	var content []byte
	var err error
	if *file == "-" {
		content, err = io.ReadAll(io.LimitReader(input, 1<<20+1))
	} else {
		content, err = os.ReadFile(*file)
	}
	if err != nil {
		fmt.Fprintln(errOut, "erro ao ler", *file+":", err)
		return 1
	}
	if len(content) > 1<<20 {
		fmt.Fprintln(errOut, "o arquivo excede 1 MiB")
		return 2
	}
	selected := stories.Parse(string(content))
	if len(selected.Stories) == 0 {
		fmt.Fprintln(errOut, "nenhuma User Story encontrada")
		return 2
	}
	if *storyID != "" {
		var matches []stories.Story
		for _, st := range selected.Stories {
			if st.ID == *storyID {
				matches = append(matches, st)
			}
		}
		if len(matches) != 1 {
			fmt.Fprintf(errOut, "ID %q não corresponde a uma única User Story\n", *storyID)
			return 2
		}
		selected.Stories = matches
	}
	c, err := newConsoleRuntime(*repo, *config, !*real, *providerID, *delay, errOut)
	if err != nil {
		fmt.Fprintln(errOut, "erro:", err)
		return 1
	}
	defer c.close()
	name := ""
	if *file != "-" {
		name = filepath.Base(*file)
	}
	err = c.request(http.MethodPost, "/api/pipeline/start", map[string]any{
		"stories": selected.Stories, "preamble": selected.Preamble, "source": *source,
		"dry_run": !*real, "rediscover": *rediscover, "document_name": name,
	}, nil)
	if err != nil {
		fmt.Fprintln(errOut, "erro:", err)
		return 1
	}
	return c.waitRun(out, errOut)
}

func (c *consoleRuntime) waitRun(out, errOut io.Writer) int {
	done := make(chan error, 1)
	go func() { done <- c.orch.Wait(context.Background()) }()
	seen := 0
	for {
		select {
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
