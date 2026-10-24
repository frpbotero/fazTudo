//go:build desktop

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"llm-orchestrator/internal/pipeline"
	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/server"
	"llm-orchestrator/internal/settings"
	"llm-orchestrator/internal/workspace"
	"llm-orchestrator/web"
)

func main() {
	if err := runDesktop(); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}
}

func runDesktop() error {
	loadShellPath()
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dryRun := desktopEnvBool("DRY_RUN", true)
	defaultProvider := provider.ID(desktopEnvOr("DEFAULT_PROVIDER", string(provider.Claude)))
	router := provider.NewRouter(dryRun, []provider.CLI{provider.ClaudeCLI, provider.CodexCLI},
		provider.WithSimulationDelay(1400*time.Millisecond), provider.WithDefaultProvider(defaultProvider))
	if err := router.Validate(); err != nil {
		cancel()
		return fmt.Errorf("provedor padrão: %w", err)
	}

	ws, err := workspace.NewManager(os.Getenv("REPO_PATH"))
	if err != nil {
		cancel()
		return err
	}
	store := settings.NewStore(desktopEnvOr("FAZTUDO_CONFIG", settings.DefaultPath()))
	ws.SetCacheDir(settings.DiscoveryDir(store.Path()))
	if file, loadErr := store.Load(); loadErr != nil {
		logger.Warn("configuração salva ignorada", "err", loadErr)
	} else {
		overrides := settings.Overrides{
			DefaultProvider: os.Getenv("DEFAULT_PROVIDER") != "",
			Repository:      os.Getenv("REPO_PATH") != "",
		}
		for _, applyErr := range settings.Apply(file, router, ws, overrides) {
			logger.Warn("parte da configuração salva ignorada", "err", applyErr)
		}
	}

	hub := server.NewHub()
	orch := pipeline.New(router, ws, pipeline.DefaultStages, hub.Notify, logger)
	orch.SetJournalDir(settings.RunsDir(store.Path()))
	srv := server.New(ctx, server.Deps{
		Orchestrator: orch, Router: router, Workspace: ws, Settings: store, Hub: hub,
		Static: web.Static, Version: version, Desktop: true, Logger: logger,
	})
	go hub.Run(ctx, srv.SnapshotJSON)

	shutdown := func(_ context.Context) {
		cancel()
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		orch.Stop()
		if waitErr := orch.Wait(stopCtx); waitErr != nil {
			logger.Warn("pipeline não finalizou a tempo", "err", waitErr)
		}
		orch.FlushJournal() // the interruption is saved, ready to resume
	}

	return wails.Run(&options.App{
		Title:            "FazTudo",
		Width:            1360,
		Height:           900,
		MinWidth:         980,
		MinHeight:        680,
		WindowStartState: options.Maximised,
		AssetServer: &assetserver.Options{
			Assets:  web.Static,
			Handler: srv.Handler(),
		},
		BackgroundColour: &options.RGBA{R: 246, G: 246, B: 247, A: 255},
		Menu:             appMenu(logger),
		OnShutdown:       shutdown,
		Mac: &mac.Options{
			Appearance:                   mac.DefaultAppearance,
			DisableEscapeExitsFullscreen: true,
		},
		Windows: &windows.Options{
			Theme:                               windows.SystemDefault,
			IsZoomControlEnabled:                false,
			WebviewGpuIsDisabled:                false,
			WebviewDisableRendererCodeIntegrity: false,
		},
		Linux: &linux.Options{
			ProgramName:      "faztudo",
			WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand,
		},
	})
}

func desktopEnvOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func desktopEnvBool(key string, fallback bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
