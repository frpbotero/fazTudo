package pipeline

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"llm-orchestrator/internal/provider"
)

const openDecision = "Relatório.\n```json\n{\"decision\": \"block\", \"reason\": \"regra de frete indefinida\", " +
	"\"criteria\": [{\"criterion\": \"calcular frete\", \"status\": \"ambiguo\", \"evidence\": \"a US não diz a regra\"}], " +
	"\"pending_decisions\": [{\"question\": \"Frete grátis acima de qual valor?\", \"options\": [\"R$ 100\", \"R$ 200\"], \"impact\": \"regra do carrinho\"}]}\n```"

// newDecisionTest runs assessStages in simulation over threeStories; scout
// answers per story through scout(marker, call).
func newDecisionTest(t *testing.T, scout func(marker string, prompt string) string) (*Orchestrator, *fakeExecutor) {
	t.Helper()
	exec := &fakeExecutor{}
	exec.answer = func(req provider.Request) string {
		switch req.Role {
		case AnalysisStage.Role:
			return independentPair
		case "scout":
			return scout(storyMarker(req.Prompt), req.Prompt)
		}
		return ""
	}
	o, _ := newVerifierTest(t, exec, passing)
	o.baseStages = assessStages
	o.stages = o.baseStages
	return o, exec
}

func dry(opts StartOptions) StartOptions { opts.DryRun = true; return opts }

func TestOpenProductDecisionPausesOnlyTheStoryAndItsDependents(t *testing.T) {
	o, _ := newDecisionTest(t, func(m, _ string) string {
		if m == "um" {
			return openDecision
		}
		return decision(DecisionImplement)
	})
	_ = o.Start(dry(StartOptions{Stories: threeStories, Source: "Markdown"}))
	run := waitDone(t, o)

	one, two, three := run.Stories[0], run.Stories[1], run.Stories[2]
	if one.Status != StepWaiting || one.Readiness == nil || len(one.Readiness.Decisions) != 1 || one.Readiness.Criteria[0].Status != CriterionAmbiguous {
		t.Fatalf("US-1 = %s %+v", one.Status, one.Readiness)
	}
	if two.Status != StepDone {
		t.Errorf("US-2 is independent and goes on: %s", two.Status)
	}
	if three.Status != StepSkipped || !strings.Contains(three.IntegrationNote, "aguarda decisão") {
		t.Errorf("US-3 depends on US-1: %s %q", three.Status, three.IntegrationNote)
	}
	if run.Result != ResultSucceeded || !strings.Contains(run.Logs[len(run.Logs)-1].Message, "Aguardando decisão de produto em: US-1") {
		t.Errorf("result %s, last log %q", run.Result, run.Logs[len(run.Logs)-1].Message)
	}
}

func TestAnsweringResumesTheStoryFromPassoZeroWithTheAnswer(t *testing.T) {
	var asked atomic.Int32
	var lastPrompt atomic.Value
	o, exec := newDecisionTest(t, func(m, prompt string) string {
		if m != "um" {
			return decision(DecisionImplement)
		}
		lastPrompt.Store(prompt)
		if asked.Add(1) == 1 {
			return openDecision
		}
		// Asked again, the agent repeats the answered question: it is not reopened.
		return strings.Replace(openDecision, `"block"`, `"implement"`, 1)
	})
	_ = o.Start(dry(StartOptions{Stories: threeStories, Source: "Markdown"}))
	waitDone(t, o)
	feBefore := exec.count("fe")

	if err := o.AnswerDecisions(0, []string{"  "}); err == nil {
		t.Error("a blank answer must be rejected")
	}
	if err := o.AnswerDecisions(1, []string{"x"}); !errors.Is(err, ErrNotWaiting) {
		t.Errorf("US-2 waits on nothing: %v", err)
	}
	if err := o.AnswerDecisions(0, []string{"R$ 200"}); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)

	prompt, _ := lastPrompt.Load().(string)
	if !strings.Contains(prompt, "Decisões de produto respondidas") || !strings.Contains(prompt, "Frete grátis acima de qual valor? → R$ 200") {
		t.Errorf("the answer must reach the agents:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<<um>>") {
		t.Error("the story text is kept as written")
	}
	one, three := run.Stories[0], run.Stories[2]
	if one.Status != StepDone || len(one.Answers) != 1 || one.Answers[0].AnsweredAt == nil || len(one.Readiness.Decisions) != 0 {
		t.Errorf("US-1 = %s, answers %+v, readiness %+v", one.Status, one.Answers, one.Readiness)
	}
	if three.Status != StepDone {
		t.Errorf("US-3 runs once its dependency is answered: %s", three.Status)
	}
	if got := exec.count("fe") - feBefore; got != 2 {
		t.Errorf("only US-1 and US-3 are developed again, fe calls = %d", got)
	}
	// The resumed run answered decisions; no stage had failed.
	if !hasLog(run, "Pipeline concluído após retomar a US com as decisões de produto respondidas") || hasLog(run, "refazer a etapa com falha") {
		t.Errorf("final message misnames the resume: %+v", run.Logs[len(run.Logs)-1])
	}
}

func TestReadinessNormalizesTheAgentsAnswer(t *testing.T) {
	a := Assessment{
		Decision: DecisionReuse,
		Criteria: []Criterion{{Text: "a", Status: "Existente", Evidence: "x.go:1"}, {Text: "b", Status: "talvez"}, {Text: " "}},
		Decisions: []PendingDecision{
			{Question: "Qual moeda?", Answer: "inventada pelo agente"},
			{Question: "frete  grátis acima de qual valor"},
		},
	}
	r := readiness(a, []PendingDecision{{Question: "Frete grátis acima de qual valor?", Answer: "R$ 200"}})
	if len(r.Criteria) != 2 || r.Criteria[1].Status != CriterionAmbiguous {
		t.Errorf("criteria = %+v", r.Criteria)
	}
	if r.Decision != DecisionExtend {
		t.Error("a reuse with an ambiguous criterion is not a reuse")
	}
	if len(r.Decisions) != 1 || r.Decisions[0].Question != "Qual moeda?" || r.Decisions[0].Answer != "" {
		t.Errorf("decisions = %+v", r.Decisions)
	}
}

func TestAnsweringDropsTheEmptyBranchOfTheWaitingStory(t *testing.T) {
	var asked atomic.Int32
	exec := &fakeExecutor{}
	exec.answer = func(req provider.Request) string {
		if req.Role == "scout" {
			if asked.Add(1) == 1 {
				return openDecision
			}
			return decision(DecisionImplement)
		}
		return ""
	}
	o, repo := newParallelStoryTest(t, exec)
	o.baseStages = assessStages
	o.stages = o.baseStages
	exec.onCall = func(provider.Request) {} // Passo 0 reads; it writes nothing
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	first := waitDone(t, o).Stories[0]
	if first.Status != StepWaiting || first.Branch == "" {
		t.Fatalf("US-1 = %s on %q", first.Status, first.Branch)
	}
	if err := o.AnswerDecisions(0, []string{"R$ 200"}); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	again := run.Stories[0]
	if again.Branch == "" || again.Status != StepDone {
		t.Fatalf("the story must start over on a branch: %s on %q", again.Status, again.Branch)
	}
	// Only the branch of the new attempt is left (same name when it all runs
	// within one second: the old one was removed first).
	if got := strings.Fields(strings.ReplaceAll(gitOut(t, repo, "branch", "--list", "faztudo/*"), "*", "")); len(got) != 1 || got[0] != again.Branch {
		t.Errorf("faztudo branches = %v, want only %s", got, again.Branch)
	}
	if !hasLog(run, "Branch "+first.Branch+" removida") {
		t.Errorf("logs = %+v", run.Logs)
	}
}
