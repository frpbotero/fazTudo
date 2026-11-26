package pipeline

import (
	"slices"
	"strings"
	"time"
)

// Criterion is one acceptance criterion as Passo 0 found it in the code.
type Criterion struct {
	Text     string `json:"criterion"`
	Status   string `json:"status"` // CriterionExists, CriterionPartial, CriterionMissing or CriterionAmbiguous
	Evidence string `json:"evidence,omitempty"`
}

// Criterion statuses.
const (
	CriterionExists    = "existente"
	CriterionPartial   = "parcial"
	CriterionMissing   = "ausente"
	CriterionAmbiguous = "ambiguo"
)

var criterionStatuses = []string{CriterionExists, CriterionPartial, CriterionMissing, CriterionAmbiguous}

// PendingDecision is a product decision the story leaves open. The answer,
// once given, is kept apart from the story, which is never rewritten.
type PendingDecision struct {
	Question   string     `json:"question"`
	Options    []string   `json:"options,omitempty"`
	Impact     string     `json:"impact,omitempty"`
	Answer     string     `json:"answer,omitempty"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
}

// Readiness is what Passo 0 found before anything was built: the decision,
// each criterion with evidence, and the product decisions still open.
type Readiness struct {
	Decision  string            `json:"decision"`
	Reason    string            `json:"reason,omitempty"`
	Criteria  []Criterion       `json:"criteria,omitempty"`
	Decisions []PendingDecision `json:"decisions,omitempty"`
	// Notes are what the orchestrator changed in the agent's answer, and why.
	Notes []string `json:"notes,omitempty"`
}

// normalizeCriterion keeps the known statuses; anything else is ambiguous,
// never "exists".
func normalizeCriterion(c Criterion) Criterion {
	c.Text, c.Evidence = strings.TrimSpace(c.Text), strings.TrimSpace(c.Evidence)
	s := strings.ToLower(strings.TrimSpace(c.Status))
	s = strings.NewReplacer("í", "i", "ú", "u").Replace(s)
	switch s {
	case "existe", "atendido", "exists", "met":
		s = CriterionExists
	case "partial":
		s = CriterionPartial
	case "missing", "absent":
		s = CriterionMissing
	}
	if !slices.Contains(criterionStatuses, s) {
		s = CriterionAmbiguous
	}
	c.Status = s
	return c
}

// readiness turns the assessment into the story's readiness report: a reuse
// needs every criterion to exist, with evidence, and decisions the user
// already answered are not asked again.
func readiness(a Assessment, answered []PendingDecision) Readiness {
	r := Readiness{Decision: a.Decision, Reason: a.Reason}
	for _, c := range a.Criteria {
		if c = normalizeCriterion(c); c.Text != "" {
			r.Criteria = append(r.Criteria, c)
		}
	}
	for _, d := range a.Decisions {
		d.Question, d.Impact = strings.TrimSpace(d.Question), strings.TrimSpace(d.Impact)
		d.Answer, d.AnsweredAt = "", nil // not the agent's to answer
		if d.Question == "" || isAnswered(d.Question, answered) {
			continue
		}
		r.Decisions = append(r.Decisions, d)
	}
	if r.Decision == DecisionReuse {
		unmet := slices.ContainsFunc(r.Criteria, func(c Criterion) bool { return c.Status != CriterionExists || c.Evidence == "" })
		if len(r.Criteria) == 0 || unmet {
			r.Decision = DecisionExtend
			r.Notes = append(r.Notes, "reuse rebaixado para extend: nem todo critério existe com evidência")
		}
	}
	return r
}

func isAnswered(question string, answered []PendingDecision) bool {
	key := normalizeQuestion(question)
	return slices.ContainsFunc(answered, func(d PendingDecision) bool { return d.Answer != "" && normalizeQuestion(d.Question) == key })
}

func normalizeQuestion(q string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSuffix(strings.TrimSpace(q), "?"))), " ")
}
