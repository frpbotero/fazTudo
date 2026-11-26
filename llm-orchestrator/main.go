//go:build !desktop

// Command llm-orchestrator serves the FazTudo dashboard and runs the
// multi-agent pipeline on top of the Claude Code and Codex CLIs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"llm-orchestrator/internal/eval"
	"llm-orchestrator/internal/pipeline"
	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/server"
	"llm-orchestrator/internal/settings"
	"llm-orchestrator/internal/workspace"
	"llm-orchestrator/web"
)

func main() {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		os.Exit(runConsole(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", defaultAddr(), "endereço HTTP (env ADDR ou PORT)")
	dryRun := flag.Bool("dry-run", envBool("DRY_RUN", true), "inicia em modo simulação (env DRY_RUN)")
	repo := flag.String("repo", os.Getenv("REPO_PATH"), "repositório alvo inicial (env REPO_PATH)")
	defaultProvider := flag.String("provider", envOr("DEFAULT_PROVIDER", string(provider.Claude)), "provider padrão: claude, codex ou gemini (env DEFAULT_PROVIDER)")
	simDelay := flag.Duration("sim-delay", 1400*time.Millisecond, "duração de cada etapa simulada")
	logLevel := flag.String("log-level", envOr("LOG_LEVEL", "info"), "debug, info, warn ou error (env LOG_LEVEL)")
	configPath := flag.String("config", envOr("FAZTUDO_CONFIG", settings.DefaultPath()), `arquivo de configuração; "" desativa a persistência (env FAZTUDO_CONFIG)`)
	evalPath := flag.String("eval", "", "executa a suíte de avaliação (JSON) e sai; veja evals/README.md")
	showVersion := flag.Bool("version", false, "exibe a versão e sai")
	showTokens := flag.Bool("tokens", false, "soma o uso de tokens por execução (logs/tokens.jsonl ao lado da configuração) e sai")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if *showTokens {
		return printTokens(settings.UsageLog(settings.NewStore(*configPath).Path()))
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("log-level inválido: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	router := provider.NewRouter(*dryRun, []provider.CLI{provider.ClaudeCLI, provider.CodexCLI, provider.GeminiCLI},
		provider.WithSimulationDelay(*simDelay), provider.WithDefaultProvider(provider.ID(*defaultProvider)))
	if err := router.Validate(); err != nil {
		return fmt.Errorf("-provider: %w", err)
	}
	ws, err := workspace.NewManager(*repo)
	if err != nil {
		return err
	}

	store := settings.NewStore(*configPath)
	ws.SetCacheDir(settings.DiscoveryDir(store.Path()))
	file, err := store.Load()
	if err != nil {
		logger.Warn("configuração salva ignorada", "err", err)
	} else {
		overrides := settings.Overrides{
			DefaultProvider: flagOrEnvSet("provider", "DEFAULT_PROVIDER"),
			Repository:      flagOrEnvSet("repo", "REPO_PATH"),
		}
		for _, e := range settings.Apply(file, router, ws, overrides) {
			logger.Warn("parte da configuração salva ignorada", "err", e)
		}
	}
	if store.Path() != "" {
		logger.Info("configuração persistida", "path", store.Path())
	}
	warnUnknownModels(router, logger)
	if *evalPath != "" {
		return runEval(ctx, *evalPath, router, logger)
	}
	hub := server.NewHub()
	orch := pipeline.New(router, ws, pipeline.DefaultStages, hub.Notify, logger)
	orch.SetAppVersion(version)
	for _, e := range orch.LoadPrompts(file.Prompts) {
		logger.Warn("parte da configuração salva ignorada", "err", e)
	}
	orch.SetJournalDir(settings.RunsDir(store.Path()))
	orch.SetUsageLog(settings.UsageLog(store.Path()))
	srv := server.New(ctx, server.Deps{
		Orchestrator: orch, Router: router, Workspace: ws, Settings: store, Hub: hub,
		Static: web.Static, Version: version, Logger: logger,
	})
	go hub.Run(ctx, srv.SnapshotJSON)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: the SSE stream is long-lived.
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	logger.Info("FazTudo iniciado", "url", "http://"+displayAddr(ln.Addr()), "version", version, "dry_run", *dryRun)
	if host, _, _ := net.SplitHostPort(*addr); host == "" || host == "0.0.0.0" || host == "::" {
		logger.Warn("servidor exposto em todas as interfaces; a API pode executar CLIs sem confirmação de permissões")
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		logger.Info("encerrando…")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	orch.Stop()
	if err := orch.Wait(shutdownCtx); err != nil {
		logger.Warn("pipeline não finalizou a tempo", "err", err)
	}
	orch.FlushJournal() // the interruption is saved, ready to resume
	return httpSrv.Shutdown(shutdownCtx)
}

// runEval benchmarks the configurations of a suite and prints the report.
func runEval(ctx context.Context, path string, router *provider.Router, logger *slog.Logger) error {
	suite, err := eval.Load(path)
	if err != nil {
		return err
	}
	results, err := eval.Run(ctx, suite, router, logger)
	if len(results) > 0 {
		fmt.Println(eval.Report(results))
	}
	return err
}

// warnUnknownModels logs configured models that the installed CLI does not
// list, which usually means a stale or mistyped model ID.
func warnUnknownModels(r *provider.Router, logger *slog.Logger) {
	for _, st := range r.States() {
		if st.Catalog == "" {
			continue
		}
		for _, id := range []string{st.HeavyModel, st.StdModel} {
			if !slices.ContainsFunc(st.Models, func(m provider.Model) bool { return m.ID == id }) {
				logger.Warn("modelo configurado não está no catálogo do CLI", "provider", st.Name, "model", id, "catalog", st.Catalog)
			}
		}
	}
}

// flagOrEnvSet reports whether a value was given explicitly on the command
// line or through its environment variable.
func flagOrEnvSet(name, env string) bool {
	if os.Getenv(env) != "" {
		return true
	}
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func defaultAddr() string {
	if a := os.Getenv("ADDR"); a != "" {
		return a
	}
	return "127.0.0.1:" + envOr("PORT", "8081")
}

func displayAddr(a net.Addr) string {
	s := a.String()
	if strings.HasPrefix(s, "[::]") || strings.HasPrefix(s, "0.0.0.0") {
		_, port, _ := net.SplitHostPort(s)
		return "localhost:" + port
	}
	return s
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

// printTokens summarises the token log for comparing runs.
func printTokens(path string) error {
	if path == "" {
		return errors.New("sem arquivo de configuração, não há log de tokens")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("log de tokens: %w", err)
	}
	defer f.Close()
	fmt.Println(path)
	return pipeline.WriteUsageSummary(f, os.Stdout)
}
