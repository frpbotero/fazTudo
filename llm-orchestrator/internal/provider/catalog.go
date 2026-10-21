package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// effortOrder is the canonical ordering used when merging effort levels.
var effortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// CodexHome returns $CODEX_HOME or ~/.codex.
func CodexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// LoadCodexCatalog reads the models the Codex CLI has fetched for the signed-in
// account (models_cache.json) and the user's default model and effort
// (config.toml). Credentials files are never touched.
func LoadCodexCatalog(home string) (Catalog, error) {
	if home == "" {
		return Catalog{}, fmt.Errorf("diretório do Codex desconhecido")
	}
	path := filepath.Join(home, "models_cache.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, err
	}

	var cache struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
			Priority    int    `json:"priority"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return Catalog{}, fmt.Errorf("%s: %w", path, err)
	}

	sort.SliceStable(cache.Models, func(i, j int) bool { return cache.Models[i].Priority < cache.Models[j].Priority })
	cat := Catalog{Source: path}
	for _, m := range cache.Models {
		if m.Slug == "" {
			continue
		}
		model := Model{ID: m.Slug, Label: m.DisplayName, Hidden: m.Visibility != "list"}
		if model.Label == "" {
			model.Label = m.Slug
		}
		for _, l := range m.Levels {
			if l.Effort != "" {
				model.Efforts = append(model.Efforts, l.Effort)
			}
		}
		cat.Models = append(cat.Models, model)
	}
	if len(cat.Models) == 0 {
		return Catalog{}, fmt.Errorf("%s: nenhum modelo", path)
	}

	cat.DefaultModel, cat.DefaultEffort = readCodexDefaults(filepath.Join(home, "config.toml"))
	return cat, nil
}

var reTomlString = regexp.MustCompile(`^\s*([A-Za-z0-9_]+)\s*=\s*"([^"]*)"`)

// readCodexDefaults extracts top-level `model` and `model_reasoning_effort`
// from config.toml (values inside [profiles.*] tables are ignored).
func readCodexDefaults(path string) (model, effort string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			break
		}
		if m := reTomlString.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "model":
				model = m[2]
			case "model_reasoning_effort":
				effort = m[2]
			}
		}
	}
	return model, effort
}

// mergeEfforts returns the union of the visible models' effort levels in
// canonical order, or fallback when no model declares any.
func mergeEfforts(models []Model, fallback []string) []string {
	seen := map[string]bool{}
	for _, m := range models {
		if m.Hidden {
			continue
		}
		for _, e := range m.Efforts {
			seen[e] = true
		}
	}
	if len(seen) == 0 {
		return fallback
	}
	var out []string
	for _, e := range effortOrder {
		if seen[e] {
			out = append(out, e)
			delete(seen, e)
		}
	}
	rest := make([]string, 0, len(seen))
	for e := range seen {
		rest = append(rest, e)
	}
	slices.Sort(rest)
	return append(out, rest...)
}

// defaultsFromCatalog picks initial settings: the most preferred visible
// model for heavy stages, the CLI's own default for standard ones.
func defaultsFromCatalog(fallback Settings, cat Catalog, efforts []string) Settings {
	var visible []Model
	for _, m := range cat.Models {
		if !m.Hidden {
			visible = append(visible, m)
		}
	}
	if len(visible) == 0 {
		return fallback
	}
	has := func(id string) bool {
		return slices.ContainsFunc(visible, func(m Model) bool { return m.ID == id })
	}

	s := Settings{HeavyModel: visible[0].ID, StdModel: visible[0].ID}
	switch {
	case has(cat.DefaultModel):
		s.StdModel = cat.DefaultModel
	case len(visible) > 1:
		s.StdModel = visible[1].ID
	}
	switch {
	case slices.Contains(efforts, cat.DefaultEffort):
		s.Effort = cat.DefaultEffort
	case slices.Contains(efforts, fallback.Effort):
		s.Effort = fallback.Effort
	case len(efforts) > 0:
		s.Effort = efforts[0]
	}
	return s
}

func findModel(models []Model, id string) (Model, bool) {
	i := slices.IndexFunc(models, func(m Model) bool { return m.ID == id })
	if i < 0 {
		return Model{}, false
	}
	return models[i], true
}

// displayHome abbreviates the user's home directory as "~".
func displayHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && p != "" && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
