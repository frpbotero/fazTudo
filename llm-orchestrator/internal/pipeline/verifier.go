package pipeline

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

// maxVerifierRounds is how many fix rounds an executable gate may trigger;
// failing commands usually need more attempts than review remarks.
const maxVerifierRounds = 3

// maxIssueOutput caps the command output quoted in a fix request, after it is
// reduced to the failures (maxIssueLines).
const (
	maxIssueOutput = 3000
	maxIssueLines  = 50
)

// maxTimeoutOutput is how much of a command cut by its time limit the report
// quotes: enough to show which phase it was in.
const maxTimeoutOutput = 600

// sweepTimeout bounds one cleanup of disposable stacks.
const sweepTimeout = 90 * time.Second

// runVerifier executes a deterministic gate and its fix rounds. A verifier
// whose repository did not change since the last approval is skipped.
func (o *Orchestrator) runVerifier(ctx context.Context, ss *session, st *storyState, stage Stage) bool {
	ref := stepRef{st.si, stage.ID}
	if !st.verifiedAt.IsZero() && len(o.changedFiles(ss, st.verifiedAt)) == 0 {
		o.updateStep(ref, func(s *Step) {
			s.Status = StepSkipped
			s.Note = "Sem alterações desde a última verificação aprovada"
			s.Validation = ValidationPassed // the approval holds for unchanged code
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
	st.verifiedAt = quality.FileClock(ss.repo)
	return true
}

// checkFixes runs the project checks after a reviewer's fix round, before the
// reviewer is called again: a fix that breaks the build goes straight back to
// the implementers without spending a review. It returns the failing
// evaluation, or nil when the checks pass (or there is nothing new to check);
// false means ctx was canceled.
func (o *Orchestrator) checkFixes(ctx context.Context, ss *session, st *storyState, ref stepRef) (*evaluation, bool) {
	if ss.dry || (!st.verifiedAt.IsZero() && len(o.changedFiles(ss, st.verifiedAt)) == 0) {
		return nil, true
	}
	started := time.Now()
	o.updateStep(ref, func(s *Step) { s.Status = StepRunning; s.StartedAt = &started })
	a, ok := o.reassess(ctx, ss, st)
	if !ok {
		o.cancelStep(ref, time.Since(started))
		return nil, false
	}
	if a.passed() {
		st.verifiedAt = quality.FileClock(ss.repo)
		if len(a.traces) > 0 {
			// The gate after this fix judges with the cases that just ran.
			o.updateStory(st.si, func(r *StoryRun) { r.Trace = a.traces })
		}
		return nil, true
	}
	report := verifierReport(a)
	o.updateStep(ref, func(s *Step) {
		s.Status = StepDone
		s.Checks = quality.WithoutCases(a.results)
		s.Findings = a.findings
		s.Output = report
		s.DurationMS = time.Since(started).Milliseconds()
	})
	// No report for the fixers: each issue already quotes its failures.
	return &evaluation{verdict: Verdict{Status: VerdictChangesRequested, Issues: a.issues}, parsed: true}, true
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

	a, ok := o.reassess(ctx, ss, st)
	if !ok {
		o.cancelStep(ref, time.Since(started))
		return evaluation{}, false
	}
	extra, ok := o.changeIssues(ctx, ss, st, a)
	if !ok {
		o.cancelStep(ref, time.Since(started))
		return evaluation{}, false
	}
	a.issues = append(a.issues, extra...)
	results, findings, traces, issues := a.results, a.findings, a.traces, a.issues
	if len(traces) > 0 {
		if ss.git != nil {
			commit, _ := ss.git.Head(ctx)
			for i := range traces {
				traces[i].Commit = commit
			}
		}
		o.updateStory(st.si, func(r *StoryRun) { r.Trace = traces })
	}

	verdict := Verdict{Status: VerdictApproved}
	if len(issues) > 0 {
		verdict = Verdict{Status: VerdictChangesRequested, Issues: issues}
	}
	report := verifierReport(a)
	elapsed := time.Since(started)
	o.updateStep(ref, func(s *Step) {
		s.Status = StepDone
		s.Checks = quality.WithoutCases(results)
		s.Findings = findings
		s.Output = report
		s.DurationMS = elapsed.Milliseconds()
	})
	// Issues quote the failures; no report. Without any command run, an
	// approval rests on static checks alone and is not an executed proof.
	return evaluation{verdict: verdict, parsed: true, noEvidence: len(results) == 0}, true
}

// assessment is the outcome of the deterministic checks on one directory.
type assessment struct {
	results  []quality.Result
	findings []quality.Finding
	traces   []quality.Trace
	issues   []Issue
	skipped  []string // commands left out because a cheaper layer failed
}

func (a assessment) passed() bool { return len(a.issues) == 0 }

// failedCommands lists what failed for a reason a retry can change: a command
// out of time would only spend its whole limit again.
func (a assessment) failedCommands() []string {
	var names []string
	for _, r := range a.results {
		if !r.Passed && !r.TimedOut {
			names = append(names, r.Name)
		}
	}
	return names
}

// assess runs all the project commands in dir, the static checks on the
// changed files and the test traceability. It returns false when ctx was
// canceled.
func (o *Orchestrator) assess(ctx context.Context, ss *session, st *storyState, dir string, changed []string) (assessment, bool) {
	a, ok := o.assessChecks(ctx, ss, st, dir, changed, ss.checks)
	o.linkTraces(ss, st, &a)
	return a, ok
}

// linkTraces ties the traces to the cases the commands reported, once every
// command of the assessment ran, and asks for the required tests that only
// exist as skipped cases.
func (o *Orchestrator) linkTraces(ss *session, st *storyState, a *assessment) {
	if len(a.traces) == 0 {
		return
	}
	a.traces = quality.LinkResults(a.traces, a.results)
	a.issues = append(a.issues, o.skippedIssues(ss, a.traces, st.plan)...)
}

// reassess proves a fix cheaply: first the commands that failed; only when
// they pass, the others. A fix usually touches one failure, and the costliest
// commands need to run just once, at the end.
func (o *Orchestrator) reassess(ctx context.Context, ss *session, st *storyState) (assessment, bool) {
	changed := o.changedFiles(ss, st.started)
	failed := func(c quality.Command) bool { return slices.Contains(st.red, c.Name) }
	red := slices.DeleteFunc(slices.Clone(ss.checks), func(c quality.Command) bool { return !failed(c) })
	if len(red) == 0 {
		a, ok := o.assess(ctx, ss, st, ss.repo, changed)
		st.note(a)
		return a, ok
	}
	first, ok := o.assessChecks(ctx, ss, st, ss.repo, changed, red)
	if !ok || len(first.failedCommands()) > 0 {
		o.linkTraces(ss, st, &first)
		st.note(first)
		first.skipped = append(first.skipped, otherNames(ss.checks, red)...)
		st.skipped = true
		return first, ok
	}
	rest := slices.DeleteFunc(slices.Clone(ss.checks), failed)
	a, ok := o.assessChecks(ctx, ss, st, ss.repo, changed, rest)
	a.results = append(first.results, a.results...)
	o.linkTraces(ss, st, &a)
	st.note(a)
	return a, ok
}

func otherNames(checks, excluded []quality.Command) []string {
	var names []string
	for _, c := range checks {
		if !slices.Contains(excluded, c) {
			names = append(names, c.Name)
		}
	}
	return names
}

// note remembers what the next round needs to run again.
func (st *storyState) note(a assessment) {
	st.red = a.failedCommands()
	st.skipped = len(a.skipped) > 0
}

// assessChecks runs checks from the cheapest layer and stops at the first
// layer that fails: tests of code that does not compile only repeat the error.
func (o *Orchestrator) assessChecks(ctx context.Context, ss *session, st *storyState, dir string, changed []string, checks []quality.Command) (assessment, bool) {
	var a assessment
	timeout := time.Duration(ss.policy.CheckTimeoutS) * time.Second
	failedLayer := -1
	for _, c := range quality.InLayers(checks) {
		if failedLayer >= 0 && quality.Layer(c) > failedLayer {
			a.skipped = append(a.skipped, c.Name)
			continue
		}
		o.logf(LevelInfo, "Executando %s: %s", c.Name, c.Run)
		r := o.runGated(ctx, ss, dir, c, timeout)
		if ctx.Err() != nil {
			return a, false
		}
		a.results = append(a.results, r)
		if r.Passed {
			o.logf(LevelSuccess, "%s passou em %s.", c.Name, time.Duration(r.DurationMS)*time.Millisecond)
			continue
		}
		failedLayer = quality.Layer(c)
		o.logf(LevelWarn, "%s falhou (%s).", c.Name, exitLabel(r))
		a.addFailure(o.commandFailure(ss, st, c, r))
	}

	a.findings = append(a.findings, quality.Syntax(ctx, dir, changed)...)
	a.findings = append(a.findings, quality.Format(ctx, dir, changed)...)
	a.findings = append(a.findings, quality.Readability(dir, changed, ss.policy.MaxLineLength)...)
	for _, f := range a.findings {
		severity := "média"
		if f.Check == "syntax" {
			severity = "alta"
		}
		a.issues = append(a.issues, Issue{
			Agent:       o.assignFile(f.File, st.plan),
			Severity:    severity,
			Description: findingLabel(f.Check) + ": " + f.Message,
			Location:    findingLocation(f),
		})
	}

	a.traces = quality.TraceTests(dir, st.reqs)
	a.issues = append(a.issues, o.traceIssues(ss, a.traces, st.plan)...)
	return a, true
}

// addFailure keeps one issue per cause: a command that only repeats failures
// another already reported adds nothing but a second owner for them.
func (a *assessment) addFailure(is Issue) {
	reported := func(f string) bool {
		return slices.ContainsFunc(a.issues, func(other Issue) bool { return slices.Contains(other.Fingerprints, f) })
	}
	if len(is.Fingerprints) > 0 && !slices.ContainsFunc(is.Fingerprints, func(f string) bool { return !reported(f) }) {
		return
	}
	a.issues = append(a.issues, is)
}

// commandFailure turns a failed command into an issue, with what identifies
// the failures behind it.
func (o *Orchestrator) commandFailure(ss *session, st *storyState, c quality.Command, r quality.Result) Issue {
	is := Issue{Severity: "alta", Description: commandIssue(c, r), Location: c.Name, Infra: r.TimedOut}
	if r.TimedOut {
		is.Agent = o.assignCommand(c, st.plan)
		is.Fingerprints = []string{quality.TextFingerprint("timeout", c.Name)}
		return is
	}
	failures := r.Failures
	if len(failures) == 0 {
		failures = quality.ParseFailures(c, failureText(r))
	}
	is.Agent = o.assignFailure(ss, c, failures, st.plan)
	is.Fingerprints = quality.Fingerprints(failures)
	switch is.Origin = st.baseline.failureOrigin(c.Name, is.Fingerprints); is.Origin {
	case originPreexisting:
		is.Description = "[Falha preexistente: já falhava antes da US; ainda bloqueia a entrega] " + is.Description
	case originNew:
		is.Description = "[Falha nova: introduzida nesta US] " + is.Description
	}
	is.Class = classBuild
	for _, f := range failures {
		if !f.Compile {
			is.Class = classTest
		}
		is.Titles = append(is.Titles, failureTitle(f))
		if f.File != "" {
			is.Files = append(is.Files, path.Join(f.Dir, f.File))
		}
	}
	return is
}

func failureTitle(f quality.Failure) string {
	switch {
	case f.Test != "" && f.Message != "":
		return f.Test + ": " + f.Message
	case f.Test != "":
		return f.Test
	case f.File != "":
		return fmt.Sprintf("%s:%d %s", f.File, f.Line, f.Message)
	}
	return f.Message
}

// runGated runs c, giving a command that starts a stack the machine's single
// slot. Holding it, every disposable stack of the repository is garbage, so
// one more sweep costs nothing and caps what agents and killed runs left.
// Stories running side by side may each have an agent using a stack, so there
// only the run's ends sweep.
func (o *Orchestrator) runGated(ctx context.Context, ss *session, dir string, c quality.Command, timeout time.Duration) quality.Result {
	if c.Resource != "" {
		release, err := o.lockResource(ctx, c.Resource, func() { o.logf(LevelInfo, "%s aguarda o recurso %s.", c.Name, c.Resource) })
		if err != nil {
			if ctx.Err() != nil {
				return quality.Result{Name: c.Name, Command: c.Run, ExitCode: -1, Output: err.Error()}
			}
			o.logf(LevelWarn, "Trava do recurso %s indisponível (%v); seguindo sem ela.", c.Resource, err)
		} else {
			defer release()
		}
	}
	if !quality.Heavy(c) {
		return o.runCheck(ctx, dir, c, timeout)
	}
	release, err := o.lockStackSlot(ctx, func() { o.logf(LevelInfo, "Aguardando vaga para E2E: %s.", c.Name) })
	switch {
	case err == nil:
		defer release()
	case ctx.Err() != nil:
		return quality.Result{Name: c.Name, Command: c.Run, ExitCode: -1, Output: err.Error()}
	default:
		o.logf(LevelWarn, "Vaga de E2E indisponível (%v); seguindo sem o limite.", err)
	}
	if ss.policy.Sequential {
		roots := []string{ss.repo, dir}
		o.sweepLeftovers(roots, ss.policy.EphemeralStacks)
		defer o.sweepLeftovers(roots, ss.policy.EphemeralStacks)
	}
	return o.runCheck(ctx, dir, c, timeout)
}

// sweepRun removes the stacks an earlier run or an agent left behind in repo.
// The repository lock guarantees no other run is using them.
func (o *Orchestrator) sweepRun(repo string, dry bool) {
	if !dry {
		o.sweepLeftovers([]string{repo}, o.ws.Policy().EphemeralStacks)
	}
}

// sweepLeftovers never fails a run: the context is its own, since the run's
// may already be canceled, and a stack that survives is only reported.
func (o *Orchestrator) sweepLeftovers(roots, patterns []string) {
	ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
	defer cancel()
	removed, err := o.sweepStacks(ctx, roots, patterns)
	if len(removed) > 0 {
		o.logf(LevelInfo, "Stacks de teste removidas: %s.", strings.Join(removed, ", "))
	}
	if err != nil {
		o.logf(LevelWarn, "Limpeza de stacks de teste: %v.", err)
	}
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

// assignFailure finds who owns a failing command from the evidence, in order:
// the front of the plan that covers the failing file, the stack of the
// directory the command runs in, and last its wording.
func (o *Orchestrator) assignFailure(ss *session, c quality.Command, failures []quality.Failure, plan Plan) string {
	dir := quality.CommandDir(c)
	for _, f := range failures {
		if f.File == "" {
			continue
		}
		if agent := planOwner(plan, path.Join(dir, f.File)); agent != "" {
			return o.prefer(agent, plan)
		}
	}
	if agent := stackOwner(ss.report, dir); agent != "" {
		return o.prefer(agent, plan)
	}
	return o.assignCommand(c, plan)
}

// planOwner is the agent of the plan's front whose files cover file.
func planOwner(plan Plan, file string) string {
	for _, s := range plan.Streams {
		for _, covered := range s.Files {
			if strings.HasPrefix(file, strings.TrimPrefix(covered, "./")) {
				return s.Agent
			}
		}
	}
	return ""
}

// stackOwner tells interface code from services by the stack found in dir.
func stackOwner(report *workspace.Report, dir string) string {
	if report == nil || dir == "" {
		return ""
	}
	owner := ""
	for _, s := range report.Stacks {
		if path.Clean(s.Dir) != path.Clean(dir) {
			continue
		}
		if s.Name == "Angular" || s.Name == "Playwright" {
			return "frontend"
		}
		owner = "backend"
	}
	return owner
}

func (o *Orchestrator) assignCommand(c quality.Command, plan Plan) string {
	if containsAny(strings.ToLower(c.Name+" "+c.Run), "e2e", "playwright", "cypress", "frontend", "front", "web") {
		return o.prefer("frontend", plan)
	}
	return defaultImplementer(o.stages, plan)
}

func (o *Orchestrator) assignKind(kind string, plan Plan) string {
	switch kind {
	case "e2e":
		return o.prefer("frontend", plan)
	case "unit", "integration":
		return o.prefer("backend", plan)
	}
	return defaultImplementer(o.stages, plan)
}

// traceIssues turns required tests missing from the repository into one
// issue per responsible implementer.
func (o *Orchestrator) traceIssues(ss *session, traces []quality.Trace, plan Plan) []Issue {
	missing := map[string][]string{}
	var order []string
	for _, t := range traces {
		// With the E2E stage off, nobody in this flow writes those tests.
		if t.Found() || (t.Kind == "e2e" && ss.policy.StageDisabled("e2e")) {
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
			Description: fmt.Sprintf("%d caso(s) de teste da especificação sem teste no repositório. "+
				"Escreva-os com o ID no nome: %s", len(ids), strings.Join(ids, "; ")),
			Location: "rastreabilidade",
			Class:    classMissingTest,
			// The list shrinks as tests appear: it is still the same task.
			Fingerprints: []string{quality.TextFingerprint("rastreabilidade", agent)},
			Titles:       []string{"Testes da especificação sem implementação"},
		})
	}
	return issues
}

// skippedIssues asks for the required tests whose every reported case was
// skipped: a test that never runs proves nothing.
func (o *Orchestrator) skippedIssues(ss *session, traces []quality.Trace, plan Plan) []Issue {
	skipped := map[string][]string{}
	var order []string
	for _, t := range traces {
		if t.Status != quality.TraceSkipped || (t.Kind == "e2e" && ss.policy.StageDisabled("e2e")) {
			continue
		}
		agent := o.assignKind(t.Kind, plan)
		if _, seen := skipped[agent]; !seen {
			order = append(order, agent)
		}
		skipped[agent] = append(skipped[agent], t.ID)
	}
	var issues []Issue
	for _, agent := range order {
		ids := skipped[agent]
		issues = append(issues, Issue{
			Agent:    agent,
			Severity: "alta",
			Description: fmt.Sprintf("%d caso(s) de teste da especificação só aparecem como ignorados (skip) no relatório: %s. "+
				"Teste ignorado não comprova o critério; faça-o rodar.", len(ids), strings.Join(ids, ", ")),
			Location:     "rastreabilidade",
			Class:        classMissingTest,
			Fingerprints: []string{quality.TextFingerprint("ignorado", agent)},
			Titles:       []string{"Testes da especificação ignorados"},
		})
	}
	return issues
}

func commandIssue(c quality.Command, r quality.Result) string {
	if r.TimedOut {
		return fmt.Sprintf("`%s` (%s) excedeu o tempo limite depois de %s, sem concluir. Não indica erro no código: "+
			"aumente o tempo limite em Portões de qualidade. Última saída:\n```\n%s\n```",
			c.Run, c.Name, (time.Duration(r.DurationMS) * time.Millisecond).Round(time.Second), tail(r.Output, maxTimeoutOutput))
	}
	return fmt.Sprintf("`%s` (%s) %s. Corrija o código até passar. Mude teste só se contrariar a especificação, com motivo. "+
		"Falhas:\n```\n%s\n```", c.Run, c.Name, exitLabel(r), tail(quality.FailureSummary(failureText(r), maxIssueLines), maxIssueOutput))
}

// failureText prefers the evidence kept from the whole output over its tail.
func failureText(r quality.Result) string {
	if r.Evidence != "" {
		return r.Evidence
	}
	return r.Output
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
func verifierReport(a assessment) string {
	results, findings, traces := a.results, a.findings, a.traces
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

	if len(a.skipped) > 0 {
		fmt.Fprintf(&b, "\nNão executados nesta rodada, até o que falhou passar: %s.\n", strings.Join(a.skipped, ", "))
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
		byStatus := map[string][]string{}
		for _, t := range traces {
			if t.Found() {
				found++
			}
			byStatus[t.Status] = append(byStatus[t.Status], t.ID)
		}
		fmt.Fprintf(&b, "\n## Rastreabilidade dos testes\n\n%d de %d casos de teste encontrados nos arquivos de teste; "+
			"%d comprovados por execução.\n", found, len(traces), len(byStatus[quality.TraceProven]))
		for _, status := range traceReportOrder {
			if ids := byStatus[status]; len(ids) > 0 {
				fmt.Fprintf(&b, "- %s: %s\n", status, strings.Join(ids, ", "))
			}
		}
	}
	return b.String()
}

// traceReportOrder lists what a trace may be short of, worst first.
var traceReportOrder = []string{
	quality.TraceFailed, quality.TraceMissing, quality.TraceSkipped, quality.TraceNoReport, quality.TraceNotRun, quality.TraceExitOnly,
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
