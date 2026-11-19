// Package health measures, without any LLM, where a repository's structure is
// most worth cleaning: files that change often, files that change together,
// dependency cycles and special cases. It suggests the areas a code health
// run should start from.
package health

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"

	"llm-orchestrator/internal/quality"
)

// AreaScore is one candidate area and the indicators behind its score.
type AreaScore struct {
	Path         string   `json:"path"`
	Score        float64  `json:"score"`
	Churn        int      `json:"churn"`
	CoChange     int      `json:"co_change"`
	Cycles       *int     `json:"cycles,omitempty"` // nil without a code graph
	SpecialCases int      `json:"special_cases"`
	Reasons      []string `json:"reasons"`
}

// AreaSuggestion is the answer to "where is cleaning worth it?".
type AreaSuggestion struct {
	Areas    []AreaScore `json:"areas"`
	Commits  int         `json:"commits"`
	Graph    bool        `json:"graph"`
	Warnings []string    `json:"warnings,omitempty"`
}

// topAreas is how many areas a suggestion lists.
const topAreas = 3

// minCommits is the history below which change-based indicators are noise.
const minCommits = 20

// indicators of one area before normalization.
type indicators struct {
	churn, coChange, special int
	cycleWith                []string
	kinds                    map[string]int
}

// Suggest scores the repository's areas without calling any model and
// returns the most worth cleaning. graph is graphify's graph.json; "" or a
// missing file leaves cycles out of the score.
func Suggest(ctx context.Context, repo, graph string) (AreaSuggestion, error) {
	var s AreaSuggestion
	files := productFiles(quality.CodeFiles(repo))
	areaOf := areaFunc(commonDir(files))
	stats := map[string]*indicators{}
	at := func(area string) *indicators {
		if stats[area] == nil {
			stats[area] = &indicators{kinds: map[string]int{}}
		}
		return stats[area]
	}
	for _, f := range files {
		area := areaOf(f)
		if area == "" {
			continue
		}
		in := at(area)
		for kind, n := range countSpecial(repo, f) {
			in.special += n
			in.kinds[kind] += n
		}
	}

	h, err := readHistory(ctx, repo)
	if err != nil {
		return s, err
	}
	s.Commits = h.commits
	for f, n := range h.churn {
		if area := areaOf(f); area != "" && stats[area] != nil {
			stats[area].churn += n
		}
	}
	for _, p := range h.pairs {
		a, b := areaOf(p[0]), areaOf(p[1])
		switch {
		case a == "" || b == "" || stats[a] == nil || stats[b] == nil:
		case a == b:
			stats[a].coChange++
		default: // a link across areas the code does not show
			stats[a].coChange += 2
			stats[b].coChange += 2
		}
	}
	if h.commits < minCommits {
		s.Warnings = append(s.Warnings, fmt.Sprintf("Histórico curto (%d commits): frequência de mudança e arquivos que mudam juntos dizem pouco.", h.commits))
	}

	if graph != "" {
		cycles, err := readCycles(graph, func(f string) string {
			if a := areaOf(f); stats[a] != nil {
				return a
			}
			return ""
		})
		if err == nil {
			s.Graph = true
			for a, others := range cycles {
				stats[a].cycleWith = others
			}
		}
	}
	if !s.Graph {
		s.Warnings = append(s.Warnings, "Grafo do código indisponível (graphify): ciclos de dependência fora da pontuação.")
	}
	s.Areas = rank(stats, s.Graph)
	return s, nil
}

// productFiles drops files under dot directories (.github, .specify…): they
// hold tooling, not the product's code.
func productFiles(files []string) []string {
	return slices.DeleteFunc(slices.Clone(files), func(f string) bool {
		return strings.HasPrefix(f, ".") || strings.Contains(f, "/.")
	})
}

// commonDir is the deepest directory holding every file that sits in a
// directory ("" = the root), so a repository whose code lives in one folder
// gets areas inside it. Loose files at the root (a start script) belong to no
// area and do not count.
func commonDir(files []string) string {
	files = slices.DeleteFunc(slices.Clone(files), func(f string) bool { return !strings.Contains(f, "/") })
	if len(files) == 0 {
		return ""
	}
	prefix := strings.Split(path.Dir(files[0]), "/")
	for _, f := range files[1:] {
		dir := strings.Split(path.Dir(f), "/")
		n := 0
		for n < len(prefix) && n < len(dir) && prefix[n] == dir[n] {
			n++
		}
		prefix = prefix[:n]
	}
	if len(prefix) == 0 || prefix[0] == "." {
		return ""
	}
	return strings.Join(prefix, "/")
}

// areaFunc maps a file to its area below base: the first two directories
// when the file is nested that deep, else the first one. Loose files directly
// in base belong to none: cleaning base would be cleaning everything.
func areaFunc(base string) func(string) string {
	return func(file string) string {
		rel := file
		if base != "" {
			var ok bool
			if rel, ok = strings.CutPrefix(file, base+"/"); !ok {
				return ""
			}
		}
		parts := strings.Split(rel, "/")
		switch {
		case len(parts) >= 3:
			return path.Join(base, parts[0], parts[1])
		case len(parts) == 2:
			return path.Join(base, parts[0])
		}
		return ""
	}
}

// rank normalizes each indicator by its maximum across areas, adds them with
// equal weights and keeps the top areas that scored at all.
func rank(stats map[string]*indicators, graph bool) []AreaScore {
	var maxChurn, maxCo, maxCycles, maxSpecial int
	for _, in := range stats {
		maxChurn, maxCo = max(maxChurn, in.churn), max(maxCo, in.coChange)
		maxCycles, maxSpecial = max(maxCycles, len(in.cycleWith)), max(maxSpecial, in.special)
	}
	var out []AreaScore
	for area, in := range stats {
		parts := []float64{ratio(in.churn, maxChurn), ratio(in.coChange, maxCo), ratio(in.special, maxSpecial)}
		a := AreaScore{Path: area, Churn: in.churn, CoChange: in.coChange, SpecialCases: in.special}
		if graph {
			n := len(in.cycleWith)
			a.Cycles = &n
			parts = append(parts, ratio(n, maxCycles))
		}
		for _, p := range parts {
			a.Score += p
		}
		if a.Score == 0 {
			continue
		}
		a.Score = math.Round(a.Score*100) / 100
		a.Reasons = reasons(in, parts)
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b AreaScore) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out[:min(len(out), topAreas)]
}

func ratio(n, top int) float64 {
	if top == 0 {
		return 0
	}
	return float64(n) / float64(top)
}

// reasons explains the indicators that weighed most, strongest first.
func reasons(in *indicators, parts []float64) []string {
	type reason struct {
		weight float64
		text   string
	}
	var rs []reason
	if in.churn > 0 {
		rs = append(rs, reason{parts[0], fmt.Sprintf("Muda muito: %d alteração(ões) de arquivo nos commits recentes.", in.churn)})
	}
	if in.coChange > 0 {
		rs = append(rs, reason{parts[1], fmt.Sprintf("Arquivos que mudam juntos: peso %d (pares entre áreas contam em dobro).", in.coChange)})
	}
	if in.special > 0 {
		rs = append(rs, reason{parts[2], fmt.Sprintf("Casos especiais: %d (%s).", in.special, kindList(in.kinds))})
	}
	if len(in.cycleWith) > 0 {
		rs = append(rs, reason{parts[len(parts)-1], "Ciclo de dependência com: " + strings.Join(in.cycleWith, ", ") + "."})
	}
	slices.SortStableFunc(rs, func(a, b reason) int { return cmp.Compare(b.weight, a.weight) })
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.text
	}
	return out
}

func kindList(kinds map[string]int) string {
	var labels []string
	for _, k := range specialKinds {
		if n := kinds[k.label]; n > 0 {
			labels = append(labels, fmt.Sprintf("%s %d", k.label, n))
		}
	}
	return strings.Join(labels, ", ")
}
