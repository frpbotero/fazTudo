package pipeline

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

// maxCauseAttempts is how many fix attempts one cause gets. The second uses
// the heavy model and must try something else; after it a person has to look.
const maxCauseAttempts = 2

// maxStalledRounds ends a fix loop whose failures stopped going down.
const maxStalledRounds = 2

// maxBriefCauses bounds how many causes one prompt lists.
const maxBriefCauses = 20

// Issue classes, set only by the orchestrator.
const (
	classBuild       = "build"
	classTest        = "test"
	classMissingTest = "missing-test"
)

// Cause statuses.
const (
	causeOpen       = "open"
	causeFixed      = "fixed"
	causePending    = "pending"
	causeBlocked    = "blocked"
	causeInherited  = "inherited"
	causeInfra      = "infra"
	causeUnverified = "unverified"
)

// Cause is one problem of a story followed across fix rounds, identified by
// its fingerprint, so a failure that returns is recognised instead of treated
// as new.
type Cause struct {
	Fingerprint string   `json:"fingerprint"`
	Title       string   `json:"title"`
	Owner       string   `json:"owner,omitempty"`
	Status      string   `json:"status"`
	Attempts    int      `json:"attempts,omitempty"`
	RootCause   string   `json:"root_cause,omitempty"`
	Evidence    string   `json:"evidence,omitempty"`
	Strategy    []string `json:"strategy,omitempty"`
	Regression  string   `json:"regression_test,omitempty"`
	Risk        string   `json:"risk,omitempty"`
	Proof       string   `json:"proof,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	// History records what each attempt did, so the next one does not repeat it.
	History    []string `json:"history,omitempty"`
	Reassigned bool     `json:"reassigned,omitempty"`
}

// issueCauses returns the fingerprints an issue stands for.
func issueCauses(is Issue) []string {
	if len(is.Fingerprints) > 0 {
		return is.Fingerprints
	}
	return []string{quality.TextFingerprint(is.Location, is.Description)}
}

func issueTitle(is Issue) string {
	if len(is.Titles) > 0 {
		return is.Titles[0]
	}
	title := strings.TrimSpace(is.Location + " " + strings.SplitN(is.Description, "\n", 2)[0])
	return clipRunes(title, 120)
}

// causes returns a copy of the story's causes by fingerprint.
func (o *Orchestrator) causes(si int) map[string]Cause {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := map[string]Cause{}
	if si >= 0 && si < len(o.run.Stories) {
		for _, c := range o.run.Stories[si].Causes {
			out[c.Fingerprint] = c
		}
	}
	return out
}

// updateCause changes one cause, creating it when new.
func (o *Orchestrator) updateCause(si int, fingerprint string, fn func(*Cause)) {
	o.updateStory(si, func(r *StoryRun) {
		i := slices.IndexFunc(r.Causes, func(c Cause) bool { return c.Fingerprint == fingerprint })
		if i < 0 {
			r.Causes = append(r.Causes, Cause{Fingerprint: fingerprint, Status: causeOpen})
			i = len(r.Causes) - 1
		}
		fn(&r.Causes[i])
	})
}

// inheritedFrom names the earlier story that already left this failure pending.
func (o *Orchestrator) inheritedFrom(si int, fingerprint string) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for i := 0; i < si && i < len(o.run.Stories); i++ {
		for _, c := range o.run.Stories[i].Causes {
			if c.Fingerprint == fingerprint && (c.Status == causePending || c.Status == causeInherited) {
				return o.run.Stories[i].Title
			}
		}
	}
	return ""
}

// touchesChanges reports whether the failure is in a file this story changed:
// then it is a regression of the story, whatever happened before it.
func touchesChanges(is Issue, changed []string) bool {
	return slices.ContainsFunc(is.Files, func(file string) bool { return slices.Contains(changed, file) })
}

// triage records the issues as causes and returns the ones worth an agent's
// call. The rest stay pending, each with the reason in its cause.
func (o *Orchestrator) triage(ss *session, st *storyState, issues []Issue) []Issue {
	changed := o.changedFiles(ss, st.started)
	var fixable []Issue
	for _, is := range issues {
		status, reason := o.classify(st, is, changed)
		for _, fingerprint := range issueCauses(is) {
			o.updateCause(st.si, fingerprint, func(c *Cause) {
				c.Title = cmp.Or(c.Title, issueTitle(is))
				c.Owner = cmp.Or(c.Owner, is.Agent)
				if c.Status != causeBlocked {
					c.Status = status
				}
				if reason != "" {
					c.Reason = reason
				}
			})
		}
		if status == causeOpen {
			fixable = append(fixable, o.withCauseOwner(st, is))
		}
	}
	return fixable
}

func (o *Orchestrator) classify(st *storyState, is Issue, changed []string) (status, reason string) {
	if is.Infra {
		return causeInfra, "Comando excedeu o tempo limite; não indica erro no código."
	}
	known := o.causes(st.si)
	fingerprints := issueCauses(is)
	if slices.ContainsFunc(fingerprints, func(f string) bool { return known[f].Status == causeBlocked }) {
		return causeBlocked, ""
	}
	if origin := o.inheritedOrigin(st, fingerprints); origin != "" && !touchesChanges(is, changed) {
		return causeInherited, "Já pendente em " + origin + "; esta US não alterou o arquivo da falha."
	}
	exhausted := func(f string) bool { return known[f].Attempts >= maxCauseAttempts }
	if !slices.ContainsFunc(fingerprints, func(f string) bool { return !exhausted(f) }) {
		return causePending, fmt.Sprintf("Persistiu após %d tentativas.", maxCauseAttempts)
	}
	return causeOpen, ""
}

// inheritedOrigin returns an earlier story only if it explains every failure.
func (o *Orchestrator) inheritedOrigin(st *storyState, fingerprints []string) string {
	origin := ""
	for _, f := range fingerprints {
		from := o.inheritedFrom(st.si, f)
		if from == "" {
			return ""
		}
		origin = from
	}
	return origin
}

// withCauseOwner applies an owner decided by an investigation or a hand-over.
func (o *Orchestrator) withCauseOwner(st *storyState, is Issue) Issue {
	known := o.causes(st.si)
	for _, f := range issueCauses(is) {
		if owner := known[f].Owner; owner != "" && isImplementer(o.stages, owner) {
			is.Agent = owner
			break
		}
	}
	return is
}

// closeCauses settles what is still open when a gate ends: what remains in
// issues is pending; the rest was fixed, unless some commands did not run and
// nothing proved it.
func (o *Orchestrator) closeCauses(st *storyState, issues []Issue) {
	remaining := map[string]bool{}
	for _, is := range issues {
		for _, f := range issueCauses(is) {
			remaining[f] = true
		}
	}
	for fingerprint, c := range o.causes(st.si) {
		if c.Status != causeOpen {
			continue
		}
		status := causeFixed
		switch {
		case remaining[fingerprint]:
			status = causePending
		case st.skipped:
			status = causeUnverified
		}
		o.updateCause(st.si, fingerprint, func(c *Cause) { c.Status = status })
	}
}

// stalled counts rounds in which the open failures did not go down.
func (st *storyState) stalled(failures int) bool {
	if st.lastFailures > 0 && failures >= st.lastFailures {
		st.stalls++
	} else {
		st.stalls = 0
	}
	st.lastFailures = failures
	return st.stalls >= maxStalledRounds
}

func countCauses(issues []Issue) int {
	seen := map[string]bool{}
	for _, is := range issues {
		for _, f := range issueCauses(is) {
			seen[f] = true
		}
	}
	return len(seen)
}

// settleNote explains why no agent will be called for the remaining issues.
func (o *Orchestrator) settleNote(st *storyState, issues []Issue) string {
	counts := map[string]int{}
	known := o.causes(st.si)
	for _, is := range issues {
		for _, f := range issueCauses(is) {
			counts[known[f].Status]++
		}
	}
	var parts []string
	for _, kind := range []struct{ status, label string }{
		{causeInfra, "excedeu o tempo limite"},
		{causeInherited, "herdada de US anterior"},
		{causeBlocked, "bloqueada"},
		{causePending, "persistiu após as tentativas"},
	} {
		if n := counts[kind.status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, kind.label))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d problema(s) sem correção automática", len(issues))
	}
	return strings.Join(parts, ", ")
}

// needsInvestigation skips the agent when the failure explains itself: a
// single compile error carries its file and line, and a missing test is
// already a precise task.
func needsInvestigation(issues []Issue) bool {
	if slices.ContainsFunc(issues, func(is Issue) bool { return is.Class == classTest }) {
		return true
	}
	build := 0
	for _, is := range issues {
		if is.Class == classBuild {
			build += len(issueCauses(is))
		}
	}
	return build > 1
}

// InvestigatorStage finds root causes before anyone changes code, the way an
// engineer does: reproduce, test hypotheses, then decide what to fix and in
// which order. It may run commands but has no tool to edit files.
var InvestigatorStage = Stage{
	ID:       "investigator",
	Title:    "Investigação",
	Role:     "Bug Investigator",
	Tier:     provider.TierStandard,
	ReadOnly: true,
	Diagnose: true,
	System: "Engenheiro sênior investigando falhas. Só investiga: não edita arquivos. " +
		"Conclusão só com evidência do código ou de um comando executado.",
	Task: "Para cada causa listada:\n" +
		"1. Reproduza com o menor comando possível (um arquivo de teste, um filtro). Sem suíte inteira nem E2E completo.\n" +
		"2. Leia a falha inteira e compare com o `git diff` desta US.\n" +
		"3. Liste as hipóteses, teste a mais provável e descarte as outras com evidência.\n" +
		"4. Pergunte por quê até chegar ao código que causa o problema, não ao sintoma.\n" +
		"5. Procure o mesmo padrão em outros pontos.\n" +
		"6. Defina a menor correção, o teste de regressão, o risco e quem corrige.\n" +
		"Causas com a mesma raiz: junte em `related`. Tentativa anterior falhou: traga hipótese nova.",
}

const investigationInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"findings\": [{\"cluster\": \"id da causa\", \"status\": \"confirmed\" | \"not_reproduced\" | \"inherited\" | \"blocked\", " +
	"\"root_cause\": \"causa raiz\", \"evidence\": \"arquivo:linha e trecho\", \"reproduced_by\": \"comando usado\", " +
	"\"owner\": \"frontend\" | \"backend\", \"strategy\": [\"passo\"], \"regression_test\": \"teste que prova\", \"risk\": \"o que pode quebrar\", " +
	"\"related\": [\"id\"]}], \"order\": [\"id da causa\"]}\n```\n" +
	"inherited = já falhava antes desta US, com evidência. blocked = depende de algo fora do código (motivo em evidence). " +
	"order = primeiro o que destrava os demais."

const fixInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"results\": [{\"cluster\": \"id da causa\", \"status\": \"fixed\" | \"blocked\" | \"reassign\", \"root_cause\": \"causa\", " +
	"\"changed\": [\"arquivo\"], \"proof\": \"teste executado e resultado\", \"reason\": \"motivo se blocked ou reassign\", " +
	"\"owner\": \"frontend\" | \"backend\"}]}\n```\n" +
	"blocked = não há o que fazer no código. reassign = é de outro agente (informe owner); use só com evidência."

// dossierFinding is the investigator's conclusion about one cause.
type dossierFinding struct {
	Cluster      string   `json:"cluster"`
	Status       string   `json:"status"`
	RootCause    string   `json:"root_cause"`
	Evidence     string   `json:"evidence"`
	ReproducedBy string   `json:"reproduced_by"` // informative: never executed
	Owner        string   `json:"owner"`
	Strategy     []string `json:"strategy"`
	Regression   string   `json:"regression_test"`
	Risk         string   `json:"risk"`
	Related      []string `json:"related"`
}

type dossier struct {
	Findings []dossierFinding `json:"findings"`
	Order    []string         `json:"order"`
}

type fixReport struct {
	Results []struct {
		Cluster   string   `json:"cluster"`
		Status    string   `json:"status"`
		RootCause string   `json:"root_cause"`
		Changed   []string `json:"changed"`
		Proof     string   `json:"proof"`
		Reason    string   `json:"reason"`
		Owner     string   `json:"owner"`
	} `json:"results"`
}

// investigate runs the investigator on the fixable issues and returns those
// still worth fixing, in the order it set. afterID is the step the next ones
// follow. false means ctx was canceled.
func (o *Orchestrator) investigate(ctx context.Context, ss *session, st *storyState, gate Stage, afterID string, issues []Issue, round int) ([]Issue, string, bool) {
	if !gate.Verifier || !needsInvestigation(issues) {
		return issues, afterID, true
	}
	step := Step{
		ID: fmt.Sprintf("investigate-%s-%d", gate.ID, round), Title: fmt.Sprintf("Investigação (%s, rodada %d)", gate.Title, round),
		Role: InvestigatorStage.Role, Tier: o.repairTier(st, issues), Status: StepPending, Kind: "investigate", Round: round, Gate: gate.ID,
	}
	o.insertSteps(st.si, afterID, step)
	resp, ok := o.runStage(ctx, stepRef{st.si, step.ID}, provider.Request{
		Role:     InvestigatorStage.Role,
		System:   o.texts.investigator.System,
		Prompt:   buildInvestigationPrompt(o.texts.investigator.Task, st.sc, ss.report, st.planText, issues, o.causes(st.si), o.changedFiles(ss, st.started)),
		Tier:     step.Tier,
		WorkDir:  ss.repo,
		ReadOnly: true,
		Diagnose: true,
	})
	if !ok {
		return nil, step.ID, false
	}
	found, parsed := lastJSON(resp.Output, func(d dossier) bool { return len(d.Findings) > 0 })
	if !parsed {
		o.logf(LevelWarn, "Investigação sem dossiê em JSON; seguindo só com a evidência dos comandos.")
		return issues, step.ID, true
	}
	return o.applyDossier(ss, st, found, issues), step.ID, true
}

// applyDossier records the conclusions and keeps what still needs a fix.
func (o *Orchestrator) applyDossier(ss *session, st *storyState, d dossier, issues []Issue) []Issue {
	changed := o.changedFiles(ss, st.started)
	findings := map[string]dossierFinding{}
	for _, f := range d.Findings {
		findings[f.Cluster] = f
		o.updateCause(st.si, f.Cluster, func(c *Cause) {
			c.RootCause, c.Evidence, c.Strategy, c.Regression, c.Risk = f.RootCause, f.Evidence, f.Strategy, f.Regression, f.Risk
			if isImplementer(o.stages, f.Owner) {
				c.Owner = f.Owner
			}
		})
	}
	var kept []Issue
	for _, is := range issues {
		if o.parkedByDossier(st, is, findings, changed) {
			continue
		}
		kept = append(kept, o.withCauseOwner(st, is))
	}
	return inOrder(kept, d.Order)
}

// parkedByDossier takes an issue out of the fix round when the investigator
// proved no code change here solves it. "Inherited" needs the orchestrator's
// own check: an agent must not excuse a regression of the story.
func (o *Orchestrator) parkedByDossier(st *storyState, is Issue, findings map[string]dossierFinding, changed []string) bool {
	fingerprints := issueCauses(is)
	concludes := func(status string) bool {
		return !slices.ContainsFunc(fingerprints, func(f string) bool { return findings[f].Status != status })
	}
	status := ""
	switch {
	case concludes("blocked"):
		status = causeBlocked
	case concludes("inherited") && !touchesChanges(is, changed):
		status = causeInherited
	default:
		return false
	}
	for _, f := range fingerprints {
		o.updateCause(st.si, f, func(c *Cause) { c.Status, c.Reason = status, findings[f].Evidence })
	}
	return true
}

// inOrder sorts issues by the first of their causes the order mentions.
func inOrder(issues []Issue, order []string) []Issue {
	rank := func(is Issue) int {
		best := len(order)
		for _, f := range issueCauses(is) {
			if i := slices.Index(order, f); i >= 0 && i < best {
				best = i
			}
		}
		return best
	}
	slices.SortStableFunc(issues, func(a, b Issue) int { return rank(a) - rank(b) })
	return issues
}

// repairTier raises the model when a cause already survived an attempt.
func (o *Orchestrator) repairTier(st *storyState, issues []Issue) provider.Tier {
	known := o.causes(st.si)
	for _, is := range issues {
		if slices.ContainsFunc(issueCauses(is), func(f string) bool { return known[f].Attempts > 0 }) {
			return provider.TierHeavy
		}
	}
	return InvestigatorStage.Tier
}

func (o *Orchestrator) countAttempts(st *storyState, issues []Issue) {
	for _, is := range issues {
		for _, f := range issueCauses(is) {
			o.updateCause(st.si, f, func(c *Cause) { c.Attempts++ })
		}
	}
}

// recordFix keeps what an attempt reported. A hand-over does not spend the
// attempt, once: the owner was wrong, so nothing was tried.
func (o *Orchestrator) recordFix(st *storyState, issues []Issue, output string) {
	report, _ := lastJSON(output, func(r fixReport) bool { return len(r.Results) > 0 })
	assigned := map[string]bool{}
	for _, is := range issues {
		for _, f := range issueCauses(is) {
			assigned[f] = false
		}
	}
	for _, r := range report.Results {
		if _, known := assigned[r.Cluster]; !known {
			continue // an id the agent made up must not become a cause
		}
		assigned[r.Cluster] = true
		o.updateCause(st.si, r.Cluster, func(c *Cause) {
			c.History = append(c.History, clipRunes(strings.TrimSpace(r.Status+": "+r.RootCause+" "+r.Reason), 300))
			c.Proof = cmp.Or(r.Proof, c.Proof)
			switch {
			case r.Status == "blocked":
				c.Status, c.Reason = causeBlocked, r.Reason
			case r.Status == "reassign" && !c.Reassigned && isImplementer(o.stages, r.Owner):
				c.Owner, c.Reassigned = r.Owner, true
				c.Attempts--
			}
		})
	}
	for f, reported := range assigned {
		if !reported {
			o.updateCause(st.si, f, func(c *Cause) { c.History = append(c.History, clipRunes(strings.TrimSpace(output), 300)) })
		}
	}
}

func buildInvestigationPrompt(task string, sc storyContext, discovery *workspace.Report, plan string, issues []Issue, causes map[string]Cause, changed []string) string {
	var b strings.Builder
	writeContext(&b, sc, discovery)
	if plan != "" {
		fmt.Fprintf(&b, "## Plano do arquiteto\n\n%s\n\n", clip(plan, maxArtifactBytes))
	}
	b.WriteString("## Causas a investigar\n\n")
	writeCauses(&b, issues, causes)
	if len(changed) > 0 {
		fmt.Fprintf(&b, "## Arquivos alterados nesta US\n\n%s\n\n", strings.Join(changed[:min(len(changed), maxBriefCauses*3)], "\n"))
	}
	writeSkills(&b, fixSkills)
	b.WriteString("## Sua tarefa\n\n")
	b.WriteString(task)
	b.WriteString(investigationInstructions)
	return b.String()
}

// writeCauses lists each cause once, with its id, evidence and past attempts.
func writeCauses(b *strings.Builder, issues []Issue, causes map[string]Cause) {
	listed := 0
	for _, is := range issues {
		for i, f := range issueCauses(is) {
			if listed == maxBriefCauses {
				b.WriteString("[… demais causas omitidas]\n\n")
				return
			}
			listed++
			c := causes[f]
			fmt.Fprintf(b, "### Causa `%s`: %s\n\n", f, cmp.Or(c.Title, issueTitle(is)))
			if i == 0 {
				fmt.Fprintf(b, "%s\n\n", strings.TrimSpace(is.Description))
			}
			writeHistory(b, c)
		}
	}
}

func writeHistory(b *strings.Builder, c Cause) {
	if len(c.History) == 0 {
		return
	}
	b.WriteString("Tentativas anteriores (não repita; a falha continuou):\n")
	for i, h := range c.History {
		fmt.Fprintf(b, "%d. %s\n", i+1, h)
	}
	b.WriteString("\n")
}

// repairBrief is what the investigation concluded about the issues of a fix.
func repairBrief(issues []Issue, causes map[string]Cause) string {
	var b strings.Builder
	for _, is := range issues {
		for _, f := range issueCauses(is) {
			c := causes[f]
			if c.RootCause == "" && len(c.History) == 0 {
				continue
			}
			fmt.Fprintf(&b, "### Causa `%s`: %s\n\n", f, c.Title)
			writeFinding(&b, c)
			writeHistory(&b, c)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "## Dossiê da investigação\n\n" + b.String()
}

func writeFinding(b *strings.Builder, c Cause) {
	for _, field := range []struct{ label, value string }{
		{"Causa raiz", c.RootCause}, {"Evidência", c.Evidence}, {"Estratégia", strings.Join(c.Strategy, "; ")},
		{"Teste de regressão", c.Regression}, {"Risco", c.Risk},
	} {
		if field.value != "" {
			fmt.Fprintf(b, "- %s: %s\n", field.label, field.value)
		}
	}
	b.WriteString("\n")
}

// causeIDs lists the ids an agent must answer about.
func causeIDs(issues []Issue) string {
	var ids []string
	for _, is := range issues {
		ids = append(ids, issueCauses(is)...)
	}
	return "`" + strings.Join(ids, "`, `") + "`"
}
