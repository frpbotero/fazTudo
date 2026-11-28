package pipeline

import (
	"context"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/provider"
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
	// The first assessment sets the level; only a rule that matched is news.
	raised := next.Level != st.risk.Level && len(next.Rules) > 0
	st.risk = next
	o.updateStory(st.si, func(r *StoryRun) { risk := next; r.Risk = &risk })
	if len(added) == 0 && !raised {
		return
	}
	var titles, unknown []string
	for _, id := range added {
		s, ok := o.lookupStage(id)
		if !ok {
			unknown = append(unknown, id)
			continue
		}
		titles = append(titles, s.Title)
		if i := slices.IndexFunc(o.stages, func(x Stage) bool { return x.ID == id }); i < st.stageAt && s.Specialist && !o.stageRan(st, id) {
			// A planning specialty found necessary only by the diff cannot plan
			// what is already built: it reviews it before the next gate, and
			// the gates judge its requirements.
			st.late = append(st.late, id)
			o.logf(LevelWarn, "%s passou a ser exigida pelo diff depois de sua vez; roda agora sobre o código já feito, antes das verificações.", s.Title)
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
	o.warnMigrationChecks(ss, st)
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

// stageRan reports whether the story's step for the stage already reached a
// result: the architect may have chosen it before the risk required it.
func (o *Orchestrator) stageRan(st *storyState, id string) bool {
	s := o.stepAt(stepRef{st.si, id})
	return s.Status == StepDone || s.Status == StepWarning
}

// lateSpecialtyNote tells a specialty that runs after the implementation
// what it is looking at.
const lateSpecialtyNote = "\n\n## Implementação já feita\n\nEsta especialidade passou a ser exigida pelo diff depois da implementação. " +
	"Analise o código já alterado (lista acima) e diga o que ele precisa garantir; as verificações seguintes cobram cada requisito " +
	"e mandam corrigir o que faltar.\n"

// runLateSpecialties runs, before a gate, the planning specialties the diff
// made mandatory after their turn. It returns false when ctx was canceled or
// the run must stop.
func (o *Orchestrator) runLateSpecialties(ctx context.Context, ss *session, st *storyState) bool {
	for len(st.late) > 0 {
		id := st.late[0]
		st.late = st.late[1:]
		stage, ok := o.lookupStage(id)
		if !ok || o.stageRan(st, id) {
			continue
		}
		ref := stepRef{st.si, id}
		o.updateStep(ref, func(s *Step) {
			s.Status, s.Note = StepPending, "Exigida pelo diff depois da implementação; avalia o código já feito"
		})
		sc := o.stageContext(ss, st, stage)
		if !ss.dry {
			sc.Changed = o.changedFiles(ss, st.started)
		}
		resp, ok := o.runStage(ctx, ref, provider.Request{
			Role:     stage.Role,
			System:   stage.System,
			Prompt:   buildPrompt(stage, sc, ss.report, o.stepsBefore(ref)) + lateSpecialtyNote,
			Tier:     stage.Tier,
			WorkDir:  ss.repo,
			ReadOnly: stage.ReadOnly,
		})
		if !ok || !o.afterAgent(ctx, ss, st, ref) {
			return false
		}
		o.logf(LevelSuccess, "%s concluída via %s em %s.", stage.Title, resp.Name, resp.Duration.Round(time.Millisecond))
		o.applySpecialist(st, ref, stage, resp)
	}
	return true
}
