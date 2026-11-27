package pipeline

import (
	"cmp"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/vcs"
)

// Delivery is where a run's delivery package was written: one report per
// story and one per integrated set, with an index.
type Delivery struct {
	Dir   string    `json:"dir"`
	Files []string  `json:"files"`
	At    time.Time `json:"at"`
}

// deliveryRetention is how many packages a repository keeps by default.
const deliveryRetention = 10

// maxDeliveryDiff caps the diff kept per story.
const maxDeliveryDiff = 2 << 20

// deliveryRoot keeps packages beside the saved runs, outside the repository.
func deliveryRoot(journalDir, repo string) string {
	sum := sha1.Sum([]byte(repo))
	return filepath.Join(filepath.Dir(journalDir), "entregas", filepath.Base(repo)+"-"+hex.EncodeToString(sum[:4]))
}

// writeDelivery writes the package of a finished run: for each story its
// criteria, diff, commits, commands and results, pending issues and the
// configuration that ran it; for each promotion the integrated set. Every
// text goes through redaction, files are readable by the user only, and only
// the newest packages of the repository are kept.
func (o *Orchestrator) writeDelivery(run Run) {
	o.mu.RLock()
	dir := o.journalDir
	o.mu.RUnlock()
	if run.Repository == "" || dir == "" || run.StartedAt == nil {
		return
	}
	root := deliveryRoot(dir, run.Repository)
	out := filepath.Join(root, run.StartedAt.Format("20060102-150405"))
	if err := os.MkdirAll(out, 0o700); err != nil {
		o.logf(LevelWarn, "Pacote de entrega não gravado: %v.", err)
		return
	}
	var g *vcs.Repo
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !run.DryRun {
		g, _, _ = vcs.Open(ctx, run.Repository)
	}
	d := Delivery{Dir: out, At: time.Now()}
	save := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(out, name), data, 0o600); err == nil {
			d.Files = append(d.Files, name)
		}
	}
	write := func(name, body string) { save(name, []byte(redactSecrets(body))) }
	var index strings.Builder
	fmt.Fprintf(&index, "# Entrega: %s\n\n", filepath.Base(run.Repository))
	fmt.Fprintf(&index, "- Execução iniciada em %s; resultado %s; validação %s.\n", run.StartedAt.Format("02/01/2006 15:04"), cmp.Or(string(run.Result), "em andamento"), cmpValidation(run.Validation).Label())
	if run.DryRun {
		index.WriteString("- **Simulação**: nenhum comando nem modelo rodou; nada aqui é evidência.\n")
	}
	index.WriteString("- A evidência abaixo complementa os checks do CI; não é garantia por si só.\n\n## US\n\n")
	for i, st := range run.Stories {
		name := deliveryName(cmp.Or(st.ID, fmt.Sprintf("US-%d", i+1)))
		write(name+".md", renderStoryDelivery(run, st))
		if data, err := redactedJSON(storyEvidence(st)); err == nil {
			save(name+".json", data)
		}
		if g != nil && st.StartCommit != "" && st.Head != "" && st.StartCommit != st.Head {
			if diff, err := g.Diff(ctx, st.StartCommit, st.Head, maxDeliveryDiff); err == nil && diff != "" {
				write(name+".diff", diff)
			}
		}
		fmt.Fprintf(&index, "- [%s](%s.md): %s, validação %s, integração %s\n", cmp.Or(st.Title, st.ID), name, st.Status,
			cmpValidation(st.Validation).Label(), cmp.Or(string(st.Integration), "não integrada"))
	}
	if len(run.Promotions) > 0 {
		index.WriteString("\n## Conjuntos integrados\n\n")
		for i, p := range run.Promotions {
			name := fmt.Sprintf("conjunto-%02d", i+1)
			write(name+".md", renderPromotionDelivery(p))
			fmt.Fprintf(&index, "- [%s](%s.md): %s, %s\n", strings.Join(p.Stories, ", "), name, p.Status, cmp.Or(p.Reason, "sem ressalvas"))
		}
	}
	write("README.md", index.String())
	o.mu.Lock()
	o.run.Delivery = &d
	o.mu.Unlock()
	o.notify()
	o.pruneDeliveries(root, o.ws.Policy().DeliveryRetention)
	o.logf(LevelInfo, "Pacote de entrega gravado em %s.", out)
}

// pruneDeliveries keeps the newest keep packages of a repository.
func (o *Orchestrator) pruneDeliveries(root string, keep int) {
	if keep <= 0 {
		keep = deliveryRetention
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	slices.Sort(dirs) // names are timestamps
	for len(dirs) > keep {
		_ = os.RemoveAll(filepath.Join(root, dirs[0]))
		dirs = dirs[1:]
	}
}

var reUnsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func deliveryName(id string) string {
	return strings.Trim(reUnsafeName.ReplaceAllString(id, "-"), "-.")
}

// storyEvidence is the machine-readable part of a story's report.
type deliveryEvidence struct {
	ID           string                  `json:"id"`
	Status       StepStatus              `json:"status"`
	Validation   Validation              `json:"validation"`
	Integration  Integration             `json:"integration,omitempty"`
	Branch       string                  `json:"branch,omitempty"`
	StartCommit  string                  `json:"start_commit,omitempty"`
	Head         string                  `json:"head,omitempty"`
	BuiltOn      map[string]string       `json:"built_on,omitempty"`
	Change       string                  `json:"change,omitempty"`
	Risk         any                     `json:"risk,omitempty"`
	Trace        []quality.Trace         `json:"trace,omitempty"`
	Acceptance   []ScenarioResult        `json:"acceptance,omitempty"`
	Reproduction *Reproduction           `json:"reproduction,omitempty"`
	Checks       []quality.Result        `json:"checks,omitempty"`
	Pending      []Issue                 `json:"pending,omitempty"`
	Calls        map[string][]CallRecord `json:"calls,omitempty"`
}

func storyEvidence(st StoryRun) deliveryEvidence {
	e := deliveryEvidence{
		ID: st.ID, Status: st.Status, Validation: cmpValidation(st.Validation), Integration: st.Integration,
		Branch: st.Branch, StartCommit: st.StartCommit, Head: st.Head, BuiltOn: st.BuiltOn, Change: st.Change,
		Trace: st.Trace, Acceptance: st.Acceptance, Reproduction: st.Reproduction, Checks: lastChecks(st.Steps),
		Pending: pendingIssues(st.Steps), Calls: map[string][]CallRecord{},
	}
	if st.Risk != nil {
		e.Risk = st.Risk
	}
	for _, s := range st.Steps {
		if len(s.Calls) > 0 {
			e.Calls[s.ID] = s.Calls
		}
	}
	return e
}

// lastChecks are the project commands of the story's last verification.
func lastChecks(steps []Step) []quality.Result {
	for i := len(steps) - 1; i >= 0; i-- {
		if len(steps[i].Checks) > 0 {
			return steps[i].Checks
		}
	}
	return nil
}

// pendingIssues are what the story's gates could not get fixed.
func pendingIssues(steps []Step) []Issue {
	var out []Issue
	for _, s := range steps {
		if s.Status == StepWarning || s.Status == StepFailed {
			out = append(out, s.Issues...)
		}
	}
	return out
}

func renderStoryDelivery(run Run, st StoryRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", cmp.Or(st.Title, st.ID))
	fmt.Fprintf(&b, "| | |\n|---|---|\n| Status | %s |\n| Validação | %s |\n| Integração | %s %s |\n", st.Status,
		cmpValidation(st.Validation).Label(), cmp.Or(string(st.Integration), "não integrada"), st.IntegrationNote)
	if st.Change != "" {
		fmt.Fprintf(&b, "| Tipo de mudança | %s %s |\n", changeLabel(st.Change), st.ChangeReason)
	}
	if st.Risk != nil {
		fmt.Fprintf(&b, "| Risco | %s (%s) |\n", st.Risk.Level, strings.Join(st.Risk.Reasons, "; "))
	}
	fmt.Fprintf(&b, "| Branch | `%s` |\n| Commit de partida | `%s` |\n| Último commit | `%s` |\n", st.Branch, st.StartCommit, st.Head)
	for dep, sha := range st.BuiltOn {
		fmt.Fprintf(&b, "| Construída sobre %s | `%s` |\n", dep, sha)
	}

	if r := st.Readiness; r != nil && len(r.Criteria) > 0 {
		b.WriteString("\n## Critérios (Passo 0)\n\n")
		for _, c := range r.Criteria {
			fmt.Fprintf(&b, "- %s — %s %s\n", c.Text, c.Status, c.Evidence)
		}
	}
	if len(st.Trace) > 0 {
		b.WriteString("\n## Casos de teste da especificação\n\n| ID | Status | Casos executados |\n|---|---|---|\n")
		for _, t := range st.Trace {
			var cases []string
			for _, c := range t.Cases {
				cases = append(cases, fmt.Sprintf("%s (%s, %s)", c.Name, c.Status, c.Command))
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", t.ID, t.Status, strings.Join(cases, "; "))
		}
	}
	if len(st.Acceptance) > 0 {
		b.WriteString("\n## Aceitação funcional\n\n")
		for _, a := range st.Acceptance {
			fmt.Fprintf(&b, "- **%s** %s: %s — %s %s\n", a.Status, a.Criterion, a.Scenario, strings.Join(a.Tests, ", "), a.Evidence)
		}
	}
	if r := st.Reproduction; r != nil {
		fmt.Fprintf(&b, "\n## Reprodução do bug\n\n%s no commit `%s`. %s\n", r.Status, r.Commit, r.Detail)
	}
	if checks := lastChecks(st.Steps); len(checks) > 0 {
		b.WriteString("\n## Comandos (última verificação)\n\n")
		for _, c := range checks {
			status := "passou"
			if !c.Passed {
				status = exitLabel(c)
			}
			fmt.Fprintf(&b, "- `%s` (%s): %s em %s\n", c.Command, c.Name, status, time.Duration(c.DurationMS)*time.Millisecond)
		}
	}
	if pending := pendingIssues(st.Steps); len(pending) > 0 {
		b.WriteString("\n## Pendências\n\n")
		for _, is := range pending {
			fmt.Fprintf(&b, "- [%s] %s — %s %s\n", is.Severity, is.Agent, is.Description, is.Location)
		}
	}
	b.WriteString("\n## Etapas e configuração efetiva\n\n| Etapa | Status | Validação | Provider/modelo | Observação |\n|---|---|---|---|---|\n")
	for _, s := range st.Steps {
		model := strings.Trim(s.Provider+" "+s.Model, " ")
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", s.Title, s.Status, s.Validation, model, oneLine(cmp.Or(s.Error, s.Note)))
	}
	if m := run.Manifest; m != nil {
		fmt.Fprintf(&b, "\nManifesto: app %s, %s, skills `%s`, política %s.\n", cmp.Or(m.AppVersion, "dev"), m.Env, m.Bundle, short(m.Policy["other"]))
		for name, v := range m.Providers {
			fmt.Fprintf(&b, "- %s %s\n", name, v)
		}
	}
	b.WriteString("\nDiff completo no arquivo `.diff` ao lado (quando houve mudança).\n")
	return b.String()
}

func renderPromotionDelivery(p Promotion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Conjunto: %s\n\n- Branch: `%s`\n- Commit avaliado: `%s`\n- Resultado: %s %s\n", strings.Join(p.Stories, ", "), p.Branch, p.Commit, p.Status, p.Reason)
	if len(p.Repos) > 0 {
		b.WriteString("\n| Repositório | Base | Candidato | Antes | Depois | Status |\n|---|---|---|---|---|---|\n")
		for _, r := range p.Repos {
			fmt.Fprintf(&b, "| %s | %s | `%s` | `%s` | `%s` | %s |\n", r.Path, r.Base, short(r.Candidate), short(r.Before), short(r.After), r.Status)
		}
	}
	if len(p.Checks) > 0 {
		b.WriteString("\n## Comandos no conjunto integrado\n\n")
		for _, c := range p.Checks {
			status := "passou"
			if !c.Passed {
				status = exitLabel(c)
			}
			fmt.Fprintf(&b, "- `%s` (%s): %s\n", c.Command, c.Name, status)
		}
	}
	return b.String()
}

func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(clipRunes(s, 160), "\n", " "), "|", "/")
}

// Credentials a report could quote from a command's output or an agent's.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr|github_pat)_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)(://[^/\s:@]+:)[^@\s/]+@`),
}

// redactSecrets removes credentials, then the personal data redact covers.
func redactSecrets(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			if sub := re.FindStringSubmatch(m); len(sub) > 1 {
				return sub[1] + "[removido]@"
			}
			return "[segredo removido]"
		})
	}
	return redact(s)
}

// redactedJSON encodes v with every string value redacted: redacting the
// encoded text could cut through its quotes.
func redactedJSON(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var tree any
	if err := json.Unmarshal(data, &tree); err != nil {
		return nil, err
	}
	return json.MarshalIndent(redactTree(tree), "", "  ")
}

func redactTree(v any) any {
	switch x := v.(type) {
	case string:
		return redactSecrets(x)
	case []any:
		for i := range x {
			x[i] = redactTree(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = redactTree(x[k])
		}
	}
	return v
}
