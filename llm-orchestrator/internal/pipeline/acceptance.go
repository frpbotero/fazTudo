package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
)

// ScenarioResult is the functional QA's judgment of one acceptance scenario,
// as the orchestrator accepted it: the model interprets the evidence, it
// does not replace it.
type ScenarioResult struct {
	// ID is the planned scenario's (AC-n); empty for one QA added.
	ID        string `json:"id,omitempty"`
	Criterion string `json:"criterion"`
	Scenario  string `json:"scenario,omitempty"`
	Method    string `json:"method,omitempty"`
	Negative  bool   `json:"negative,omitempty"`
	Status    string `json:"status"` // Scenario* statuses
	// Evidence is what QA cited, with sensitive data redacted; Tests are the
	// test IDs it named, and Artifacts the runner's files for their cases.
	Evidence string   `json:"evidence,omitempty"`
	Tests    []string `json:"tests,omitempty"`
	// Commands are the project commands QA cited for a scenario that only
	// their result proves, such as "the tests and the linter pass".
	Commands  []string `json:"commands,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
	// Responsible is who vouches for a scenario only inspection can check.
	Responsible string `json:"responsible,omitempty"`
	// Note says what the orchestrator changed in QA's answer, and why.
	Note string `json:"note,omitempty"`
	// Extra marks a scenario QA judged that the plan does not have.
	Extra bool `json:"extra,omitempty"`
}

// Scenario statuses. Only ScenarioAccepted counts toward approval.
const (
	ScenarioAccepted    = "aceito"
	ScenarioRejected    = "recusado"
	ScenarioNoEvidence  = "sem evidência"
	ScenarioInvalidated = "invalidado" // the code changed after it was accepted
)

// inspectionResponsible vouches for scenarios checked by reading, not running.
const inspectionResponsible = "QA funcional (inspeção do código)"

// commandResponsible vouches for scenarios the project commands prove.
const commandResponsible = "verificação executável do orquestrador"

const acceptanceInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"status\": \"approved\" | \"changes_requested\", \"issues\": [{\"agent\": \"frontend\" | \"backend\", \"severity\": \"alta\" | \"média\" | \"baixa\", " +
	"\"description\": \"problema e correção\", \"location\": \"arquivo:linha\"}], " +
	"\"scenarios\": [{\"id\": \"AC-n do plano\", \"criterion\": \"critério da US\", \"scenario\": \"dado/quando/então\", \"method\": \"teste unitário | integração | e2e | inspeção\", " +
	"\"negative\": false, \"status\": \"aceito\" | \"recusado\" | \"sem evidência\", \"tests\": [\"ID do caso executado\"], " +
	"\"commands\": [\"comando do projeto\"], \"evidence\": \"caso executado, comando ou arquivo:linha\"}]}\n```\n" +
	"Um item em scenarios por cenário de aceitação, inclusive os negativos, com o id do cenário do plano (AC-n); " +
	"cenário que você acrescentar fora do plano vai sem id. aceito só com evidência: " +
	"cenário testado cita em tests o ID de um caso comprovado por execução; inspeção cita arquivo:linha; " +
	"cenário que só o resultado de um comando do projeto prova (\"os testes passam\") usa method comando e cita em commands " +
	"os comandos da última verificação. Sem evidência, diga sem evidência; não deduza da descrição da implementação. " +
	"Cenário recusado vira issue com o agente que corrige."

// acceptanceEvidence is what the runner proved, for QA to interpret: the
// scenarios to judge, each criterion's executed cases and the bug's
// reproduction.
func (o *Orchestrator) acceptanceEvidence(st *storyState) string {
	run := o.storyRun(st.si)
	var b strings.Builder
	b.WriteString("## Evidência executada\n\n")
	if len(st.plan.Acceptance) > 0 {
		b.WriteString("Cenários de aceitação do plano:\n")
		for _, a := range st.plan.Acceptance {
			b.WriteString(describeScenario(a))
		}
		b.WriteString("\n")
	} else if run.Readiness != nil && len(run.Readiness.Criteria) > 0 {
		b.WriteString("Critérios de aceite do Passo 0 (o plano não trouxe cenários):\n")
		for _, c := range run.Readiness.Criteria {
			fmt.Fprintf(&b, "- %s\n", c.Text)
		}
		b.WriteString("\n")
	}
	b.WriteString(executedEvidence(run))
	return b.String()
}

// executedEvidence is what ran for the story: the specialties' requirements,
// each spec case with its executed cases, and the bug's reproduction.
func executedEvidence(run StoryRun) string {
	var b strings.Builder
	b.WriteString(specialtyRequirements(run))
	if len(run.Trace) == 0 {
		b.WriteString("Nenhum caso de teste da especificação foi rastreado.\n")
	}
	for _, t := range run.Trace {
		fmt.Fprintf(&b, "- %s (%s): %s", t.ID, clipRunes(t.Description, 80), t.Status)
		for _, c := range t.Cases {
			fmt.Fprintf(&b, "; %s %s via %s", c.Name, c.Status, c.Command)
		}
		b.WriteString("\n")
	}
	if checks := lastChecks(run.Steps); len(checks) > 0 {
		b.WriteString("\nComandos do projeto na última verificação:\n")
		for _, c := range checks {
			fmt.Fprintf(&b, "- `%s` (%s): %s\n", c.Command, c.Name, checkOutcome(c))
		}
	}
	if r := run.Reproduction; r != nil {
		fmt.Fprintf(&b, "\nReprodução do bug: %s. %s\n", r.Status, r.Detail)
	}
	b.WriteString("\nSó \"comprovado\" mostra que o caso rodou e passou.\n\n")
	return b.String()
}

// missingScenariosInstructions is the answer contract when QA is asked again
// for the planned scenarios it left out.
const missingScenariosInstructions = "\n\nResposta: só o bloco ```json:\n" +
	"```json\n{\"scenarios\": [{\"id\": \"AC-n\", \"criterion\": \"critério da US\", \"scenario\": \"dado/quando/então\", " +
	"\"method\": \"teste unitário | integração | e2e | inspeção | comando\", \"negative\": false, \"status\": \"aceito\" | \"recusado\" | \"sem evidência\", " +
	"\"tests\": [\"ID do caso executado\"], \"commands\": [\"comando do projeto\"], \"evidence\": \"caso executado, comando ou arquivo:linha\"}]}\n```\n" +
	"Um item por cenário listado, com o id dele. aceito só com evidência: cenário testado cita em tests o ID de um caso " +
	"comprovado por execução; inspeção cita arquivo:linha; comando cita em commands os comandos da última verificação. " +
	"Sem evidência, diga sem evidência."

// buildMissingScenariosPrompt asks QA, read-only, only for the planned
// scenarios its last answer left out, with the same executed evidence.
func buildMissingScenariosPrompt(missing []AcceptanceScenario, run StoryRun, report string) string {
	var b strings.Builder
	b.WriteString("Sua avaliação anterior não trouxe estes cenários do plano. Não altere arquivos e não refaça o resto: " +
		"avalie só eles, com a evidência executada abaixo.\n\n## Cenários a avaliar\n\n")
	for _, a := range missing {
		b.WriteString(describeScenario(a))
	}
	b.WriteString("\n## Evidência executada\n\n")
	b.WriteString(executedEvidence(run))
	fmt.Fprintf(&b, "## Sua avaliação anterior\n\n%s\n", clip(strings.TrimSpace(report), maxArtifactBytes))
	b.WriteString(missingScenariosInstructions)
	return b.String()
}

// recoverScenarios asks the functional QA again, read-only, only for the
// planned scenarios it left out, and judges its answers with the earlier
// ones. The request is a new step after stepID; base names it.
func (o *Orchestrator) recoverScenarios(ctx context.Context, ss *session, st *storyState, gate Stage, base, stepID string, ev evaluation, try int) (evaluation, string, bool) {
	ids := make([]string, len(ev.missing))
	for i, a := range ev.missing {
		ids[i] = a.ID
	}
	o.logf(LevelWarn, "%s não avaliou %d cenário(s) do plano (%s); pedindo só esses (tentativa %d de %d).",
		gate.Title, len(ids), strings.Join(ids, ", "), try, maxFormatRecoveries)
	id := fmt.Sprintf("%s-cenarios-%d", base, try)
	o.insertSteps(st.si, stepID, Step{
		ID: id, Title: fmt.Sprintf("%s — cenários faltantes (%d)", gate.Title, try), Role: gate.Role, Tier: gate.Tier,
		Status: StepPending, Kind: "verify",
	})
	ref := stepRef{st.si, id}
	resp, ok := o.runStage(ctx, ref, provider.Request{
		Role:     gate.Role,
		System:   gate.System,
		Prompt:   buildMissingScenariosPrompt(ev.missing, o.storyRun(st.si), ev.report),
		Tier:     gate.Tier,
		WorkDir:  ss.repo,
		ReadOnly: true,
	})
	if !ok || !o.afterAgent(ctx, ss, st, ref) {
		return evaluation{}, id, false
	}
	// Only answers for what was asked: a repeated scenario would count twice.
	asked := func(s ScenarioResult) bool {
		sid := strings.ToUpper(strings.TrimSpace(s.ID))
		return sid == "" || slices.Contains(ids, sid)
	}
	recovered := slices.DeleteFunc(qaScenarios(resp.Output), func(s ScenarioResult) bool { return !asked(s) })
	ev.scenarios = append(slices.Clone(ev.scenarios), recovered...)
	ev = o.judgeScenarios(ss, st, ev)
	ev.verdict, ev.advice = proportional(ev.verdict, ss.policy)
	o.updateStep(ref, func(s *Step) {
		s.Note = fmt.Sprintf("%d de %d cenário(s) avaliado(s)", len(ids)-len(ev.missing), len(ids))
	})
	return ev, id, true
}

// checkOutcome is how a command ended, as QA reads it.
func checkOutcome(c quality.Result) string {
	switch {
	case c.TimedOut:
		return "excedeu o tempo"
	case c.Passed:
		return fmt.Sprintf("passou em %s", time.Duration(c.DurationMS)*time.Millisecond)
	}
	return fmt.Sprintf("falhou (código %d)", c.ExitCode)
}

// describeScenario is a planned scenario as QA reads it, ID first.
func describeScenario(a AcceptanceScenario) string {
	neg := ""
	if a.Negative {
		neg = " (negativo)"
	}
	return fmt.Sprintf("- %s: %s%s: %s [%s]\n", a.ID, a.Criterion, neg, a.Scenario, a.Method)
}

// judgeAcceptance reads QA's scenarios and holds them to the executed
// evidence (see judgeScenarios).
func (o *Orchestrator) judgeAcceptance(ss *session, st *storyState, ev evaluation) evaluation {
	if !ev.parsed || ev.simulated {
		return ev
	}
	ev.scenarios, ev.qaVerdict = qaScenarios(ev.report), ev.verdict
	return o.judgeScenarios(ss, st, ev)
}

// qaScenarios is the scenarios block of a QA answer.
func qaScenarios(report string) []ScenarioResult {
	answer, _ := lastJSON(report, func(a struct {
		Scenarios []ScenarioResult `json:"scenarios"`
	}) bool {
		return true
	})
	return answer.Scenarios
}

// judgeScenarios checks QA's scenarios against the executed evidence. A
// scenario accepted without a proven case (or, for an inspection, without a
// location) has no evidence; one whose cited case failed is rejected. A QA
// scenario is tied to the plan by its ID, or by its text when it has none;
// one with an ID the plan lacks is extra. Every planned scenario QA left out
// has no evidence and is listed in ev.missing, to be asked for again.
func (o *Orchestrator) judgeScenarios(ss *session, st *storyState, ev evaluation) evaluation {
	run := o.storyRun(st.si)
	traces, checks := run.Trace, lastChecks(run.Steps)
	planned := st.plan.Acceptance
	results := make([]ScenarioResult, 0, len(ev.scenarios)+len(planned))
	for _, s := range ev.scenarios {
		if s.Criterion = strings.TrimSpace(s.Criterion); s.Criterion == "" {
			continue
		}
		r := checkScenarioWith(s, traces, checks)
		r.ID = strings.ToUpper(strings.TrimSpace(s.ID))
		if r.ID != "" && !slices.ContainsFunc(planned, func(a AcceptanceScenario) bool { return a.ID == r.ID }) {
			r.ID, r.Extra = "", true
			r.Note = joinNonEmpty("cenário fora do plano", "; ", r.Note)
		}
		results = append(results, r)
	}
	ev.missing = nil
	for _, a := range planned {
		i := slices.IndexFunc(results, func(r ScenarioResult) bool { return r.ID == a.ID })
		if i < 0 {
			i = slices.IndexFunc(results, func(r ScenarioResult) bool {
				return r.ID == "" && !r.Extra && sameCriterionText(r.Criterion, a.Criterion) &&
					(r.Scenario == "" || a.Scenario == "" || normalizeQuestion(r.Scenario) == normalizeQuestion(a.Scenario) || r.Negative == a.Negative)
			})
		}
		if i >= 0 {
			results[i].ID = a.ID
			continue
		}
		ev.missing = append(ev.missing, a)
		results = append(results, ScenarioResult{
			ID: a.ID, Criterion: a.Criterion, Scenario: a.Scenario, Method: a.Method, Negative: a.Negative,
			Status: ScenarioNoEvidence, Note: "cenário do plano que o QA não avaliou",
		})
	}

	verdict := ev.qaVerdict
	verdict.Issues = slices.Clone(verdict.Issues)
	for _, r := range results {
		if r.Status != ScenarioRejected {
			continue
		}
		fp := quality.TextFingerprint("aceitação", normalizeQuestion(r.Criterion+" "+r.Scenario))
		if slices.ContainsFunc(verdict.Issues, func(is Issue) bool { return slices.Contains(is.Fingerprints, fp) }) {
			continue
		}
		verdict.Status = VerdictChangesRequested
		verdict.Issues = append(verdict.Issues, Issue{
			Agent:        defaultImplementer(o.stages, st.plan),
			Severity:     "alta",
			Description:  "Cenário de aceitação recusado: " + r.Criterion + " — " + r.Scenario + ". " + cmpOrString(r.Note, r.Evidence),
			Location:     "aceitação",
			Fingerprints: []string{fp},
			Titles:       []string{clipRunes("Cenário de aceitação recusado: "+strings.TrimPrefix(r.ID+" · "+r.Criterion, " · "), 120)},
		})
	}
	ev.verdict = verdict
	unproven := slices.ContainsFunc(results, func(r ScenarioResult) bool { return r.Status == ScenarioNoEvidence })
	ev.inconclusive = unproven && !verdict.NeedsChanges()
	st.acceptedAt = time.Time{}
	if !verdict.NeedsChanges() && !unproven && len(results) > 0 {
		st.acceptedAt = quality.FileClock(ss.repo)
	}
	o.updateStory(st.si, func(r *StoryRun) { r.Acceptance = slices.Clone(results) })
	return ev
}

// checkScenario holds one scenario to what ran. The model's status stands
// only when the evidence agrees with it.
func checkScenario(s ScenarioResult, traces []quality.Trace) ScenarioResult {
	return checkScenarioWith(s, traces, nil)
}

// checkScenarioWith also holds a scenario QA proved by the project commands
// to how they ended in the last verification (checks).
func checkScenarioWith(s ScenarioResult, traces []quality.Trace, checks []quality.Result) ScenarioResult {
	s.Scenario, s.Method = strings.TrimSpace(s.Scenario), strings.TrimSpace(s.Method)
	s.Evidence = redact(strings.TrimSpace(s.Evidence))
	s.Criterion, s.Scenario = redact(s.Criterion), redact(s.Scenario)
	s.Responsible, s.Note, s.Artifacts = "", "", nil
	status := strings.ToLower(strings.TrimSpace(s.Status))
	switch status {
	case "aceito", "accepted", "aprovado", "passed":
		status = ScenarioAccepted
	case "recusado", "rejected", "reprovado", "failed":
		status = ScenarioRejected
	default:
		status = ScenarioNoEvidence
	}
	s.Status = status
	if status != ScenarioAccepted {
		return s
	}
	if len(s.Commands) > 0 || isCommandMethod(s.Method) {
		return judgeByCommands(s, checks)
	}
	if isInspection(s.Method) {
		if !reLocation.MatchString(s.Evidence) {
			s.Status, s.Note = ScenarioNoEvidence, "inspeção sem arquivo:linha como evidência"
			return s
		}
		s.Responsible = inspectionResponsible
		return s
	}
	proven := false
	for _, id := range s.Tests {
		id = strings.TrimSpace(id)
		i := slices.IndexFunc(traces, func(t quality.Trace) bool { return strings.EqualFold(t.ID, id) })
		if i < 0 {
			// QA may cite the executed case instead of the spec's ID: that
			// case's own result is the evidence.
			switch c, ok := citedCase(traces, id); {
			case !ok:
			case c.Status == quality.CaseFailed:
				s.Status, s.Note = ScenarioRejected, "o caso "+c.Name+" falhou na execução"
				return s
			case c.Status == quality.CasePassed:
				s.Artifacts = append(s.Artifacts, c.Attachments...)
				proven = true
			}
			continue
		}
		t := traces[i]
		for _, c := range t.Cases {
			s.Artifacts = append(s.Artifacts, c.Attachments...)
		}
		switch t.Status {
		case quality.TraceFailed:
			s.Status, s.Note = ScenarioRejected, "o caso "+t.ID+" falhou na execução"
			return s
		case quality.TraceProven:
			proven = true
		}
	}
	if !proven {
		s.Status, s.Note = ScenarioNoEvidence, "nenhum caso citado foi comprovado por execução"
	}
	return s
}

// citedCase finds an executed case, linked to a trace, by its full name.
func citedCase(traces []quality.Trace, name string) (quality.CaseRef, bool) {
	for _, t := range traces {
		for _, c := range t.Cases {
			if strings.EqualFold(c.Name, name) {
				return c, true
			}
		}
	}
	return quality.CaseRef{}, false
}

// reEnumeration is the numbering QA may put before a criterion ("1. ", "2) ").
var reEnumeration = regexp.MustCompile(`^\s*\d{1,3}[.)]\s+`)

// sameCriterionText compares a criterion QA judged to a planned one, ignoring
// case, spacing and the numbering QA adds.
func sameCriterionText(a, b string) bool {
	strip := func(s string) string { return normalizeQuestion(reEnumeration.ReplaceAllString(s, "")) }
	return strip(a) == strip(b)
}

// judgeByCommands accepts a scenario only when every command it cites ran in
// the last verification and passed; one that failed rejects it.
func judgeByCommands(s ScenarioResult, checks []quality.Result) ScenarioResult {
	var cited []string
	for _, c := range s.Commands {
		if c = strings.Trim(strings.TrimSpace(c), "`"); c != "" {
			cited = append(cited, c)
		}
	}
	s.Commands = cited
	if len(cited) == 0 {
		s.Status, s.Note = ScenarioNoEvidence, "cenário por comando sem comando citado"
		return s
	}
	for _, c := range cited {
		i := slices.IndexFunc(checks, func(r quality.Result) bool {
			return strings.EqualFold(r.Name, c) || strings.EqualFold(strings.TrimSpace(r.Command), c)
		})
		switch {
		case i < 0:
			s.Status, s.Note = ScenarioNoEvidence, "o comando "+c+" não rodou na última verificação"
			return s
		case !checks[i].Passed:
			s.Status, s.Note = ScenarioRejected, "o comando "+c+" falhou na última verificação"
			return s
		}
	}
	s.Responsible = commandResponsible
	return s
}

func isCommandMethod(method string) bool {
	return strings.Contains(strings.ToLower(method), "comando")
}

func isInspection(method string) bool {
	m := strings.ToLower(method)
	return containsAny(m, "inspe", "manual", "revisão", "leitura")
}

var reLocation = regexp.MustCompile(`[\w./-]+\.[A-Za-z]{1,6}:\d+`)

// invalidateAcceptance drops an acceptance the code outgrew: a code change
// after it means the scenarios were judged on other code.
func (o *Orchestrator) invalidateAcceptance(ss *session, st *storyState) bool {
	if st.acceptedAt.IsZero() {
		return false
	}
	changed := slices.ContainsFunc(o.changedFiles(ss, st.acceptedAt), quality.IsCodeFile)
	if !changed {
		return false
	}
	st.acceptedAt = time.Time{}
	o.updateStory(st.si, func(r *StoryRun) {
		r.Acceptance = slices.Clone(r.Acceptance) // snapshots share the old one
		for i := range r.Acceptance {
			if r.Acceptance[i].Status == ScenarioAccepted {
				r.Acceptance[i].Status = ScenarioInvalidated
				r.Acceptance[i].Note = "código alterado depois da aceitação"
			}
		}
	})
	return true
}

func cmpOrString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Sensitive data a scenario's evidence may quote: credentials, tokens,
// e-mails and personal or card numbers.
var redactions = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?i)\bbearer\s+[\w.~+/-]+=*`), "Bearer [removido]"},
	{regexp.MustCompile(`(?i)\b(password|senha|passwd|secret|segredo|token|api[_-]?key)\b(\s*[:=]\s*|\s+)("[^"]*"|'[^']*'|\S+)`), "$1$2[removido]"},
	{regexp.MustCompile(`\beyJ[\w-]+\.[\w-]+\.[\w-]+`), "[jwt removido]"},
	{regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`), "[e-mail removido]"},
	{regexp.MustCompile(`\b\d{3}\.?\d{3}\.?\d{3}-?\d{2}\b`), "[cpf removido]"},
}

// cardNumber matches the ways card numbers are written: 13 to 19 digits in a
// row, four groups of four (plus a short tail), or the 4-6-5 of Amex. Other
// digit runs, such as the date and time in a branch name, are left alone.
var cardNumber = regexp.MustCompile(`\b(?:\d{13,19}|\d{4}(?:[ -]\d{4}){3}(?:[ -]\d{1,3})?|\d{4}[ -]\d{6}[ -]\d{5})\b`)

// redact removes sensitive data from text kept in the run's record.
func redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return cardNumber.ReplaceAllStringFunc(s, func(m string) string {
		if luhn(m) {
			return "[número removido]"
		}
		return m
	})
}

// luhn reports whether the digits in s pass the Luhn check every card number
// passes; separators are skipped.
func luhn(s string) bool {
	sum, double := 0, false
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] < '0' || s[i] > '9' {
			continue
		}
		d := int(s[i] - '0')
		if double {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum, double = sum+d, !double
	}
	return sum%10 == 0
}
