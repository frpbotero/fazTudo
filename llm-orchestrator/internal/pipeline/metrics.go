package pipeline

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// newRunID identifies a run in every record it leaves: steps, calls, the
// token log and the delivery package.
func newRunID(at time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return at.Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

// StoryMetrics is where a story's time and effort went. Times add up the
// steps of each kind; tokens and costs come from the calls, observed and
// estimated apart.
type StoryMetrics struct {
	Story         string  `json:"story"`
	DurationMS    int64   `json:"duration_ms"`
	QueueMS       int64   `json:"queue_ms"`
	AgentMS       int64   `json:"agent_ms"`
	VerifyMS      int64   `json:"verify_ms"`
	IntegrationMS int64   `json:"integration_ms"`
	FixRounds     int     `json:"fix_rounds"`
	Pending       int     `json:"pending"`
	Calls         int     `json:"calls"`
	Tokens        int64   `json:"tokens"`
	CostObserved  float64 `json:"cost_observed_usd,omitempty"`
	CostEstimated float64 `json:"cost_estimated_usd,omitempty"`
	UnknownCost   int     `json:"unknown_cost,omitempty"`
	// ReviewMinutes and ReworkMinutes are what people logged by hand.
	ReviewMinutes int `json:"review_minutes,omitempty"`
	ReworkMinutes int `json:"rework_minutes,omitempty"`
}

// storyMetrics sums a story's steps, calls and logged review time.
func storyMetrics(st StoryRun, promotions []Promotion) StoryMetrics {
	m := StoryMetrics{Story: st.ID, DurationMS: st.DurationMS}
	if m.Story == "" {
		m.Story = st.Title
	}
	for _, s := range st.Steps {
		m.QueueMS += s.QueueMS
		switch {
		case s.Kind == "fix":
			m.FixRounds++
			m.AgentMS += s.DurationMS
		case len(s.Checks) > 0 || s.Kind == "verify" && len(s.Calls) == 0:
			m.VerifyMS += s.DurationMS
		case len(s.Calls) > 0:
			m.AgentMS += s.DurationMS
		}
		if s.Status == StepWarning || s.Status == StepFailed {
			m.Pending += len(s.Issues)
		}
	}
	label := st.ID
	for _, p := range promotions {
		for _, s := range p.Stories {
			if label != "" && strings.HasPrefix(s, label) {
				m.IntegrationMS += p.DurationMS
			}
		}
	}
	if u := st.Usage; u != nil {
		m.Calls, m.Tokens = u.Calls, u.Tokens()
		m.CostObserved, m.CostEstimated, m.UnknownCost = u.CostObservedUSD, u.CostEstimatedUSD, u.UnknownCost
	}
	for _, h := range st.HumanReview {
		m.ReviewMinutes += h.Minutes
		m.ReworkMinutes += h.ReworkMinutes
	}
	return m
}

// Metrics are the run's story metrics, recomputed from what it recorded.
func (r Run) Metrics() []StoryMetrics {
	out := make([]StoryMetrics, len(r.Stories))
	for i, st := range r.Stories {
		out[i] = storyMetrics(st, r.Promotions)
	}
	return out
}

// HumanReview is time a person logged reviewing a story or reworking it:
// entered by hand, never measured behind their back.
type HumanReview struct {
	At            time.Time `json:"at"`
	Minutes       int       `json:"minutes"`
	ReworkMinutes int       `json:"rework_minutes,omitempty"`
	Note          string    `json:"note,omitempty"`
}

// ErrBadReviewLog is returned for a review time out of bounds.
var ErrBadReviewLog = errors.New("registro de revisão inválido")

// LogReview records review and rework time a person spent on story si.
func (o *Orchestrator) LogReview(si int, h HumanReview) error {
	if h.Minutes < 0 || h.ReworkMinutes < 0 || h.Minutes+h.ReworkMinutes == 0 || h.Minutes > 24*60 || h.ReworkMinutes > 24*60 || len(h.Note) > 500 {
		return fmt.Errorf("%w: informe de 1 a 1440 minutos e nota de até 500 caracteres", ErrBadReviewLog)
	}
	o.mu.Lock()
	if si < 0 || si >= len(o.run.Stories) || o.run.Repository == "" {
		o.mu.Unlock()
		return fmt.Errorf("%w: US inexistente", ErrBadReviewLog)
	}
	h.At, h.Note = time.Now(), redactSecrets(strings.TrimSpace(h.Note))
	o.run.Stories[si].HumanReview = append(o.run.Stories[si].HumanReview, h)
	o.isJournalOwner = true
	o.mu.Unlock()
	o.notify()
	o.requestJournalSave()
	return nil
}
