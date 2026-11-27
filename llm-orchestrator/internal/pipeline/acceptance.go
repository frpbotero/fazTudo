package pipeline

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/quality"
)

// ScenarioResult is the functional QA's judgment of one acceptance scenario,
// as the orchestrator accepted it: the model interprets the evidence, it
// does not replace it.
type ScenarioResult struct {
	Criterion string `json:"criterion"`
	Scenario  string `json:"scenario,omitempty"`
	Method    string `json:"method,omitempty"`
	Negative  bool   `json:"negative,omitempty"`
	Status    string `json:"status"` // Scenario* statuses
	// Evidence is what QA cited, with sensitive data redacted; Tests are the
	// test IDs it named, and Artifacts the runner's files for their cases.
	Evidence  string   `json:"evidence,omitempty"`
	Tests     []string `json:"tests,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
	// Responsible is who vouches for a scenario only inspection can check.
	Responsible string `json:"responsible,omitempty"`
	// Note says what the orchestrator changed in QA's answer, and why.
	Note string `json:"note,omitempty"`
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

const acceptanceInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"status\": \"approved\" | \"changes_requested\", \"issues\": [{\"agent\": \"frontend\" | \"backend\", \"severity\": \"alta\" | \"média\" | \"baixa\", " +
	"\"description\": \"problema e correção\", \"location\": \"arquivo:linha\"}], " +
	"\"scenarios\": [{\"criterion\": \"critério da US\", \"scenario\": \"dado/quando/então\", \"method\": \"teste unitário | integração | e2e | inspeção\", " +
	"\"negative\": false, \"status\": \"aceito\" | \"recusado\" | \"sem evidência\", \"tests\": [\"ID do caso executado\"], " +
	"\"evidence\": \"caso executado, comando ou arquivo:linha\"}]}\n```\n" +
	"Um item em scenarios por cenário de aceitação, inclusive os negativos. aceito só com evidência: " +
	"cenário testado cita em tests o ID de um caso comprovado por execução; inspeção cita arquivo:linha. " +
	"Sem evidência, diga sem evidência; não deduza da descrição da implementação. " +
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
			neg := ""
			if a.Negative {
				neg = " (negativo)"
			}
			fmt.Fprintf(&b, "- %s%s: %s [%s]\n", a.Criterion, neg, a.Scenario, a.Method)
		}
		b.WriteString("\n")
	} else if run.Readiness != nil && len(run.Readiness.Criteria) > 0 {
		b.WriteString("Critérios de aceite do Passo 0 (o plano não trouxe cenários):\n")
		for _, c := range run.Readiness.Criteria {
			fmt.Fprintf(&b, "- %s\n", c.Text)
		}
		b.WriteString("\n")
	}
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
	if r := run.Reproduction; r != nil {
		fmt.Fprintf(&b, "\nReprodução do bug: %s. %s\n", r.Status, r.Detail)
	}
	b.WriteString("\nSó \"comprovado\" mostra que o caso rodou e passou.\n\n")
	return b.String()
}

// judgeAcceptance checks QA's scenarios against the executed evidence. A
// scenario accepted without a proven case (or, for an inspection, without a
// location) has no evidence; one whose cited case failed is rejected. Every
// planned scenario QA left out has no evidence either.
func (o *Orchestrator) judgeAcceptance(ss *session, st *storyState, ev evaluation) evaluation {
	if !ev.parsed || ev.simulated {
		return ev
	}
	answer, _ := lastJSON(ev.report, func(a struct {
		Scenarios []ScenarioResult `json:"scenarios"`
	}) bool {
		return true
	})
	traces := o.storyRun(st.si).Trace
	results := make([]ScenarioResult, 0, len(answer.Scenarios))
	for _, s := range answer.Scenarios {
		if s.Criterion = strings.TrimSpace(s.Criterion); s.Criterion != "" {
			results = append(results, checkScenario(s, traces))
		}
	}
	for _, a := range st.plan.Acceptance {
		judged := slices.ContainsFunc(results, func(r ScenarioResult) bool {
			return normalizeQuestion(r.Criterion) == normalizeQuestion(a.Criterion) &&
				(r.Scenario == "" || a.Scenario == "" || normalizeQuestion(r.Scenario) == normalizeQuestion(a.Scenario) || r.Negative == a.Negative)
		})
		if !judged {
			results = append(results, ScenarioResult{
				Criterion: a.Criterion, Scenario: a.Scenario, Method: a.Method, Negative: a.Negative,
				Status: ScenarioNoEvidence, Note: "cenário do plano que o QA não avaliou",
			})
		}
	}

	verdict := ev.verdict
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
			Titles:       []string{"Cenário de aceitação recusado"},
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
		i := slices.IndexFunc(traces, func(t quality.Trace) bool { return strings.EqualFold(t.ID, strings.TrimSpace(id)) })
		if i < 0 {
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
	{regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`), "[número removido]"},
}

// redact removes sensitive data from text kept in the run's record.
func redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}
