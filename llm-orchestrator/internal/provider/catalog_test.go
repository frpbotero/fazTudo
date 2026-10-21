package provider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cacheFixture = `{
  "fetched_at": "2026-10-21T14:04:00Z",
  "models": [
    {"slug": "luna", "display_name": "Luna", "visibility": "list", "priority": 9,
     "supported_reasoning_levels": [{"effort": "low"}, {"effort": "medium"}, {"effort": "max"}]},
    {"slug": "astra", "display_name": "Astra", "visibility": "list", "priority": 2,
     "supported_reasoning_levels": [{"effort": "low"}, {"effort": "ultra"}]},
    {"slug": "reserve", "display_name": "Reserve", "visibility": "hide", "priority": 4,
     "supported_reasoning_levels": [{"effort": "minimal"}]},
    {"slug": "terra", "display_name": "Terra", "visibility": "list", "priority": 8,
     "supported_reasoning_levels": [{"effort": "medium"}, {"effort": "high"}]}
  ]
}`

func writeCodexHome(t *testing.T, cache, config string) string {
	t.Helper()
	dir := t.TempDir()
	if cache != "" {
		if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(cache), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadCodexCatalog(t *testing.T) {
	home := writeCodexHome(t, cacheFixture,
		"model = \"terra\"\nmodel_reasoning_effort = \"high\"\n\n[profiles.fast]\nmodel = \"luna\"\n")

	cat, err := LoadCodexCatalog(home)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range cat.Models {
		ids = append(ids, m.ID)
	}
	if got := strings.Join(ids, ","); got != "astra,reserve,terra,luna" {
		t.Errorf("order by priority = %s", got)
	}
	if !cat.Models[1].Hidden || cat.Models[0].Hidden {
		t.Error("visibility not mapped to Hidden")
	}
	if cat.DefaultModel != "terra" || cat.DefaultEffort != "high" {
		t.Errorf("defaults = %s/%s (profile values must be ignored)", cat.DefaultModel, cat.DefaultEffort)
	}

	efforts := mergeEfforts(cat.Models, nil)
	if got := strings.Join(efforts, ","); got != "low,medium,high,max,ultra" {
		t.Errorf("merged efforts = %s (hidden models excluded, canonical order)", got)
	}

	s := defaultsFromCatalog(CodexCLI.Defaults, cat, efforts)
	if s.HeavyModel != "astra" || s.StdModel != "terra" || s.Effort != "high" {
		t.Errorf("defaults from catalog = %+v", s)
	}
}

func TestLoadCodexCatalogErrors(t *testing.T) {
	if _, err := LoadCodexCatalog(writeCodexHome(t, "", "")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing cache: %v", err)
	}
	if _, err := LoadCodexCatalog(writeCodexHome(t, "{", "")); err == nil {
		t.Error("invalid JSON must fail")
	}
	if _, err := LoadCodexCatalog(writeCodexHome(t, `{"models": []}`, "")); err == nil {
		t.Error("empty catalog must fail")
	}
}

func TestRouterUsesLiveCatalog(t *testing.T) {
	t.Setenv("CODEX_HOME", writeCodexHome(t, cacheFixture, "model = \"terra\"\n"))
	r := NewRouter(true, []CLI{ClaudeCLI, CodexCLI}, WithLookPath(allInstalled))

	var codex State
	for _, s := range r.States() {
		if s.ID == Codex {
			codex = s
		}
	}
	if codex.Catalog == "" || len(codex.Models) != 4 || codex.HeavyModel != "astra" || codex.StdModel != "terra" {
		t.Fatalf("codex state not built from catalog: %+v", codex)
	}

	// Per-model effort validation: terra does not support "ultra".
	err := r.UpdateConfig(ConfigUpdate{Providers: map[ID]Settings{Codex: {HeavyModel: "astra", StdModel: "terra", Effort: "ultra"}}})
	if err == nil || !strings.Contains(err.Error(), "Terra") {
		t.Errorf("expected per-model effort error, got %v", err)
	}
	if err := r.UpdateConfig(ConfigUpdate{Providers: map[ID]Settings{Codex: {HeavyModel: "astra", StdModel: "astra", Effort: "ultra"}}}); err != nil {
		t.Errorf("astra supports ultra: %v", err)
	}
	// Custom IDs outside the catalog stay allowed.
	if err := r.UpdateConfig(ConfigUpdate{Providers: map[ID]Settings{Codex: {HeavyModel: "my-model", StdModel: "terra", Effort: "medium"}}}); err != nil {
		t.Errorf("custom model rejected: %v", err)
	}
}

func TestRouterFallsBackWithoutCatalog(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	r := NewRouter(true, []CLI{CodexCLI}, WithLookPath(allInstalled))
	s := r.States()[0]
	if s.Catalog != "" || len(s.Models) != len(CodexCLI.Models) || s.Settings != CodexCLI.Defaults {
		t.Errorf("fallback not used: %+v", s)
	}
}
