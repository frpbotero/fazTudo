package pipeline

import (
	"strings"
	"testing"

	"llm-orchestrator/internal/workspace"
)

func TestEachStepSaysWhyItIsPartOfTheStory(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"architect": {fence(frontOnly)}}}
	run := runRiskFlow(t, exec, "Tela de login com senha", func(p *workspace.Policy) { p.Flow = workspace.FlowFast })
	steps := run.Stories[0].Steps
	cases := map[string]struct{ need, reason string }{
		"arch":     {NeedMandatory, "faz parte do fluxo"},
		"frontend": {NeedOptional, "selecionada pelo arquiteto"},
		"backend":  {NeedNotApplicable, "não a selecionou"},
		"security": {NeedRisk, "Autenticação"},
		"review":   {NeedRisk, "regras de risco"},
	}
	for id, want := range cases {
		s := step(steps, id)
		if s.Need != want.need || !strings.Contains(s.NeedReason, want.reason) {
			t.Errorf("%s = %q (%q), want %q with %q", id, s.Need, s.NeedReason, want.need, want.reason)
		}
	}
}

func TestStepDisabledByTheFlowSaysSo(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"architect": {fence(frontOnly)}}}
	run := runRiskFlow(t, exec, "Mudar a cor do botão", func(p *workspace.Policy) { p.Flow = workspace.FlowFast })
	if s := step(run.Stories[0].Steps, "review"); s.Need != NeedNotApplicable || !strings.Contains(s.NeedReason, "fluxo fast") {
		t.Errorf("review = %q (%q)", s.Need, s.NeedReason)
	}
}
