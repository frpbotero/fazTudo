package pipeline

import (
	"strings"

	"llm-orchestrator/internal/workspace"
)

// Why a stage is part of a story, as each step says it.
const (
	NeedMandatory     = "obrigatória"        // part of the flow, for every story
	NeedRisk          = "exigida pelo risco" // the risk policy made it mandatory
	NeedOptional      = "opcional"           // the plan chose it
	NeedNotApplicable = "não aplicável"      // left out, with the reason
)

// stageNeed tells why stage is, or is not, part of the story.
func (o *Orchestrator) stageNeed(stage Stage, st *storyState, policy workspace.Policy) (string, string) {
	switch {
	case st.requiredByRisk(stage.ID):
		return NeedRisk, "regras de risco: " + strings.Join(st.risk.Rules, ", ")
	case st.reused && !stage.Verifier:
		return NeedNotApplicable, "o Passo 0 achou a US já atendida; só as verificações executáveis rodam"
	case policy.StageDisabled(stage.ID):
		return NeedNotApplicable, "desligada pelo fluxo " + policy.Flow
	case stage.Selectable && st.plan.Includes(stage.ID):
		if stage.Conditional {
			return NeedOptional, "acionada pelo plano"
		}
		return NeedOptional, "selecionada pelo arquiteto"
	case stage.Selectable && stage.Conditional:
		return NeedNotApplicable, "nem o plano nem o risco pediram esta especialidade"
	case stage.Selectable:
		return NeedNotApplicable, "o arquiteto não a selecionou"
	}
	return NeedMandatory, "faz parte do fluxo " + policy.Flow
}

// annotateNeeds writes on each stage's step why it runs or not; it follows
// the plan and the risk, so it is refreshed as they change.
func (o *Orchestrator) annotateNeeds(ss *session, st *storyState) {
	for _, stage := range o.stages {
		need, why := o.stageNeed(stage, st, ss.policy)
		o.updateStep(stepRef{st.si, stage.ID}, func(s *Step) { s.Need, s.NeedReason = need, why })
	}
}
