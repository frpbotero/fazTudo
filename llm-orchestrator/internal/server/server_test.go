package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"llm-orchestrator/internal/pipeline"
	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/settings"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

// lastStore is the settings store of the most recent test server.
var lastStore *settings.Store

func newTestServer(t *testing.T) (*httptest.Server, *pipeline.Orchestrator) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := provider.NewRouter(true, []provider.CLI{provider.ClaudeCLI, provider.CodexCLI},
		provider.WithSimulationDelay(time.Millisecond))
	hub := NewHub()
	ws, err := workspace.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	orch := pipeline.New(router, ws, pipeline.DefaultStages, hub.Notify, logger)

	ctx, cancel := context.WithCancel(context.Background())
	store := settings.NewStore(filepath.Join(t.TempDir(), "config.json"))
	srv := New(ctx, Deps{
		Orchestrator: orch, Router: router, Workspace: ws, Settings: store, Hub: hub,
		Static: fstest.MapFS{"index.html": {Data: []byte("ok")}}, Version: "test", Logger: logger,
	})
	lastStore = store
	go hub.Run(ctx, srv.SnapshotJSON)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		cancel()
		ts.Close()
		waitCtx, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_ = orch.Wait(waitCtx)
	})
	return ts, orch
}

func do(t *testing.T, ts *httptest.Server, method, path, body string, headers ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestStartValidation(t *testing.T) {
	ts, _ := newTestServer(t)

	cases := []struct {
		name, body string
		want       int
	}{
		{"empty story", `{"content":"  ","source":"markdown"}`, http.StatusUnprocessableEntity},
		{"bad source", `{"content":"x","source":"email"}`, http.StatusUnprocessableEntity},
		{"unknown field", `{"content":"x","source":"markdown","evil":1}`, http.StatusBadRequest},
		{"malformed", `{`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := do(t, ts, "POST", "/api/pipeline/start", c.body).StatusCode; got != c.want {
				t.Errorf("status = %d, want %d", got, c.want)
			}
		})
	}
}

func TestStartRunsPipelineAndRejectsConcurrentRun(t *testing.T) {
	ts, orch := newTestServer(t)
	body := `{"content":"US-1","source":"markdown","dry_run":true}`

	if got := do(t, ts, "POST", "/api/pipeline/start", body).StatusCode; got != http.StatusAccepted {
		t.Fatalf("start = %d", got)
	}
	if got := do(t, ts, "POST", "/api/pipeline/start", body).StatusCode; got != http.StatusConflict {
		t.Errorf("concurrent start = %d, want 409", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := orch.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	var snap Snapshot
	if err := json.NewDecoder(do(t, ts, "GET", "/api/status", "").Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if snap.Run.Result != pipeline.ResultSucceeded || snap.Run.Source != "Markdown" {
		t.Errorf("unexpected run: result=%s source=%s", snap.Run.Result, snap.Run.Source)
	}
}

func TestCrossOriginWritesAreBlocked(t *testing.T) {
	ts, _ := newTestServer(t)

	resp := do(t, ts, "POST", "/api/pipeline/start", `{"content":"x","source":"markdown"}`, "Origin", "https://evil.example")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin start = %d, want 403", resp.StatusCode)
	}

	req, _ := http.NewRequest("POST", ts.URL+"/api/pipeline/start", strings.NewReader(`{"content":"x","source":"markdown"}`))
	req.Header.Set("Content-Type", "text/plain")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain start = %d, want 415", r2.StatusCode)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	ts, _ := newTestServer(t)

	bad := do(t, ts, "PUT", "/api/config", `{"providers":{"claude":{"heavy_model":"opus","std_model":"sonnet","effort":"ultra"}}}`)
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("invalid effort = %d, want 422", bad.StatusCode)
	}

	ok := do(t, ts, "PUT", "/api/config", `{"default_provider":"codex","failover":false,"providers":{"claude":{"heavy_model":" opus ","std_model":"sonnet","effort":"max"}}}`)
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("valid config = %d", ok.StatusCode)
	}
	var cfg provider.Config
	_ = json.NewDecoder(ok.Body).Decode(&cfg)
	if cfg.Providers[provider.Claude].HeavyModel != "opus" || cfg.DefaultProvider != provider.Codex || cfg.Failover {
		t.Errorf("config not applied: %+v", cfg)
	}

	// Partial update: only the default provider changes.
	part := do(t, ts, "PUT", "/api/config", `{"default_provider":"claude"}`)
	_ = json.NewDecoder(part.Body).Decode(&cfg)
	if cfg.DefaultProvider != provider.Claude || cfg.Failover || cfg.Providers[provider.Claude].HeavyModel != "opus" {
		t.Errorf("partial update changed other fields: %+v", cfg)
	}

	if got := do(t, ts, "PUT", "/api/config", `{"default_provider":"gemini"}`).StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("unknown default provider = %d, want 422", got)
	}
}

func TestExhaustAndRestore(t *testing.T) {
	ts, _ := newTestServer(t)

	if got := do(t, ts, "POST", "/api/providers/claude/exhaust", `{"seconds":60}`).StatusCode; got != http.StatusNoContent {
		t.Fatalf("exhaust = %d", got)
	}
	if got := do(t, ts, "POST", "/api/providers/gemini/exhaust", "").StatusCode; got != http.StatusNotFound {
		t.Errorf("unknown provider = %d, want 404", got)
	}

	var snap Snapshot
	_ = json.NewDecoder(do(t, ts, "GET", "/api/status", "").Body).Decode(&snap)
	if snap.Providers[0].Status != provider.StatusCooldown {
		t.Errorf("claude status = %s", snap.Providers[0].Status)
	}

	if got := do(t, ts, "POST", "/api/providers/restore", "").StatusCode; got != http.StatusNoContent {
		t.Fatalf("restore all = %d", got)
	}
	_ = json.NewDecoder(do(t, ts, "GET", "/api/status", "").Body).Decode(&snap)
	if snap.Providers[0].Status == provider.StatusCooldown {
		t.Error("claude should be restored")
	}
}

func TestSecurityHeaders(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := do(t, ts, "GET", "/healthz", "")
	if resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing security headers: %v", resp.Header)
	}
}

func TestWorkspaceEndpoints(t *testing.T) {
	ts, orch := newTestServer(t)

	if got := do(t, ts, "PUT", "/api/workspace", `{"path":"/definitely/not/here"}`).StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("invalid path = %d, want 422", got)
	}

	repo := t.TempDir()
	resp := do(t, ts, "PUT", "/api/workspace", `{"path":"`+repo+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set workspace = %d", resp.StatusCode)
	}

	if got := do(t, ts, "POST", "/api/pipeline/start", `{"content":"US","source":"markdown","dry_run":true}`).StatusCode; got != http.StatusAccepted {
		t.Fatalf("start = %d", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = orch.Wait(ctx)

	var state workspace.State
	_ = json.NewDecoder(do(t, ts, "GET", "/api/workspace", "").Body).Decode(&state)
	if state.Path != repo || state.Discovery == nil {
		t.Fatalf("discovery should be cached for %s: %+v", repo, state)
	}

	if got := do(t, ts, "DELETE", "/api/workspace/discovery", "").StatusCode; got != http.StatusNoContent {
		t.Fatalf("reset discovery = %d", got)
	}
	var cleared workspace.State
	_ = json.NewDecoder(do(t, ts, "GET", "/api/workspace", "").Body).Decode(&cleared)
	if cleared.Discovery != nil {
		t.Error("discovery should be cleared")
	}
}

func TestParseAndStartSequence(t *testing.T) {
	ts, orch := newTestServer(t)
	md := "# Épico\n\n## US-1 A\nconteúdo A\n\n## US-2 B\nconteúdo B\n"
	body, _ := json.Marshal(map[string]string{"content": md})

	var doc stories.Document
	if err := json.NewDecoder(do(t, ts, "POST", "/api/stories/parse", string(body)).Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Stories) != 2 || doc.Preamble != "# Épico" {
		t.Fatalf("unexpected parse: %+v", doc)
	}

	start := map[string]any{"content": md, "source": "markdown", "dry_run": true}
	b, _ := json.Marshal(start)
	if got := do(t, ts, "POST", "/api/pipeline/start", string(b)).StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("multi-story content without a choice = %d, want 422", got)
	}

	start = map[string]any{"stories": doc.Stories, "preamble": doc.Preamble, "source": "markdown", "dry_run": true}
	b, _ = json.Marshal(start)
	if got := do(t, ts, "POST", "/api/pipeline/start", string(b)).StatusCode; got != http.StatusAccepted {
		t.Fatalf("start sequence = %d", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = orch.Wait(ctx)

	run := orch.Snapshot()
	if run.Result != pipeline.ResultSucceeded || len(run.Stories) != 2 || run.Stories[1].Status != pipeline.StepDone {
		t.Fatalf("sequence did not complete: %+v", run.Stories)
	}

	empty := `{"stories":[{"title":"x","content":"  "}],"source":"markdown"}`
	if got := do(t, ts, "POST", "/api/pipeline/start", empty).StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("empty story = %d, want 422", got)
	}
}

func TestConfigAndRepositoryArePersisted(t *testing.T) {
	ts, _ := newTestServer(t)
	store := lastStore
	repo := t.TempDir()

	do(t, ts, "PUT", "/api/config", `{"default_provider":"codex","failover":false,"providers":{"codex":{"heavy_model":"gpt-5.5-pro","std_model":"gpt-5.4","effort":"high"}}}`)
	do(t, ts, "PUT", "/api/workspace", `{"path":"`+repo+`"}`)

	f, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if f.DefaultProvider != provider.Codex || f.Failover == nil || *f.Failover ||
		f.Providers[provider.Codex].HeavyModel != "gpt-5.5-pro" || f.Repository != repo {
		t.Errorf("file not persisted: %+v", f)
	}

	var snap Snapshot
	_ = json.NewDecoder(do(t, ts, "GET", "/api/status", "").Body).Decode(&snap)
	if !snap.Settings.Enabled || snap.Settings.SavedAt == nil || snap.Settings.Error != "" {
		t.Errorf("settings status = %+v", snap.Settings)
	}

	// Rejected updates must not be written.
	do(t, ts, "PUT", "/api/config", `{"default_provider":"gemini"}`)
	if f2, _ := store.Load(); f2.DefaultProvider != provider.Codex {
		t.Errorf("invalid update leaked to disk: %+v", f2)
	}
}

func TestQualityPolicyIsValidatedAndPersisted(t *testing.T) {
	ts, _ := newTestServer(t)

	var st workspace.State
	if err := json.NewDecoder(do(t, ts, "GET", "/api/workspace", "").Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if !st.Policy.AutoChecks || !st.Policy.Git || st.Policy.MaxLineLength != 160 || st.Suggested == nil {
		t.Fatalf("default policy = %+v suggested = %v", st.Policy, st.Suggested)
	}

	bad := []string{
		`{"auto_checks":false,"checks":[],"max_line_length":160,"check_timeout_s":60}`,
		`{"auto_checks":true,"max_line_length":20,"check_timeout_s":60}`,
		`{"auto_checks":false,"checks":[{"name":"x","run":"a\nb"}],"max_line_length":160,"check_timeout_s":60}`,
	}
	for _, body := range bad {
		if resp := do(t, ts, "PUT", "/api/workspace/policy", body); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d", body, resp.StatusCode)
		}
	}

	body := `{"auto_checks":false,"checks":[{"name":" unit ","run":" npm test "}],"protected":["EDD*.md"," "],"git":false,"max_line_length":120,"check_timeout_s":90}`
	resp := do(t, ts, "PUT", "/api/workspace/policy", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if p := st.Policy; p.AutoChecks || len(p.Checks) != 1 || p.Checks[0].Run != "npm test" || len(p.Protected) != 1 || p.Git {
		t.Fatalf("saved policy = %+v", p)
	}

	file, err := lastStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := file.Quality[st.Path]; !ok || p.MaxLineLength != 120 {
		t.Fatalf("persisted quality = %+v", file.Quality)
	}
}

func TestShowCommitRejectsInvalidRequests(t *testing.T) {
	ts, _ := newTestServer(t)
	if resp := do(t, ts, "GET", "/api/git/commits/abc1234", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("repository without git: status %d", resp.StatusCode)
	}
}

func TestSameOriginAcceptsBodylessRequestOfUnknownLength(t *testing.T) {
	ok := sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	}))
	cases := []struct {
		name, body string
		want       int
	}{
		{"empty body, as the desktop webview sends Stop", "", http.StatusOK},
		{"text body without JSON content type", "x", http.StatusUnsupportedMediaType},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "/api/pipeline/stop", strings.NewReader(c.body))
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		ok.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func promptValue(t *testing.T, resp *http.Response, key string) (string, bool) {
	t.Helper()
	var items []pipeline.PromptItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Key == key {
			return item.Value, item.Customized
		}
	}
	t.Fatalf("no prompt %s", key)
	return "", false
}

func TestPromptEditsArePersistedAndRestored(t *testing.T) {
	ts, _ := newTestServer(t)

	resp := do(t, ts, http.MethodPut, "/api/prompts/fix.task", `{"value":"meu texto"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if value, customized := promptValue(t, resp, "fix.task"); value != "meu texto" || !customized {
		t.Fatalf("value=%q customized=%v", value, customized)
	}
	file, err := lastStore.Load()
	if err != nil || file.Prompts["fix.task"] != "meu texto" {
		t.Fatalf("saved = %+v, err = %v", file.Prompts, err)
	}

	resp = do(t, ts, http.MethodDelete, "/api/prompts/fix.task", "")
	if _, customized := promptValue(t, resp, "fix.task"); customized {
		t.Fatal("reset must drop the customization")
	}
	file, _ = lastStore.Load()
	if _, saved := file.Prompts["fix.task"]; saved {
		t.Fatalf("the key must leave config.json: %+v", file.Prompts)
	}
}

func TestInvalidPromptEditsAreRefused(t *testing.T) {
	ts, _ := newTestServer(t)
	for name, request := range map[string][2]string{
		"unknown": {"/api/prompts/nao.existe", `{"value":"x"}`},
		"blank":   {"/api/prompts/fix.task", `{"value":"  "}`},
		"nul":     {"/api/prompts/fix.task", `{"value":"a\u0000b"}`},
		"huge":    {"/api/prompts/fix.task", `{"value":"` + strings.Repeat("x", 33<<10) + `"}`},
	} {
		if resp := do(t, ts, http.MethodPut, request[0], request[1]); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d", name, resp.StatusCode)
		}
	}
}

func TestCrossOriginPromptEditIsBlocked(t *testing.T) {
	ts, orch := newTestServer(t)

	resp := do(t, ts, http.MethodPut, "/api/prompts/fix.task", `{"value":"injetado"}`, "Origin", "https://evil.example")

	if resp.StatusCode != http.StatusForbidden || orch.Prompt("fix.task") == "injetado" {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestPromptTextStaysOutOfTheLogs(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	router := provider.NewRouter(true, []provider.CLI{provider.ClaudeCLI})
	ws, err := workspace.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	orch := pipeline.New(router, ws, pipeline.DefaultStages, nil, logger)
	store := settings.NewStore(filepath.Join(t.TempDir(), "config.json"))
	srv := New(context.Background(), Deps{
		Orchestrator: orch, Router: router, Workspace: ws, Settings: store, Hub: NewHub(),
		Static: fstest.MapFS{"index.html": {Data: []byte("ok")}}, Version: "test", Logger: logger,
	})
	ts := httptest.NewServer(srv.Handler())

	do(t, ts, http.MethodPut, "/api/prompts/fix.task", `{"value":"texto-confidencial"}`)
	ts.Close() // waits for the handlers, which log after answering

	if strings.Contains(logs.String(), "texto-confidencial") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestHealthEndpoints(t *testing.T) {
	ts, orch := newTestServer(t)
	var state workspace.State
	if err := json.NewDecoder(do(t, ts, "GET", "/api/workspace", "").Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	repo := state.Path
	if err := os.MkdirAll(filepath.Join(repo, "src", "pay"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "pay", "a.go"), []byte("time.Sleep(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "src", "cart"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "cart", "b.go"), []byte("package cart\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := do(t, ts, "GET", "/api/health/areas", "")
	var s struct {
		Areas []struct {
			Path string `json:"path"`
		} `json:"areas"`
		Warnings []string `json:"warnings"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&s) != nil || len(s.Areas) != 1 || s.Areas[0].Path != "src/pay" {
		t.Fatalf("areas: status %d, %+v", resp.StatusCode, s)
	}

	for body, want := range map[string]int{
		`{"area": "nope", "dry_run": true}`:           http.StatusBadRequest,
		`{"area": "../x", "dry_run": true}`:           http.StatusBadRequest,
		`{"area": ".", "rounds": 9, "dry_run": true}`: http.StatusBadRequest,
	} {
		if resp := do(t, ts, "POST", "/api/health/start", body); resp.StatusCode != want {
			t.Errorf("start %s: status %d, want %d", body, resp.StatusCode, want)
		}
	}

	if resp := do(t, ts, "POST", "/api/health/start", `{"area": "src/pay", "dry_run": true}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("simulated cleanup: status %d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := orch.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if run := orch.Snapshot(); run.Kind != pipeline.RunCleanup || run.Cleanup == nil || run.Cleanup.Verdict != pipeline.VerdictApproved {
		t.Fatalf("run = %+v", run.Cleanup)
	}
	for _, path := range []string{"/api/pipeline/resume"} {
		if resp := do(t, ts, "POST", path, ""); resp.StatusCode != http.StatusConflict {
			t.Errorf("%s after a cleanup: status %d, want 409", path, resp.StatusCode)
		}
	}
	if resp := do(t, ts, "POST", "/api/pipeline/retry", `{"story": 0, "step": "cleanup"}`); resp.StatusCode != http.StatusConflict {
		t.Errorf("retry after a cleanup: status %d, want 409", resp.StatusCode)
	}

	policy := state.Policy
	policy.Git = false
	data, _ := json.Marshal(policy)
	if resp := do(t, ts, "PUT", "/api/workspace/policy", string(data)); resp.StatusCode != http.StatusOK {
		t.Fatalf("policy: status %d", resp.StatusCode)
	}
	if resp := do(t, ts, "POST", "/api/health/start", `{"area": "src/pay"}`); resp.StatusCode != http.StatusConflict {
		t.Errorf("real cleanup without git: status %d, want 409", resp.StatusCode)
	}
}
