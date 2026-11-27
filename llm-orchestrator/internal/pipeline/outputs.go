// Stage outputs: what Passo 0, the specification and the architect decide
// for the rest of the story.

package pipeline

import (
	"cmp"
	"fmt"
	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"slices"
	"strings"
)

// applyAssessment records the Passo 0 decision. It returns true when the
// story ends here: already implemented (reuse) or blocked.
func (o *Orchestrator) applyAssessment(st *storyState, ref stepRef, resp *provider.Response) bool {
	a := parseAssessment(resp.Output)
	r := readiness(a, o.storyRun(st.si).Answers)
	kind, note := changeKind(a.Change, a.ChangeReason, st.sc.Story.Title+"\n"+st.sc.Story.Content)
	if note != "" {
		r.Notes = append(r.Notes, note)
	}
	o.setChange(st, kind, strings.TrimSpace(a.ChangeReason))
	o.updateStory(st.si, func(sr *StoryRun) { rr := r; sr.Readiness = &rr })
	for _, n := range r.Notes {
		o.logf(LevelWarn, "Passo 0 de %s: %s.", st.sc.Story.Title, n)
	}
	reason := cmp.Or(r.Reason, "sem motivo informado")
	if n := len(r.Decisions); n > 0 {
		var questions []string
		for _, d := range r.Decisions {
			questions = append(questions, d.Question)
		}
		o.updateStep(ref, func(s *Step) { s.Note = fmt.Sprintf("Aguardando %d decisão(ões) de produto", n) })
		o.skipRemaining(st.si, "Aguardando decisão de produto (Passo 0)")
		st.waiting = n
		o.logf(LevelWarn, "%s aguarda decisão de produto: %s. Responda para retomar; as US independentes seguem.",
			st.sc.Story.Title, strings.Join(questions, " · "))
		return true
	}
	switch r.Decision {
	case DecisionReuse:
		o.updateStep(ref, func(s *Step) { s.Note = "Já atendida: " + reason })
		// The claim is checked, not taken: the executable gates still run.
		o.skipRemainingExcept(st.si, "Já atendida pelo código existente (Passo 0)", o.isVerifierStep)
		st.reused = true
		o.logf(LevelSuccess, "%s já está implementada segundo o Passo 0; só os comandos do projeto vão conferir.", st.sc.Story.Title)
		return false
	case DecisionBlock:
		o.updateStep(ref, func(s *Step) { s.Note = "Bloqueada: " + reason })
		o.skipRemaining(st.si, "US bloqueada no Passo 0")
		st.blocked = reason
		return true
	}
	o.updateStep(ref, func(s *Step) { s.Note = "Decisão: " + decisionLabel(r.Decision) + "; " + changeLabel(kind) })
	o.logf(LevelInfo, "Passo 0: %s; %s.", decisionLabel(r.Decision), changeLabel(kind))
	return false
}

func decisionLabel(d string) string {
	switch d {
	case DecisionExtend:
		return "estender (só o delta)"
	case DecisionRefactor:
		return "refatorar"
	}
	return "implementar"
}

// skipRemainingExcept marks the story's pending steps as skipped, but for
// those keep reports.
func (o *Orchestrator) skipRemainingExcept(si int, note string, keep func(Step) bool) {
	o.mu.Lock()
	for j := range o.run.Stories[si].Steps {
		if s := &o.run.Stories[si].Steps[j]; s.Status == StepPending && !keep(*s) {
			s.Status, s.Note = StepSkipped, note
		}
	}
	o.mu.Unlock()
	o.notify()
}

func (o *Orchestrator) isVerifierStep(s Step) bool {
	stage, ok := o.lookupStage(s.ID)
	return ok && stage.Verifier
}

// skipRemaining marks the story's pending steps as skipped.
func (o *Orchestrator) skipRemaining(si int, note string) {
	o.mu.Lock()
	for j := range o.run.Stories[si].Steps {
		if s := &o.run.Stories[si].Steps[j]; s.Status == StepPending {
			s.Status, s.Note = StepSkipped, note
		}
	}
	o.mu.Unlock()
	o.notify()
}

// testList is the spec stage's machine-readable list of required tests.
type testList struct {
	Tests []quality.Requirement `json:"tests"`
}

// applySpec adds the tests listed by the spec stage to the IDs found in the
// story document.
func (o *Orchestrator) applySpec(st *storyState, resp *provider.Response) {
	if listed, ok := lastJSON(resp.Output, func(l testList) bool { return l.Tests != nil }); ok {
		for i := range listed.Tests {
			listed.Tests[i].Kind = strings.ToLower(strings.TrimSpace(listed.Tests[i].Kind))
		}
		st.reqs = quality.MergeRequirements(st.reqs, listed.Tests)
	}
	if len(st.reqs) > 0 {
		o.logf(LevelInfo, "%d caso(s) de teste com ID serão rastreados até os arquivos de teste.", len(st.reqs))
	}
}

// applyPlan parses the planner output and marks unselected agents as omitted.
func (o *Orchestrator) applyPlan(si int, sc storyContext, resp *provider.Response) Plan {
	plan, ok := parsePlan(resp.Output, o.stages)
	switch {
	case ok:
	case resp.Simulated:
		plan = heuristicPlan(sc.Story.Content, o.stages)
	default:
		plan = fullPlan(o.stages, "fallback")
		o.logf(LevelWarn, "O arquiteto não retornou um plano em JSON; todos os agentes serão executados.")
	}

	policy := o.ws.Policy()
	plan.Agents = slices.DeleteFunc(plan.Agents, policy.StageDisabled)
	var names []string
	o.mu.Lock()
	st := &o.run.Stories[si]
	st.Plan = &plan
	for _, stage := range o.stages {
		if !stage.Selectable {
			continue
		}
		if plan.Includes(stage.ID) {
			names = append(names, stage.Title)
			continue
		}
		// A stage that already ran (a specialty before the plan) stays as it ended.
		if i := stepIndex(st.Steps, stage.ID); i >= 0 && !policy.StageDisabled(stage.ID) && st.Steps[i].Status == StepPending {
			st.Steps[i].Status = StepOmitted
			st.Steps[i].Note = "Não selecionado pelo arquiteto"
		}
	}
	o.mu.Unlock()
	o.notify()

	if len(names) == 0 {
		names = append(names, "nenhum agente opcional")
	}
	o.logf(LevelInfo, "Plano do arquiteto: %s.", strings.Join(names, ", "))
	for _, gap := range plan.Gaps {
		o.logf(LevelWarn, "Plano de %s: %s.", sc.Story.Title, gap)
	}
	return plan
}
