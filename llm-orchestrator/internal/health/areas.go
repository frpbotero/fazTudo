// Package health measures, without any LLM, where a repository's structure is
// most worth cleaning: files that change often, files that change together,
// dependency cycles and special cases. It suggests the areas a code health
// run should start from.
package health

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
