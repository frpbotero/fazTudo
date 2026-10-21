package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"llm-orchestrator/internal/provider"
)

// runLenses runs one review per lens in parallel and merges them into a
// single response whose verdict is the union of the lenses' issues.
func (o *Orchestrator) runLenses(ctx context.Context, ss *session, st *storyState, stage Stage, lenses []Lens, ref stepRef) (*provider.Response, bool) {
	started := time.Now()
	o.updateStep(ref, func(s *Step) {
		s.Status = StepRunning
		s.StartedAt = &started
		s.Note = fmt.Sprintf("%d revisores em paralelo", len(lenses))
	})
	o.logf(LevelInfo, "%s: %d revisores em paralelo (%s).", stage.Title, len(lenses), lensTitles(lenses))

	base := buildPrompt(stage, st.sc, ss.report, o.stepsBefore(ref))
	type result struct {
		resp *provider.Response
		err  error
	}
	results := make([]result, len(lenses))
	var wg sync.WaitGroup
	for i, l := range lenses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := o.exec.Execute(ctx, provider.Request{
				Role:     stage.Role + " · " + l.Title,
				System:   stage.System,
				Prompt:   base + lensFocus(l),
				Tier:     stage.Tier,
				WorkDir:  ss.repo,
				ReadOnly: stage.ReadOnly,
			}, o.providerLog)
			results[i] = result{resp, err}
		}()
	}
	wg.Wait()

	elapsed := time.Since(started)
	if ctx.Err() != nil {
		o.cancelStep(ref, elapsed)
		return nil, false
	}
	responses := make([]*provider.Response, len(lenses))
	for i, r := range results {
		if r.err != nil {
			o.failStep(ref, elapsed, fmt.Errorf("revisor \"%s\": %w", lenses[i].Title, r.err))
			return nil, false
		}
		responses[i] = r.resp
	}

	merged := mergeReviews(lenses, responses, o.stages, st.plan)
	merged.Duration = elapsed
	o.updateStep(ref, func(s *Step) {
		s.Status = StepDone
		s.Provider, s.Model, s.Effort = merged.Name, merged.Model, merged.Effort
		s.Output = merged.Output
		s.DurationMS = elapsed.Milliseconds()
	})
	return merged, true
}

func lensFocus(l Lens) string {
	return fmt.Sprintf("\n\n## Foco desta revisão: %s\n\n%s\nAvalie somente este foco; outros revisores cobrem os demais.", l.Title, l.Focus)
}

func lensTitles(lenses []Lens) string {
	titles := make([]string, len(lenses))
	for i, l := range lenses {
		titles[i] = l.Title
	}
	return strings.Join(titles, ", ")
}

// mergeReviews joins the lens reports and appends one verdict block: changes
// are requested when any lens requested them, with duplicate issues dropped.
// Without any parseable verdict the merged report carries none either.
func mergeReviews(lenses []Lens, responses []*provider.Response, stages []Stage, plan Plan) *provider.Response {
	var b strings.Builder
	merged := Verdict{Status: VerdictApproved, Issues: []Issue{}}
	parsed := false
	seen := map[string]bool{}
	simulated := false
	for i, resp := range responses {
		simulated = simulated || resp.Simulated
		fmt.Fprintf(&b, "## Revisão: %s\n\n%s\n\n", lenses[i].Title, stripJSONBlocks(resp.Output))
		v, ok := parseVerdict(resp.Output, stages, plan)
		if !ok {
			continue
		}
		parsed = true
		if !v.NeedsChanges() {
			continue
		}
		merged.Status = VerdictChangesRequested
		for _, is := range v.Issues {
			key := strings.ToLower(strings.TrimSpace(is.Location + "|" + is.Description))
			if !seen[key] {
				seen[key] = true
				merged.Issues = append(merged.Issues, is)
			}
		}
	}
	if parsed {
		data, _ := json.MarshalIndent(merged, "", "  ")
		fmt.Fprintf(&b, "```json\n%s\n```\n", data)
	}
	first := responses[0]
	return &provider.Response{
		Output: strings.TrimSpace(b.String()), Provider: first.Provider, Name: first.Name,
		Model: first.Model, Effort: first.Effort, Simulated: simulated,
	}
}
