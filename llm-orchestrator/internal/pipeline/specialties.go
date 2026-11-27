package pipeline

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

// SpecialistReport is the contract every planning specialty answers with:
// what it looked at, what the implementation must meet and how each item is
// verified, and what it left out.
type SpecialistReport struct {
	Scope        string                  `json:"scope"`
	Requirements []SpecialistRequirement `json:"requirements"`
	Risks        []string                `json:"risks,omitempty"`
	OutOfScope   string                  `json:"out_of_scope,omitempty"`
	// NoneReason says why a specialty found nothing to require.
	NoneReason string `json:"none_reason,omitempty"`
	// Problems are what the orchestrator found wrong with the answer.
	Problems []string `json:"problems,omitempty"`
}

// SpecialistRequirement is one thing a specialty requires, with how it is
// proven: a test the implementers write or an inspection QA performs.
type SpecialistRequirement struct {
	ID           string `json:"id"`
	Text         string `json:"text"`
	Verification string `json:"verification"`
}

const specialistInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"scope\": \"o que você analisou\", \"requirements\": [{\"id\": \"TM-01\", \"text\": \"o que a implementação precisa garantir\", " +
	"\"verification\": \"teste (ID e tipo) ou inspeção que comprova\"}], \"risks\": [\"risco que fica\"], " +
	"\"out_of_scope\": \"o que não analisou\", \"none_reason\": \"só se não há requisito: por quê\"}\n```\n" +
	"Cada requisito com verificação. Sem requisito, none_reason obrigatório. Não implemente nada."

// parseSpecialist reads and checks a specialty's answer. A report with a
// requirement lacking its verification, or with nothing required and no
// reason, does not honor the contract.
func parseSpecialist(output string) SpecialistReport {
	r, ok := lastJSON(output, func(r SpecialistReport) bool { return r.Scope != "" || len(r.Requirements) > 0 || r.NoneReason != "" })
	if !ok {
		return SpecialistReport{Problems: []string{"resposta sem o bloco JSON do contrato"}}
	}
	r.Problems = nil
	var reqs []SpecialistRequirement
	for i, q := range r.Requirements {
		q.ID, q.Text, q.Verification = strings.TrimSpace(q.ID), strings.TrimSpace(q.Text), strings.TrimSpace(q.Verification)
		if q.Text == "" {
			continue
		}
		if q.ID == "" {
			q.ID = fmt.Sprintf("REQ-%02d", i+1)
		}
		if q.Verification == "" {
			r.Problems = append(r.Problems, q.ID+" sem verificação")
		}
		reqs = append(reqs, q)
	}
	r.Requirements = reqs
	if strings.TrimSpace(r.Scope) == "" {
		r.Problems = append(r.Problems, "escopo ausente")
	}
	if len(reqs) == 0 && strings.TrimSpace(r.NoneReason) == "" {
		r.Problems = append(r.Problems, "nenhum requisito e nenhuma justificativa")
	}
	return r
}

// applySpecialist records a specialty's report; one that breaks the contract
// leaves the step without a valid result.
func (o *Orchestrator) applySpecialist(st *storyState, ref stepRef, stage Stage, resp *provider.Response) {
	if resp.Simulated {
		return
	}
	r := parseSpecialist(resp.Output)
	o.updateStory(st.si, func(sr *StoryRun) {
		if sr.Specialties == nil {
			sr.Specialties = map[string]SpecialistReport{}
		}
		sr.Specialties[stage.ID] = r
	})
	if len(r.Problems) > 0 {
		problems := strings.Join(r.Problems, "; ")
		o.updateStep(ref, func(s *Step) {
			s.Status, s.Validation = StepWarning, ValidationInconclusive
			s.Error = "Contrato de saída não atendido: " + problems
		})
		o.logf(LevelWarn, "%s de %s não atendeu o contrato (%s); validação inconclusiva.", stage.Title, st.sc.Story.Title, problems)
		return
	}
	o.updateStep(ref, func(s *Step) {
		s.Validation = ValidationPassed
		s.Note = fmt.Sprintf("%d requisito(s) para a implementação", len(r.Requirements))
	})
}

// specialtyRequirements lists what the specialties required, for QA.
func specialtyRequirements(run StoryRun) string {
	var b strings.Builder
	for _, id := range sortedSpecialties(run.Specialties) {
		for _, q := range run.Specialties[id].Requirements {
			fmt.Fprintf(&b, "- %s (%s): %s — verificação: %s\n", q.ID, id, q.Text, q.Verification)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "Requisitos das especialidades (julgue cada um como cenário):\n" + b.String() + "\n"
}

func sortedSpecialties(m map[string]SpecialistReport) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// enforceSpecialties brings in the specialties the plan itself calls for:
// a migration needs its data plan and its check; a performance requirement
// with a target needs its specialist.
func (o *Orchestrator) enforceSpecialties(st *storyState) {
	var need []string
	if st.plan.Impact != nil && len(st.plan.Impact.Migrations) > 0 {
		need = append(need, "data", "migrations")
	}
	if slices.ContainsFunc(o.ws.Policy().Benchmarks, func(b workspace.Benchmark) bool { return b.Required }) {
		need = append(need, "performance")
	}
	for _, n := range st.plan.NonFunctional {
		if strings.TrimSpace(n.SLO) != "" && containsAny(strings.ToLower(n.Kind+" "+n.Requirement), "desempenho", "performance", "latência", "latencia", "throughput") {
			need = append(need, "performance")
			break
		}
	}
	added := false
	for _, id := range need {
		if s, ok := o.lookupStage(id); ok && s.Selectable && !st.plan.Includes(id) {
			st.plan.Agents = append(st.plan.Agents, id)
			o.updateStep(stepRef{st.si, id}, func(step *Step) {
				if step.Status == StepOmitted {
					step.Status, step.Note = StepPending, "Exigida pelo plano (impacto ou requisito não funcional)"
				}
			})
			added = true
		}
	}
	if added {
		plan := st.plan
		o.updateStory(st.si, func(r *StoryRun) { r.Plan = &plan })
	}
}

// runCommandGate runs a list of policy commands as an executable gate, with
// fix rounds like the project checks. Without commands there is nothing to
// prove: the step says what to configure and stays inconclusive.
func (o *Orchestrator) runCommandGate(ctx context.Context, ss *session, st *storyState, stage Stage, commands []quality.Command, missing string) bool {
	ref := stepRef{st.si, stage.ID}
	if ss.dry {
		o.updateStep(ref, func(s *Step) {
			s.Status, s.Note = StepDone, fmt.Sprintf("Simulação: %d comando(s) não executados", len(commands))
			s.Validation = ValidationNotEvaluated
		})
		return true
	}
	if len(commands) == 0 {
		o.updateStep(ref, func(s *Step) {
			s.Status, s.Validation, s.Note = StepWarning, ValidationInconclusive, missing
		})
		o.logf(LevelWarn, "%s de %s: %s", stage.Title, st.sc.Story.Title, missing)
		return true
	}
	run := func(ref stepRef) (evaluation, bool) {
		started := time.Now()
		o.updateStep(ref, func(s *Step) { s.Status = StepRunning; s.StartedAt = &started })
		a, ok := o.assessChecks(ctx, ss, st, ss.repo, nil, commands)
		if !ok {
			o.cancelStep(ref, time.Since(started))
			return evaluation{}, false
		}
		// Only the commands judge here: static checks belong to the project gate.
		a.issues = slices.DeleteFunc(a.issues, func(is Issue) bool { return is.Location == "rastreabilidade" })
		a.findings, a.traces = nil, nil
		for i := range a.issues {
			if stage.ID == "migrations" {
				a.issues[i].Agent = o.prefer("backend", st.plan)
			}
		}
		verdict := Verdict{Status: VerdictApproved}
		if len(a.issues) > 0 {
			verdict = Verdict{Status: VerdictChangesRequested, Issues: a.issues}
		}
		o.updateStep(ref, func(s *Step) {
			s.Status = StepDone
			s.Checks = quality.WithoutCases(a.results)
			s.Output = verifierReport(a)
			s.DurationMS = time.Since(started).Milliseconds()
		})
		return evaluation{verdict: verdict, parsed: true}, true
	}
	ev, ok := run(ref)
	if !ok {
		return false
	}
	recheck := func(_ context.Context, ref stepRef, _ []Issue, _ []Step, _ int) (evaluation, bool) { return run(ref) }
	return o.enforceGate(ctx, ss, st, stage, ev, recheck)
}

// measure runs the benchmark commands for the performance specialist and
// returns their outcome as evidence; false when ctx was canceled. Without
// a measurement the specialist has nothing to judge from.
func (o *Orchestrator) measure(ctx context.Context, ss *session, st *storyState) (string, bool) {
	var b strings.Builder
	results, alive := o.runBenchmarks(ctx, ss, st)
	if !alive {
		return "", false
	}
	st.benchResults = results
	o.updateStory(st.si, func(r *StoryRun) { r.Performance = slices.Clone(results) })
	b.WriteString(benchmarkEvidence(results))
	compared := slices.ContainsFunc(results, func(r BenchResult) bool { return r.Verdict != benchVerdictUnclear })
	b.WriteString("## Medições executadas\n\n")
	if len(ss.policy.BenchmarkChecks) == 0 {
		if !compared {
			b.WriteString("Nenhuma medição comparável (`benchmarks` ou `benchmark_checks`): sem medição, não aprove.\n\n")
		}
		st.measured = compared
		return b.String(), true
	}
	timeout := time.Duration(ss.policy.CheckTimeoutS) * time.Second
	measured := false
	for _, c := range ss.policy.BenchmarkChecks {
		o.logf(LevelInfo, "Medindo %s: %s", c.Name, c.Run)
		r := o.runGated(ctx, ss, ss.repo, c, timeout)
		if ctx.Err() != nil {
			return "", false
		}
		status := "ok"
		if !r.Passed {
			status = exitLabel(r)
		} else {
			measured = true
		}
		fmt.Fprintf(&b, "### %s (`%s`): %s em %s\n\n```\n%s\n```\n\n", c.Name, c.Run, status, time.Duration(r.DurationMS)*time.Millisecond, tail(r.Output, maxIssueOutput))
	}
	for _, n := range st.plan.NonFunctional {
		fmt.Fprintf(&b, "- Requisito %s: %s (SLO: %s)\n", n.Kind, n.Requirement, cmpOrString(n.SLO, "não definido"))
	}
	st.measured = measured || compared
	return b.String(), true
}

const performanceInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"status\": \"approved\" | \"changes_requested\", \"issues\": [{\"agent\": \"frontend\" | \"backend\", \"severity\": \"alta\" | \"média\" | \"baixa\", " +
	"\"description\": \"gargalo, medição que o mostra e correção\", \"location\": \"arquivo:linha\"}], " +
	"\"measurements\": [{\"metric\": \"p95 do endpoint\", \"value\": \"valor medido\", \"slo\": \"meta\", \"met\": true}]}\n```\n" +
	"Só a partir das medições acima; hipótese sem medição vai fora de issues. Meta não atingida exige changes_requested."

// judgePerformance holds the specialist to the measurements: no measurement
// means no approval, and a target reported as missed cannot be approved.
func (o *Orchestrator) judgePerformance(st *storyState, ev evaluation) evaluation {
	if !ev.parsed || ev.simulated {
		return ev
	}
	answer, _ := lastJSON(ev.report, func(a struct {
		Measurements []Measurement `json:"measurements"`
	}) bool {
		return true
	})
	o.updateStory(st.si, func(r *StoryRun) { r.Measurements = slices.Clone(answer.Measurements) })
	issues, unclear := o.benchmarkIssues(st, st.benchResults)
	if len(issues) > 0 {
		ev.verdict.Status = VerdictChangesRequested
		ev.verdict.Issues = append(ev.verdict.Issues, issues...)
	}
	if ev.verdict.NeedsChanges() {
		return ev
	}
	if unclear {
		ev.inconclusive = true
	}
	for _, m := range answer.Measurements {
		if !m.Met {
			ev.verdict.Status = VerdictChangesRequested
			ev.verdict.Issues = append(ev.verdict.Issues, Issue{
				Agent:        defaultImplementer(o.stages, st.plan),
				Severity:     "alta",
				Description:  fmt.Sprintf("Meta de desempenho não atingida: %s = %s (meta %s).", m.Metric, m.Value, m.SLO),
				Location:     "desempenho",
				Fingerprints: []string{quality.TextFingerprint("desempenho", m.Metric)},
			})
		}
	}
	if !ev.verdict.NeedsChanges() && (!st.measured || (len(answer.Measurements) == 0 && len(st.benchResults) == 0)) {
		ev.inconclusive = true
	}
	return ev
}

// Measurement is one metric the performance specialist read from a
// benchmark, against its target.
type Measurement struct {
	Metric string `json:"metric"`
	Value  string `json:"value"`
	SLO    string `json:"slo,omitempty"`
	Met    bool   `json:"met"`
}
