// Gates: reading a verdict, recovering its format and the fix rounds it
// asks for, until the gate approves or settles.

package pipeline

import (
	"cmp"
	"context"
	"fmt"
	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"slices"
	"strings"
)

// evaluation is a gate's outcome: the verdict, when one could be read, and
// the report forwarded to whoever fixes the issues.
type evaluation struct {
	verdict   Verdict
	parsed    bool
	simulated bool
	report    string
	// problem says why no valid verdict could be read.
	problem string
	// noEvidence marks an approval backed by nothing executed, such as a
	// verifier with no command to run: it is not evaluated, not passed.
	noEvidence bool
	// inconclusive marks an approval whose evidence does not cover what it
	// approves, such as an accepted scenario with no proven case.
	inconclusive bool
	// advice are the issues proportional moved out of the demands.
	advice []Issue
	// scenarios are the functional QA's answers as given and qaVerdict its
	// verdict before the evidence was applied; missing are the planned
	// scenarios it left out.
	scenarios []ScenarioResult
	qaVerdict Verdict
	missing   []AcceptanceScenario
}

// maxFormatRecoveries bounds how many times a gate is asked again, only for
// the verdict, when its answer carried none that honors the contract.
const maxFormatRecoveries = 2

// recheckFunc re-evaluates a gate in the verification step ref after fixes.
type recheckFunc func(ctx context.Context, ref stepRef, issues []Issue, fixes []Step, round int) (evaluation, bool)

// gateEvaluation reads a gate's answer; the functional QA's is also held to
// the executed evidence.
func (o *Orchestrator) gateEvaluation(ss *session, st *storyState, stage Stage, resp *provider.Response) evaluation {
	ev := o.llmEvaluation(resp, st.plan)
	switch {
	case stage.Acceptor:
		ev = o.judgeAcceptance(ss, st, ev)
	case stage.Measured:
		ev = o.judgePerformance(st, ev)
	}
	if ev.parsed {
		ev.verdict, ev.advice = proportional(ev.verdict, ss.policy)
	}
	return ev
}

func (o *Orchestrator) llmEvaluation(resp *provider.Response, plan Plan) evaluation {
	v, problem := readVerdict(resp.Output, o.stages, plan)
	return evaluation{verdict: v, parsed: problem == "", problem: problem, simulated: resp.Simulated, report: resp.Output}
}

// enforceGate reads a gate's verdict and, while it requests changes, sends the
// issues to the responsible implementers and re-evaluates the gate.
func (o *Orchestrator) enforceGate(ctx context.Context, ss *session, st *storyState, gate Stage, ev evaluation, recheck recheckFunc) bool {
	stepID := gate.ID
	rounds := maxFixRounds
	if ss.policy.MaxFixRounds > 0 {
		rounds = ss.policy.MaxFixRounds
	}
	if gate.Verifier {
		rounds = maxVerifierRounds
	}
	st.lastFailures, st.stalls = 0, 0
	for round := 1; ; round++ {
		base := stepID
		for try := 1; !ev.parsed && !ev.simulated && try <= maxFormatRecoveries; try++ {
			var alive bool
			if ev, stepID, alive = o.recoverVerdict(ctx, ss, st, gate, base, stepID, ev, try); !alive {
				return false
			}
		}
		if !ev.parsed {
			if ev.simulated {
				o.setValidation(st, gate, ValidationNotEvaluated)
				return true
			}
			return o.inconclusiveGate(st, stepID, gate, ev.problem)
		}
		for try := 1; gate.Acceptor && len(ev.missing) > 0 && !ev.simulated && try <= maxFormatRecoveries; try++ {
			var alive bool
			if ev, stepID, alive = o.recoverScenarios(ctx, ss, st, gate, base, stepID, ev, try); !alive {
				return false
			}
		}
		verdict := ev.verdict
		o.updateStep(stepRef{st.si, stepID}, func(s *Step) {
			s.Issues = verdict.Issues
			s.Advisory = ev.advice
			if verdict.NeedsChanges() {
				s.Note = fmt.Sprintf("%d problema(s) encontrado(s)", len(verdict.Issues))
			} else {
				s.Note = "Aprovado"
			}
		})
		if !verdict.NeedsChanges() {
			o.closeCauses(st, nil)
			switch {
			case ev.inconclusive:
				o.setValidation(st, gate, ValidationInconclusive)
				o.updateStep(stepRef{st.si, stepID}, func(s *Step) { s.Note = "Aprovado sem evidência para todos os cenários" })
			case ev.noEvidence:
				o.setValidation(st, gate, ValidationNotEvaluated)
			default:
				o.setValidation(st, gate, ValidationPassed)
			}
			if round > 1 {
				o.logf(LevelSuccess, "%s aprovou as correções.", gate.Title)
			}
			return true
		}

		fixable := o.triage(ss, st, verdict.Issues)
		settle := func(msg string) bool {
			o.closeCauses(st, verdict.Issues)
			return o.settleGate(st, stepID, gate, verdict.Issues, msg)
		}
		switch {
		case round > rounds:
			return settle(fmt.Sprintf("%d problema(s) persistem após %d rodadas de correção", len(verdict.Issues), rounds))
		case len(fixable) == 0:
			return settle(o.settleNote(st, verdict.Issues))
		case st.stalled(countCauses(verdict.Issues)):
			return settle(fmt.Sprintf("%d problema(s) sem redução há %d rodadas", len(verdict.Issues), maxStalledRounds))
		}
		fixable, afterID, alive := o.investigate(ctx, ss, st, gate, stepID, fixable, round)
		if !alive {
			return false
		}
		if len(fixable) == 0 {
			return settle(o.settleNote(st, verdict.Issues))
		}
		tier := o.repairTier(st, fixable)
		o.countAttempts(st, fixable)
		fixes := o.scheduleFixes(st.si, gate, afterID, fixable, round)
		if len(fixes) == 0 {
			return settle(fmt.Sprintf("%d problema(s) sem implementador para corrigi-los", len(verdict.Issues)))
		}
		verifyID := fmt.Sprintf("%s-verify-%d", gate.ID, round+1)
		o.insertSteps(st.si, fixes[len(fixes)-1].step.ID, Step{
			ID: verifyID, Title: fmt.Sprintf("%s — rodada %d", gate.Title, round+1), Role: gate.Role, Tier: gate.Tier,
			Status: StepPending, Kind: "verify", Round: round + 1,
		})

		fixesStarted := quality.FileClock(ss.repo)
		var done []Step
		for _, f := range fixes {
			ref := stepRef{st.si, f.step.ID}
			fixResp, ok := o.runStage(ctx, ref, provider.Request{
				Role:    f.stage.Role,
				System:  f.stage.System,
				Prompt:  buildFixPrompt(o.texts.fixTask, gate, st.sc, ss.report, st.planText, ev.report, repairBrief(f.step.Issues, o.causes(st.si)), f.step.Issues, round),
				Tier:    fixTier(f.stage, tier),
				WorkDir: ss.repo,
			})
			if !ok || !o.afterAgent(ctx, ss, st, ref) {
				return false
			}
			o.recordFix(st, f.step.Issues, fixResp.Output)
			f.step.Output = fixResp.Output
			done = append(done, f.step)
			o.logf(LevelSuccess, "%s concluída via %s.", f.step.Title, fixResp.Name)
		}

		if !ss.dry && len(o.changedFiles(ss, fixesStarted)) == 0 {
			o.updateStep(stepRef{st.si, verifyID}, func(s *Step) { s.Note = "Sem alterações desde a última verificação" })
			stepID = verifyID
			return settle("as correções não alteraram nenhum arquivo")
		}
		if ev, alive = recheck(ctx, stepRef{st.si, verifyID}, verdict.Issues, done, round+1); !alive {
			return false
		}
		stepID = verifyID
	}
}

// settleGate keeps the run going when a gate's issues outlast the fix rounds:
// the step is marked with the pending issues and the next stages and stories
// still run, so an unattended run never stops on one stubborn problem.
func (o *Orchestrator) settleGate(st *storyState, stepID string, gate Stage, issues []Issue, msg string) bool {
	o.updateStep(stepRef{st.si, stepID}, func(s *Step) {
		s.Status = StepWarning
		s.Error = msg + ". Pendências registradas; o pipeline segue."
		// A later round's step has no verdict of its own: it carries the
		// issues it settles, or the reports count none.
		if len(s.Issues) == 0 {
			s.Issues = slices.Clone(issues)
		}
	})
	o.setValidation(st, gate, ValidationFailed)
	class := FailureBehavior
	if len(issues) > 0 && !slices.ContainsFunc(issues, func(is Issue) bool { return !is.Infra }) {
		class = FailureInfra // every issue is a time-out or another machine problem
	}
	o.updateStep(stepRef{st.si, stepID}, func(s *Step) { s.FailureClass = class })
	st.pending += len(issues)
	o.logf(LevelWarn, "%s: %s. Seguindo com as pendências registradas.", gate.Title, msg)
	return true
}

// recoverVerdict asks a gate again, read-only, only for the verdict its last
// answer lacked. The request is a new step after stepID; base names it.
func (o *Orchestrator) recoverVerdict(ctx context.Context, ss *session, st *storyState, gate Stage, base, stepID string, ev evaluation, try int) (evaluation, string, bool) {
	o.logf(LevelWarn, "%s não retornou um veredito válido (%s); pedindo só o veredito (tentativa %d de %d).",
		gate.Title, ev.problem, try, maxFormatRecoveries)
	id := fmt.Sprintf("%s-formato-%d", base, try)
	o.insertSteps(st.si, stepID, Step{
		ID: id, Title: fmt.Sprintf("%s — formato do veredito (%d)", gate.Title, try), Role: gate.Role, Tier: gate.Tier,
		Status: StepPending, Kind: "verify",
	})
	ref := stepRef{st.si, id}
	resp, ok := o.runStage(ctx, ref, provider.Request{
		Role:     gate.Role,
		System:   gate.System,
		Prompt:   buildFormatPrompt(ev.problem, ev.report),
		Tier:     gate.Tier,
		WorkDir:  ss.repo,
		ReadOnly: true,
	})
	if !ok || !o.afterAgent(ctx, ss, st, ref) {
		return evaluation{}, id, false
	}
	next := o.llmEvaluation(resp, st.plan)
	// The recovered verdict is about the original review: keep its report
	// for whoever fixes the issues.
	if next.parsed {
		next.report = ev.report
	} else {
		next.report = cmp.Or(strings.TrimSpace(ev.report), resp.Output)
	}
	return next, id, true
}

// inconclusiveGate ends a gate that never produced a valid verdict: the run
// goes on, but the story cannot count as approved.
func (o *Orchestrator) inconclusiveGate(st *storyState, stepID string, gate Stage, problem string) bool {
	msg := fmt.Sprintf("Veredito inválido após %d tentativa(s) de recuperação: %s. Validação inconclusiva; o pipeline segue.", maxFormatRecoveries, problem)
	o.updateStep(stepRef{st.si, stepID}, func(s *Step) {
		s.Status = StepWarning
		s.Error = msg
		s.FailureClass = FailureFormat
	})
	o.setValidation(st, gate, ValidationInconclusive)
	st.inconclusive++
	o.logf(LevelWarn, "%s: %s", gate.Title, msg)
	return true
}

// setValidation records a gate's final outcome on the gate's own step, which
// a retry keeps, unlike the dynamic rounds that led to it.
func (o *Orchestrator) setValidation(st *storyState, gate Stage, v Validation) {
	o.updateStep(stepRef{st.si, gate.ID}, func(s *Step) { s.Validation = v })
}

// fixTier raises a fix to the heavy model for a cause's second attempt.
func fixTier(stage Stage, tier provider.Tier) provider.Tier {
	if tier == provider.TierHeavy {
		return tier
	}
	return stage.Tier
}

type scheduledFix struct {
	stage Stage
	step  Step
}

// scheduleFixes groups issues by implementer and inserts one pending fix
// step per implementer right after the gate step.
func (o *Orchestrator) scheduleFixes(si int, gate Stage, afterID string, issues []Issue, round int) []scheduledFix {
	policy := o.ws.Policy()
	var fixes []scheduledFix
	var names []string
	for _, impl := range o.stages {
		if !impl.Implementer || policy.StageDisabled(impl.ID) {
			continue
		}
		var mine []Issue
		for _, is := range issues {
			if is.Agent == impl.ID {
				mine = append(mine, is)
			}
		}
		if len(mine) == 0 {
			continue
		}
		step := Step{
			ID:     fmt.Sprintf("fix-%s-%d-%s", gate.ID, round, impl.ID),
			Title:  fmt.Sprintf("Correção: %s (%s, rodada %d)", impl.Title, gate.Title, round),
			Role:   impl.Role,
			Tier:   impl.Tier,
			Status: StepPending,
			Kind:   "fix",
			Round:  round,
			Gate:   gate.ID,
			Issues: mine,
		}
		o.insertSteps(si, afterID, step)
		afterID = step.ID
		fixes = append(fixes, scheduledFix{stage: impl, step: step})
		names = append(names, fmt.Sprintf("%s (%d)", impl.Title, len(mine)))
	}
	if len(fixes) > 0 {
		o.logf(LevelWarn, "%s solicitou %d correção(ões); acionando %s.", gate.Title, len(issues), strings.Join(names, ", "))
	}
	return fixes
}
