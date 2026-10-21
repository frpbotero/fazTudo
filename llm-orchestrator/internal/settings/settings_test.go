package settings

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/workspace"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	s := NewStore(path)

	if f, err := s.Load(); err != nil || f.DefaultProvider != "" {
		t.Fatalf("missing file must load empty: %+v %v", f, err)
	}

	off := false
	want := File{
		DefaultProvider: provider.Codex,
		Failover:        &off,
		Providers:       map[provider.ID]provider.Settings{provider.Claude: {HeavyModel: "opus", StdModel: "sonnet", Effort: "max"}},
		Repository:      "/tmp/repo",
	}
	if err := s.Update(func() File { return want }); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600", info.Mode().Perm())
	}

	got, err := NewStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != CurrentVersion || got.DefaultProvider != provider.Codex || got.Failover == nil || *got.Failover ||
		got.Providers[provider.Claude].Effort != "max" || got.Repository != "/tmp/repo" {
		t.Errorf("round trip mismatch: %+v", got)
	}

	st := s.Status()
	if !st.Enabled || st.SavedAt == nil || st.Error != "" {
		t.Errorf("unexpected status: %+v", st)
	}

	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestLoadRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{"), 0o600)
	if _, err := NewStore(bad).Load(); err == nil {
		t.Error("invalid JSON must fail")
	}

	future := filepath.Join(dir, "future.json")
	_ = os.WriteFile(future, []byte(`{"version": 99}`), 0o600)
	if _, err := NewStore(future).Load(); err == nil {
		t.Error("newer version must fail")
	}
}

func TestDisabledStore(t *testing.T) {
	s := NewStore("")
	called := false
	if err := s.Update(func() File { called = true; return File{} }); err != nil || called {
		t.Error("disabled store must not build or write")
	}
	if s.Status().Enabled {
		t.Error("disabled store must report disabled")
	}
}

func TestSaveErrorIsReported(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	_ = os.WriteFile(blocker, nil, 0o600)
	s := NewStore(filepath.Join(blocker, "config.json")) // parent is a file
	if err := s.Update(func() File { return File{} }); err == nil {
		t.Fatal("expected error")
	}
	if s.Status().Error == "" {
		t.Error("status must expose the last error")
	}
}

func TestConcurrentUpdatesKeepValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := NewStore(path)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Update(func() File { return File{Repository: string(rune('a' + i))} })
		}()
	}
	wg.Wait()
	if _, err := s.Load(); err != nil {
		t.Fatalf("file corrupted by concurrent writes: %v", err)
	}
}

func TestApplyAndCapture(t *testing.T) {
	newRouter := func() *provider.Router {
		return provider.NewRouter(true, []provider.CLI{provider.ClaudeCLI, provider.CodexCLI})
	}
	repo := t.TempDir()
	off := false
	f := File{
		DefaultProvider: provider.Codex,
		Failover:        &off,
		Providers: map[provider.ID]provider.Settings{
			provider.Claude: {HeavyModel: "opus", StdModel: "sonnet", Effort: "max"},
			"gemini":        {HeavyModel: "x", StdModel: "y"},                                        // unknown: skipped
			provider.Codex:  {HeavyModel: "gpt-6-astra", StdModel: "gpt-5.6-terra", Effort: "ultra"}, // invalid effort: skipped
		},
		Repository: repo,
	}

	r := newRouter()
	ws, _ := workspace.NewManager("")
	errs := Apply(f, r, ws, Overrides{})
	if len(errs) != 2 {
		t.Errorf("want 2 errors (gemini, codex effort), got %v", errs)
	}
	cfg := r.Config()
	if cfg.DefaultProvider != provider.Codex || cfg.Failover || cfg.Providers[provider.Claude].Effort != "max" ||
		cfg.Providers[provider.Codex] != provider.CodexCLI.Defaults || ws.Path() != repo {
		t.Errorf("valid parts not applied: %+v repo=%s", cfg, ws.Path())
	}

	captured := Capture(r, ws)
	if captured.DefaultProvider != provider.Codex || *captured.Failover || captured.Repository != repo {
		t.Errorf("capture mismatch: %+v", captured)
	}

	// Flags win over the file.
	r2 := newRouter()
	ws2, _ := workspace.NewManager(t.TempDir())
	flagRepo := ws2.Path()
	Apply(f, r2, ws2, Overrides{DefaultProvider: true, Repository: true})
	if r2.Config().DefaultProvider != provider.Claude || ws2.Path() != flagRepo {
		t.Errorf("overrides ignored: provider=%s repo=%s", r2.Config().DefaultProvider, ws2.Path())
	}

	// A repository that no longer exists is reported, not fatal.
	r3 := newRouter()
	ws3, _ := workspace.NewManager("")
	if errs := Apply(File{Repository: "/definitely/not/here"}, r3, ws3, Overrides{}); len(errs) != 1 || ws3.Path() != "" {
		t.Errorf("missing repo: errs=%v path=%q", errs, ws3.Path())
	}
}
