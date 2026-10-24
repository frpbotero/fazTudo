// Package server exposes the orchestrator over HTTP: a JSON API, a
// Server-Sent Events stream and the embedded web UI.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"llm-orchestrator/internal/pipeline"
	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/settings"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/vcs"
	"llm-orchestrator/internal/workspace"
)

// The UI embeds its fonts; not every system maps .woff2 to a MIME type.
func init() { _ = mime.AddExtensionType(".woff2", "font/woff2") }

const (
	maxBodyBytes     = 1 << 20
	maxDocumentRunes = 200_000
	defaultExhaustS  = 45
	maxExhaustS      = 3600
	sseKeepAlive     = 25 * time.Second
	detectTimeout    = 3 * time.Minute
	contentTypeJSON  = "application/json"
	contentTypeEvent = "text/event-stream"
)

// Sources accepted as the origin of a user story.
var sources = map[string]string{
	"markdown": "Markdown",
	"jira":     "Jira",
	"trello":   "Trello",
}

// Snapshot is the full state pushed to the UI.
type Snapshot struct {
	Version   string           `json:"version"`
	Desktop   bool             `json:"desktop,omitempty"`
	DryRun    bool             `json:"dry_run"`
	Config    provider.Config  `json:"config"`
	Settings  settings.Status  `json:"settings"`
	Workspace workspace.State  `json:"workspace"`
	Run       pipeline.Run     `json:"run"`
	Providers []provider.State `json:"providers"`
}

// Server wires HTTP handlers to the orchestrator and router.
type Server struct {
	ctx     context.Context
	orch    *pipeline.Orchestrator
	router  *provider.Router
	ws      *workspace.Manager
	store   *settings.Store
	hub     *Hub
	log     *slog.Logger
	version string
	desktop bool
	handler http.Handler

	detectMu  sync.Mutex
	detectKey [sha256.Size]byte
	detectDoc stories.Document
}

// Deps groups the components the server exposes.
type Deps struct {
	Orchestrator *pipeline.Orchestrator
	Router       *provider.Router
	Workspace    *workspace.Manager
	Settings     *settings.Store
	Hub          *Hub
	Static       fs.FS
	Version      string
	Desktop      bool
	Logger       *slog.Logger
}

// New builds the HTTP handler. ctx bounds long-lived SSE connections so they
// close on shutdown.
func New(ctx context.Context, d Deps) *Server {
	if d.Settings == nil {
		d.Settings = settings.NewStore("")
	}
	s := &Server{
		ctx: ctx, orch: d.Orchestrator, router: d.Router, ws: d.Workspace, store: d.Settings,
		hub: d.Hub, log: d.Logger, version: d.Version, desktop: d.Desktop,
	}
	static := d.Static

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/stories/parse", s.handleParseStories)
	mux.HandleFunc("POST /api/pipeline/start", s.handleStart)
	mux.HandleFunc("POST /api/pipeline/stop", s.handleStop)
	mux.HandleFunc("POST /api/pipeline/retry", s.handleRetry)
	mux.HandleFunc("POST /api/pipeline/resume", s.handleResume)
	mux.HandleFunc("GET /api/workspace", s.handleGetWorkspace)
	mux.HandleFunc("PUT /api/workspace", s.handlePutWorkspace)
	mux.HandleFunc("DELETE /api/workspace/discovery", s.handleResetDiscovery)
	mux.HandleFunc("PUT /api/workspace/policy", s.handlePutPolicy)
	mux.HandleFunc("GET /api/workspace/repos", s.handleListRepos)
	mux.HandleFunc("GET /api/git/commits/{hash}", s.handleShowCommit)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("POST /api/providers/{id}/exhaust", s.handleExhaust)
	mux.HandleFunc("POST /api/providers/{id}/restore", s.handleRestore)
	mux.HandleFunc("POST /api/providers/restore", s.handleRestore)
	mux.Handle("GET /", http.FileServerFS(static))

	s.handler = chain(mux, s.recoverer, s.requestLogger, securityHeaders, sameOrigin)
	return s
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// SnapshotJSON serialises the current state; used by the hub.
func (s *Server) SnapshotJSON() ([]byte, error) {
	return json.Marshal(s.snapshot())
}

func (s *Server) snapshot() Snapshot {
	return Snapshot{
		Version:   s.version,
		Desktop:   s.desktop,
		DryRun:    s.router.DryRun(),
		Config:    s.router.Config(),
		Settings:  s.store.Status(),
		Workspace: s.ws.State(),
		Run:       s.orch.Snapshot(),
		Providers: s.router.States(),
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.snapshot())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming não suportado")
		return
	}
	w.Header().Set("Content-Type", contentTypeEvent)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	events, unsubscribe := s.hub.Subscribe()
	defer unsubscribe()

	initial, err := s.SnapshotJSON()
	if err != nil {
		return
	}
	fmt.Fprintf(w, "retry: 3000\ndata: %s\n\n", initial)
	flusher.Flush()

	keepAlive := time.NewTicker(sseKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case data := <-events:
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (s *Server) handleParseStories(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
		// UseModel lets a light LLM find the stories when no story heading
		// matches (sent for loaded files, not for a story typed by hand).
		UseModel bool `json:"use_model"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if utf8.RuneCountInString(req.Content) > maxDocumentRunes {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("o documento excede %d caracteres", maxDocumentRunes))
		return
	}
	doc := stories.Parse(req.Content)
	if doc.Method == stories.MethodWhole && req.UseModel && !s.router.DryRun() {
		if found, err := s.detectStories(r.Context(), req.Content); err != nil {
			s.log.Warn("detecção de User Stories pelo modelo falhou", "err", err)
		} else {
			doc = found
		}
	}
	writeJSON(w, http.StatusOK, doc)
}

// detectStories asks a light model where each story starts. The last result
// is cached because the UI parses the same document again before starting.
func (s *Server) detectStories(ctx context.Context, md string) (stories.Document, error) {
	key := sha256.Sum256([]byte(md))
	s.detectMu.Lock()
	defer s.detectMu.Unlock()
	if s.detectKey == key {
		return s.detectDoc, nil
	}

	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	resp, err := s.router.Execute(ctx, provider.Request{
		Role:     "Identificação de User Stories",
		System:   stories.DetectSystem,
		Prompt:   stories.DetectPrompt(md),
		WorkDir:  os.TempDir(),
		Tier:     provider.TierLight,
		ReadOnly: true,
	}, func(level slog.Level, format string, args ...any) {
		s.log.Log(ctx, level, fmt.Sprintf(format, args...))
	})
	if err != nil {
		return stories.Document{}, err
	}
	doc, err := stories.FromModel(md, resp.Output)
	if err != nil {
		return stories.Document{}, err
	}
	s.detectKey, s.detectDoc = key, doc
	return doc, nil
}

// startRequest accepts either explicit stories (as chosen by the user from a
// parsed document) or a single free-text story in content.
type startRequest struct {
	Content    string          `json:"content"`
	Stories    []stories.Story `json:"stories"`
	Preamble   string          `json:"preamble"`
	Source     string          `json:"source"`
	DryRun     bool            `json:"dry_run"`
	Rediscover bool            `json:"rediscover"`
	// DocumentName is the loaded file's name; it is protected from edits.
	DocumentName string `json:"document_name"`
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	source, ok := sources[req.Source]
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("origem inválida: %q", req.Source))
		return
	}
	list, preamble, msg := resolveStories(req)
	if msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}

	err := s.orch.Start(pipeline.StartOptions{
		Stories:      list,
		Preamble:     preamble,
		Source:       source,
		DryRun:       req.DryRun,
		Rediscover:   req.Rediscover,
		DocumentName: req.DocumentName,
	})
	if err != nil {
		switch {
		case errors.Is(err, pipeline.ErrAlreadyRunning), errors.Is(err, workspace.ErrRepositoryBusy):
			writeError(w, http.StatusConflict, err.Error())
			return
		case errors.Is(err, workspace.ErrNotConfigured):
			writeError(w, http.StatusUnprocessableEntity, "configure o repositório antes de iniciar")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "stories": len(list)})
}

// resolveStories validates the request and returns the stories to develop,
// or a user-facing error message.
func resolveStories(req startRequest) ([]stories.Story, string, string) {
	size := utf8.RuneCountInString(req.Content) + utf8.RuneCountInString(req.Preamble)
	for _, st := range req.Stories {
		size += utf8.RuneCountInString(st.Content)
	}
	if size > maxDocumentRunes {
		return nil, "", fmt.Sprintf("o conteúdo excede %d caracteres", maxDocumentRunes)
	}

	if len(req.Stories) == 0 {
		doc := stories.Parse(req.Content)
		switch n := len(doc.Stories); {
		case n == 0:
			return nil, "", "a User Story não pode estar vazia"
		case n > 1:
			return nil, "", fmt.Sprintf("o documento contém %d User Stories; escolha quais desenvolver", n)
		}
		return doc.Stories, doc.Preamble, ""
	}

	if len(req.Stories) > stories.MaxStories {
		return nil, "", fmt.Sprintf("no máximo %d User Stories por execução", stories.MaxStories)
	}
	list := make([]stories.Story, len(req.Stories))
	for i, st := range req.Stories {
		st.Content = strings.TrimSpace(st.Content)
		st.Title = strings.TrimSpace(st.Title)
		if st.Content == "" {
			return nil, "", fmt.Sprintf("a User Story %d está vazia", i+1)
		}
		if st.Title == "" {
			st.Title = fmt.Sprintf("US %d", i+1)
		}
		if st.ID == "" {
			st.ID = st.Title
		}
		list[i] = st
	}
	return list, strings.TrimSpace(req.Preamble), ""
}

func (s *Server) handleStop(w http.ResponseWriter, _ *http.Request) {
	if !s.orch.Stop() {
		writeError(w, http.StatusConflict, "nenhuma execução em andamento")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
}

// handleResume continues the stopped run (failed, canceled or cut by the app
// closing) from where it stopped.
func (s *Server) handleResume(w http.ResponseWriter, _ *http.Request) {
	if err := s.orch.Resume(); err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, pipeline.ErrAlreadyRunning) || errors.Is(err, workspace.ErrRepositoryBusy) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "resuming"})
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Story int    `json:"story"`
		Step  string `json:"step"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.orch.RetryFailed(body.Story, strings.TrimSpace(body.Step)); err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, pipeline.ErrAlreadyRunning) || errors.Is(err, workspace.ErrRepositoryBusy) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "retrying"})
}

func (s *Server) handleGetWorkspace(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.ws.State())
}

func (s *Server) handlePutWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	path, err := s.orch.SetRepository(body.Path)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, pipeline.ErrAlreadyRunning) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	s.log.Info("repository configured", "path", path)
	s.persist()
	s.hub.Notify()
	writeJSON(w, http.StatusOK, s.ws.State())
}

func (s *Server) handleResetDiscovery(w http.ResponseWriter, _ *http.Request) {
	s.ws.Invalidate()
	s.hub.Notify()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePutPolicy(w http.ResponseWriter, r *http.Request) {
	var p workspace.Policy
	if !decodeJSON(w, r, &p) {
		return
	}
	saved, err := s.ws.SetPolicy(p)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.log.Info("quality policy updated", "repository", s.ws.Path(), "auto_checks", saved.AutoChecks, "checks", len(saved.Checks), "git", saved.Git)
	s.persist()
	s.hub.Notify()
	writeJSON(w, http.StatusOK, s.ws.State())
}

// handleListRepos lists the repository and its nested repositories with
// their branches, to choose each one's base branch.
func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	repo := s.ws.Path()
	if repo == "" {
		writeError(w, http.StatusUnprocessableEntity, workspace.ErrNotConfigured.Error())
		return
	}
	g, err := vcs.Find(r.Context(), repo)
	if err != nil {
		writeJSON(w, http.StatusOK, []vcs.RepoBranches{})
		return
	}
	repos, err := g.AllBranches(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

// handleShowCommit returns a commit made by the pipeline in the current
// repository, with its stats and patch.
func (s *Server) handleShowCommit(w http.ResponseWriter, r *http.Request) {
	repo := s.ws.Path()
	if repo == "" {
		writeError(w, http.StatusUnprocessableEntity, workspace.ErrNotConfigured.Error())
		return
	}
	g, err := vcs.Find(r.Context(), repo)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	patch, err := g.Show(r.Context(), r.PathValue("hash"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"patch": patch})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.router.Config())
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var cfg provider.ConfigUpdate
	if !decodeJSON(w, r, &cfg) {
		return
	}
	for id, c := range cfg.Providers {
		cfg.Providers[id] = provider.Settings{
			HeavyModel: strings.TrimSpace(c.HeavyModel),
			StdModel:   strings.TrimSpace(c.StdModel),
			Effort:     strings.TrimSpace(c.Effort),
		}
	}
	if err := s.router.UpdateConfig(cfg); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.log.Info("provider config updated", "config", cfg)
	s.persist()
	s.hub.Notify()
	writeJSON(w, http.StatusOK, s.router.Config())
}

func (s *Server) handleExhaust(w http.ResponseWriter, r *http.Request) {
	body := struct {
		Seconds int `json:"seconds"`
	}{Seconds: defaultExhaustS}
	if r.ContentLength > 0 && !decodeJSON(w, r, &body) {
		return
	}
	if body.Seconds <= 0 || body.Seconds > maxExhaustS {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("seconds deve estar entre 1 e %d", maxExhaustS))
		return
	}

	id := provider.ID(r.PathValue("id"))
	if err := s.router.MarkExhausted(id, time.Duration(body.Seconds)*time.Second); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.log.Info("quota exhaustion simulated", "provider", id, "seconds", body.Seconds)
	s.hub.Notify()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	id := provider.ID(r.PathValue("id"))
	if err := s.router.Restore(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.hub.Notify()
	w.WriteHeader(http.StatusNoContent)
}

// persist saves the current configuration. Failures are logged and surfaced
// through the snapshot's settings status; the in-memory change still applies.
func (s *Server) persist() {
	if err := s.store.Update(func() settings.File { return settings.Capture(s.router, s.ws) }); err != nil {
		s.log.Error("failed to save configuration", "path", s.store.Path(), "err", err)
	}
}

// ==================== helpers ====================

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "JSON inválido: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
