package pipeline

import (
	"slices"
	"strings"

	"llm-orchestrator/internal/workspace"
)

// riskRules are the policy's rules; nil means the defaults.
func riskRules(p workspace.Policy) []workspace.RiskRule {
	if p.RiskRules == nil {
		return workspace.DefaultRiskRules
	}
	return p.RiskRules
}

// assessStoryRisk adds what the story's text and the files changed so far
// trigger to the story's risk. Requirements only grow: a later assessment on
// the real diff can raise the risk, never lower it, and the model's plan has
// no say in it.
func (o *Orchestrator) assessStoryRisk(ss *session, st *storyState, changed []string) {
	text := st.sc.Story.Title + "\n" + st.sc.Story.Content
	next := st.risk.Merge(workspace.AssessRisk(riskRules(ss.policy), text, changed))
	added := slices.DeleteFunc(slices.Clone(next.Required), func(id string) bool { return slices.Contains(st.risk.Required, id) })
	raised := next.Level != st.risk.Level
	st.risk = next
	o.updateStory(st.si, func(r *StoryRun) { risk := next; r.Risk = &risk })
	if len(added) == 0 && !raised {
		return
	}
	var titles, unknown []string
	for _, id := range added {
		if s, ok := o.lookupStage(id); ok {
			titles = append(titles, s.Title)
		} else {
			unknown = append(unknown, id)
		}
	}
	msg := "Risco " + next.Level + " (" + strings.Join(next.Reasons, "; ") + ")"
	if len(titles) > 0 {
		msg += ": etapas obrigatórias " + strings.Join(titles, ", ")
	}
	o.logf(LevelWarn, "%s.", msg)
	if len(unknown) > 0 {
		o.logf(LevelWarn, "Regra de risco exige etapa inexistente neste fluxo (%s); a US não poderá ser aprovada sem ela.", strings.Join(unknown, ", "))
	}
	o.enforceRisk(ss, st)
}

// enforceRisk puts the stages the risk requires back into the plan and the
// story's steps: neither the architect nor a fast flow preset removes them.
func (o *Orchestrator) enforceRisk(ss *session, st *storyState) {
	if len(st.risk.Required) == 0 {
		return
	}
	note := "Exigida pela política de risco: " + strings.Join(st.risk.Rules, ", ")
	changed := false
	for _, id := range st.risk.Required {
		stage, ok := o.lookupStage(id)
		if !ok {
			continue
		}
		if stage.Selectable && !st.plan.Includes(id) {
			st.plan.Agents = append(st.plan.Agents, id)
			changed = true
		}
		o.updateStep(stepRef{st.si, id}, func(s *Step) {
			if s.Status == StepOmitted {
				s.Status, s.Note = StepPending, note
			}
		})
	}
	if changed {
		plan := st.plan
		o.updateStory(st.si, func(r *StoryRun) { r.Plan = &plan })
	}
}

// requiredByRisk reports whether the story's risk makes the stage mandatory.
func (st *storyState) requiredByRisk(id string) bool { return slices.Contains(st.risk.Required, id) }

// missingRequired lists the mandatory stages that did not reach a result: a
// gate without validation, or a stage that never ran. Such a story cannot be
// approved.
func (o *Orchestrator) missingRequired(st *storyState, steps []Step) []string {
	var missing []string
	for _, id := range st.risk.Required {
		stage, ok := o.lookupStage(id)
		i := stepIndex(steps, id)
		switch {
		case !ok || i < 0:
			missing = append(missing, id)
		case stage.Gate || stage.Verifier:
			if steps[i].Validation == "" {
				missing = append(missing, stage.Title)
			}
		case steps[i].Status != StepDone && steps[i].Status != StepWarning:
			missing = append(missing, stage.Title)
		}
	}
	return missing
}

func (o *Orchestrator) lookupStage(id string) (Stage, bool) {
	i := slices.IndexFunc(o.stages, func(s Stage) bool { return s.ID == id })
	if i < 0 {
		return Stage{}, false
	}
	return o.stages[i], true
}

// warnRiskVersusFlow tells, before the run, which stages the flow turns off
// that the risk rules bring back when a story needs them.
func (o *Orchestrator) warnRiskVersusFlow(policy workspace.Policy) {
	var back []string
	for _, rule := range riskRules(policy) {
		for _, id := range rule.Require {
			if s, ok := o.lookupStage(id); ok && policy.StageDisabled(id) && !slices.Contains(back, s.Title) {
				back = append(back, s.Title)
			}
		}
	}
	if len(back) > 0 {
		o.logf(LevelInfo, "O fluxo escolhido desliga %s; a política de risco as religa nas US que as exigirem.", strings.Join(back, ", "))
	}
}
