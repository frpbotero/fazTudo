package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/quality"
)

// maxVerifierRounds is how many fix rounds an executable gate may trigger;
// failing commands usually need more attempts than review remarks.
const maxVerifierRounds = 3

// maxIssueOutput caps the command output quoted in a fix request.
const maxIssueOutput = 3000

// runVerifier executes a deterministic gate and its fix rounds. A verifier
// whose repository did not change since the last approval is skipped.
func (o *Orchestrator) runVerifier(ctx context.Context, ss *session, st *storyState, stage Stage) bool {
	ref := stepRef{st.si, stage.ID}
	if !st.verifiedAt.IsZero() && len(o.changedFiles(ss, st.verifiedAt)) == 0 {
		o.updateStep(ref, func(s *Step) {
			s.Status = StepSkipped
			s.Note = "Sem alterações desde a última verificação aprovada"
		})
		return true
	}
	ev, ok := o.verify(ctx, ss, st, ref)
	if !ok {
		return false
	}
	recheck := func(ctx context.Context, ref stepRef, _ []Issue, _ []Step, _ int) (evaluation, bool) {
		return o.verify(ctx, ss, st, ref)
	}
	if !o.enforceGate(ctx, ss, st, stage, ev, recheck) {
		return false
	}
	st.verifiedAt = time.Now()
	return true
}

// verify runs the project commands, the static checks on files changed by
// this story and the test traceability, turning every failure into an issue
// for the implementer most likely responsible.
func (o *Orchestrator) verify(ctx context.Context, ss *session, st *storyState, ref stepRef) (evaluation, bool) {
	started := time.Now()
	o.updateStep(ref, func(s *Step) { s.Status = StepRunning; s.StartedAt = &started })

	if ss.dry {
		o.updateStep(ref, func(s *Step) {
			s.Status = StepDone
			s.Note = fmt.Sprintf("Simulação: %d comando(s) e checagens estáticas não executados", len(ss.checks))
			s.Output = describeChecks(ss.checks)
			s.DurationMS = time.Since(started).Milliseconds()
		})
		return evaluation{simulated: true}, true
	}

	var results []quality.Result
	var issues []Issue
	timeout := time.Duration(ss.policy.CheckTimeoutS) * time.Second
	for _, c := range ss.checks {
		o.logf(LevelInfo, "Executando %s: %s", c.Name, c.Run)
		r := o.runCheck(ctx, ss.repo, c, timeout)
		if ctx.Err() != nil {
			o.cancelStep(ref, time.Since(started))
			return evaluation{}, false
		}
		results = append(results, r)
		snapshot := slices.Clone(results)
		o.updateStep(ref, func(s *Step) { s.Checks = snapshot })
		if r.Passed {
			o.logf(LevelSuccess, "%s passou em %s.", c.Name, time.Duration(r.DurationMS)*time.Millisecond)
			continue
		}
		o.logf(LevelWarn, "%s falhou (%s).", c.Name, exitLabel(r))
		issues = append(issues, Issue{
			Agent:       o.assignCommand(c, st.plan),
			Severity:    "alta",
			Description: commandIssue(c, r),
			Location:    c.Name,
		})
	}

	changed := o.changedFiles(ss, st.started)
	var findings []quality.Finding
	findings = append(findings, quality.Syntax(ctx, ss.repo, changed)...)
	findings = append(findings, quality.Format(ctx, ss.repo, changed)...)
	findings = append(findings, quality.Readability(ss.repo, changed, ss.policy.MaxLineLength)...)
	for _, f := range findings {
		severity := "média"
		if f.Check == "syntax" {
			severity = "alta"
		}
		issues = append(issues, Issue{
			Agent:       o.assignFile(f.File, st.plan),
			Severity:    severity,
			Description: findingLabel(f.Check) + ": " + f.Message,
			Location:    findingLocation(f),
		})
	}

	traces := quality.TraceTests(ss.repo, st.reqs)
	issues = append(issues, o.traceIssues(traces, st.plan)...)
	if len(traces) > 0 {
		o.updateStory(st.si, func(r *StoryRun) { r.Trace = traces })
	}

	verdict := Verdict{Status: VerdictApproved}
	if len(issues) > 0 {
		verdict = Verdict{Status: VerdictChangesRequested, Issues: issues}
	}
	report := verifierReport(results, findings, traces)
	elapsed := time.Since(started)
	o.updateStep(ref, func(s *Step) {
		s.Status = StepDone
		s.Checks = results
		s.Findings = findings
		s.Output = report
		s.DurationMS = elapsed.Milliseconds()
	})
	return evaluation{verdict: verdict, parsed: true, report: report}, true
}

// changedFiles lists files modified since t, excluding protected inputs.
func (o *Orchestrator) changedFiles(ss *session, t time.Time) []string {
	protected := ss.guard.Files()
	return slices.DeleteFunc(quality.ChangedSince(ss.repo, t), func(f string) bool {
		return slices.Contains(protected, f)
	})
}

var frontendExts = map[string]bool{
	".html": true, ".htm": true, ".css": true, ".scss": true, ".less": true,
	".vue": true, ".svelte": true, ".jsx": true, ".tsx": true,
}

// prefer returns id when the plan selected that implementer, otherwise the
// plan's default implementer.
func (o *Orchestrator) prefer(id string, plan Plan) string {
	if plan.Includes(id) && isImplementer(o.stages, id) {
		return id
	}
	return defaultImplementer(o.stages, plan)
}

func (o *Orchestrator) assignFile(file string, plan Plan) string {
	lower := strings.ToLower(file)
	if frontendExts[filepath.Ext(lower)] || containsAny(lower, "frontend/", "web/", "ui/", "client/", "e2e") {
		return o.prefer("frontend", plan)
	}
	return o.prefer("backend", plan)
}

func (o *Orchestrator) assignCommand(c quality.Command, plan Plan) string {
	if containsAny(strings.ToLower(c.Name+" "+c.Run), "e2e", "playwright", "cypress", "frontend", "web") {
		return o.prefer("frontend", plan)
	}
	return defaultImplementer(o.stages, plan)
}

func (o *Orchestrator) assignKind(kind string, plan Plan) string {
	switch kind {
	case "e2e", "integration":
		return o.prefer("frontend", plan)
	case "unit":
		return o.prefer("backend", plan)
	}
	return defaultImplementer(o.stages, plan)
}

// traceIssues turns required tests missing from the repository into one
// issue per responsible implementer.
func (o *Orchestrator) traceIssues(traces []quality.Trace, plan Plan) []Issue {
	missing := map[string][]string{}
	var order []string
	for _, t := range traces {
		if t.Found() {
			continue
		}
		agent := o.assignKind(t.Kind, plan)
		if _, seen := missing[agent]; !seen {
			order = append(order, agent)
		}
		label := t.ID
		if t.Description != "" {
			label += " (" + clipRunes(t.Description, 90) + ")"
		}
		missing[agent] = append(missing[agent], label)
	}
	var issues []Issue
	for _, agent := range order {
		ids := missing[agent]
		issues = append(issues, Issue{
			Agent:    agent,
			Severity: "alta",
			Description: fmt.Sprintf("%d caso(s) de teste da especificação sem teste correspondente no repositório. "+
				"Implemente-os citando o ID no nome do teste: %s", len(ids), strings.Join(ids, "; ")),
			Location: "rastreabilidade",
		})
	}
	return issues
}

func commandIssue(c quality.Command, r quality.Result) string {
	return fmt.Sprintf("O comando `%s` (%s) %s. Corrija o código até ele passar; ajuste um teste apenas se ele contrariar a especificação, "+
		"explicando o motivo. Final da saída:\n```\n%s\n```", c.Run, c.Name, exitLabel(r), tail(r.Output, maxIssueOutput))
}

func exitLabel(r quality.Result) string {
	if r.TimedOut {
		return "excedeu o tempo limite"
	}
	return fmt.Sprintf("falhou com código %d", r.ExitCode)
}

func findingLabel(check string) string {
	switch check {
	case "syntax":
		return "Erro de sintaxe"
	case "format":
		return "Formatação"
	}
	return "Legibilidade"
}

func findingLocation(f quality.Finding) string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}

// verifierReport is the Markdown summary shown in the step and sent to the
// implementers in fix rounds.
func verifierReport(results []quality.Result, findings []quality.Finding, traces []quality.Trace) string {
	var b strings.Builder
	b.WriteString("## Comandos do projeto\n\n")
	if len(results) == 0 {
		b.WriteString("Nenhum comando configurado.\n")
	}
	for _, r := range results {
		status := "ok"
		if !r.Passed {
			status = exitLabel(r)
		}
		fmt.Fprintf(&b, "- [%s] %s: `%s` (%s)\n", status, r.Name, r.Command, time.Duration(r.DurationMS)*time.Millisecond)
		if !r.Passed && r.Output != "" {
			fmt.Fprintf(&b, "\n```\n%s\n```\n\n", tail(r.Output, maxIssueOutput))
		}
	}

	b.WriteString("\n## Checagens estáticas dos arquivos alterados\n\n")
	if len(findings) == 0 {
		b.WriteString("Sem problemas de sintaxe, formatação ou legibilidade.\n")
	}
	for _, f := range findings {
		fmt.Fprintf(&b, "- %s — %s: %s\n", findingLocation(f), findingLabel(f.Check), f.Message)
	}

	if len(traces) > 0 {
		found := 0
		var missing []string
		for _, t := range traces {
			if t.Found() {
				found++
			} else {
				missing = append(missing, t.ID)
			}
		}
		fmt.Fprintf(&b, "\n## Rastreabilidade dos testes\n\n%d de %d casos de teste encontrados nos arquivos de teste.\n", found, len(traces))
		if len(missing) > 0 {
			fmt.Fprintf(&b, "Sem teste: %s\n", strings.Join(missing, ", "))
		}
	}
	return b.String()
}

func describeChecks(checks []quality.Command) string {
	if len(checks) == 0 {
		return "Nenhum comando de verificação configurado."
	}
	var b strings.Builder
	b.WriteString("Comandos que seriam executados:\n")
	for _, c := range checks {
		fmt.Fprintf(&b, "- %s: %s\n", c.Name, c.Run)
	}
	return b.String()
}

func containsAny(s string, subs ...string) bool {
	return slices.ContainsFunc(subs, func(sub string) bool { return strings.Contains(s, sub) })
}

// tail keeps the last n bytes of s, where test runners print the failures.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut++
	}
	return "[…]\n" + s[cut:]
}

func clipRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
