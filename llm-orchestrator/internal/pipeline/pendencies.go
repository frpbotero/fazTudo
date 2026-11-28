package pipeline

import (
	"bytes"
	"cmp"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// renderPendencies lists everything the run left unresolved: issues a gate
// could not get fixed, failed or canceled steps, and stories that were not
// delivered. It returns "" when there is nothing pending.
func renderPendencies(run Run) string {
	var b bytes.Buffer
	count := 0
	section := func(title string) { fmt.Fprintf(&b, "\n## %s\n", title) }
	var pendingCauses map[string]bool
	covered := func(is Issue) bool {
		fps := issueCauses(is)
		return len(fps) > 0 && !slices.ContainsFunc(fps, func(f string) bool { return !pendingCauses[f] })
	}

	addStep := func(prefix string, s Step) {
		switch s.Status {
		case StepWarning, StepFailed, StepCanceled:
		default:
			return
		}
		count++
		fmt.Fprintf(&b, "\n### %s%s (%s)\n", prefix, s.Title, s.Status)
		if s.Error != "" {
			fmt.Fprintf(&b, "\n%s\n", s.Error)
		}
		for _, is := range s.Issues {
			if covered(is) {
				continue // listed once, grouped, under Causas
			}
			fmt.Fprintf(&b, "- %s%s%s\n", severityTag(is.Severity), is.Description, locationSuffix(is.Location))
		}
		for _, f := range s.Findings {
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			fmt.Fprintf(&b, "- [%s] %s (`%s`)\n", f.Check, f.Message, loc)
		}
		for _, c := range s.Checks {
			if !c.Passed {
				fmt.Fprintf(&b, "- Verificação `%s` falhou\n", c.Name)
			}
		}
	}

	for _, st := range run.Stories {
		before := b.Len()
		pendingCauses = map[string]bool{}
		for _, c := range st.Causes {
			if _, pending := causeLabels[c.Status]; pending {
				pendingCauses[c.Fingerprint] = true
			}
		}
		section(fmt.Sprintf("%s — %s", cmp.Or(st.ID, st.Title), st.Title))
		header := b.Len()
		for _, s := range st.Steps {
			addStep("", s)
		}
		count += writePendingCauses(&b, st.Causes)
		if st.Integration == IntegrationBlocked {
			count++
			fmt.Fprintf(&b, "\nFora da branch base: %s", cmp.Or(st.IntegrationNote, "promoção bloqueada"))
			if st.Branch != "" {
				fmt.Fprintf(&b, " (branch `%s` mantida para revisão)", st.Branch)
			}
			b.WriteString("\n")
		}
		if reason := notDelivered(st); reason != "" {
			if b.Len() == header {
				count++
			}
			fmt.Fprintf(&b, "\nHistória não entregue (%s): %s\n", st.Status, reason)
		}
		if b.Len() == header {
			b.Truncate(before)
		}
	}
	for _, s := range []*Step{&run.Discovery, run.Analysis} {
		if s != nil {
			addStep("Execução: ", *s)
		}
	}
	if count == 0 {
		return ""
	}
	var head strings.Builder
	fmt.Fprintf(&head, "# Pendências da execução\n\n")
	fmt.Fprintf(&head, "Resultado: **%s**", cmp.Or(string(run.Result), "indefinido"))
	if run.Validation != "" {
		fmt.Fprintf(&head, " · validação **%s**", run.Validation.Label())
	}
	if run.FinishedAt != nil {
		fmt.Fprintf(&head, " · encerrada em %s", run.FinishedAt.Format("02/01/2006 15:04"))
	}
	fmt.Fprintf(&head, " · %d item(ns) não resolvido(s)\n", count)
	return head.String() + b.String()
}

// causeLabels name what kept a cause open, for whoever picks it up.
var causeLabels = map[string]string{
	causePending:    "persistiu após as tentativas",
	causeBlocked:    "bloqueada",
	causeInherited:  "herdada de US anterior",
	causeInfra:      "infraestrutura",
	causeUnverified: "não reverificada",
}

// causeGroup is the pending causes of one source with one status: the many
// tests a database outage fails are one item to act on, not one each.
type causeGroup struct {
	source, status string
	causes         []Cause
}

// pendingCauseGroups groups the unresolved causes by source and status, in
// the order they first appeared. A cause without a source stands alone.
func pendingCauseGroups(causes []Cause) []causeGroup {
	var groups []causeGroup
	for _, c := range causes {
		if _, pending := causeLabels[c.Status]; !pending {
			continue
		}
		i := -1
		if c.Source != "" {
			i = slices.IndexFunc(groups, func(g causeGroup) bool { return g.source == c.Source && g.status == c.Status })
		}
		if i < 0 {
			groups = append(groups, causeGroup{source: c.Source, status: c.Status})
			i = len(groups) - 1
		}
		groups[i].causes = append(groups[i].causes, c)
	}
	return groups
}

// pendingCount is the story's unresolved items, the one number the log, the
// metrics and the reports give: a group of causes counts once.
func pendingCount(st StoryRun) int {
	if n := len(pendingCauseGroups(st.Causes)); n > 0 {
		return n
	}
	return len(pendingIssues(st.Steps))
}

// maxGroupTitles bounds how many failures one group names.
const maxGroupTitles = 8

// writePendingCauses hands over what the investigation learned, so nobody
// starts the diagnosis again. It returns how many items it listed.
func writePendingCauses(b *bytes.Buffer, causes []Cause) int {
	groups := pendingCauseGroups(causes)
	if len(groups) > 0 {
		b.WriteString("\n### Causas\n")
	}
	for _, g := range groups {
		label := causeLabels[g.status]
		first := g.causes[0]
		if len(g.causes) == 1 {
			fmt.Fprintf(b, "\n- **%s** (%s, `%s`)\n", first.Title, label, first.Fingerprint)
		} else {
			fmt.Fprintf(b, "\n- **%s: %d falha(s)** (%s)\n", g.source, len(g.causes), label)
			var titles []string
			for _, c := range g.causes {
				titles = append(titles, c.Title)
			}
			fmt.Fprintf(b, "  - Casos: %s\n", strings.Join(clipList(titles, maxGroupTitles), "; "))
		}
		// What one investigation learned holds for the group: show the first
		// value of each field, not one copy per failure.
		for _, field := range []struct {
			name  string
			value func(Cause) string
		}{
			{"Causa raiz", func(c Cause) string { return c.RootCause }},
			{"Evidência", func(c Cause) string { return c.Evidence }},
			{"Estratégia", func(c Cause) string { return strings.Join(c.Strategy, "; ") }},
			{"Teste de regressão", func(c Cause) string { return c.Regression }},
			{"Motivo", func(c Cause) string { return c.Reason }},
		} {
			for _, c := range g.causes {
				if v := field.value(c); v != "" {
					fmt.Fprintf(b, "  - %s: %s\n", field.name, v)
					break
				}
			}
		}
	}
	return len(groups)
}

// notDelivered explains why a story was left undelivered: blocked in Passo 0,
// failed or canceled. A story already met by the existing code is not pending.
func notDelivered(st StoryRun) string {
	switch st.Status {
	case StepFailed, StepCanceled:
		return "execução interrompida antes de concluir"
	case StepWaiting:
		if st.Readiness != nil {
			var qs []string
			for _, d := range st.Readiness.Decisions {
				qs = append(qs, d.Question)
			}
			return "aguarda decisão de produto: " + strings.Join(qs, " · ")
		}
		return "aguarda decisão de produto"
	case StepSkipped:
		for _, s := range st.Steps {
			if strings.Contains(s.Note, "bloqueada") {
				return s.Note
			}
		}
	}
	return ""
}

func severityTag(s string) string {
	if s == "" {
		return ""
	}
	return "**" + s + "** "
}

func locationSuffix(loc string) string {
	if loc == "" {
		return ""
	}
	return " (`" + loc + "`)"
}

// pendenciesPath keeps the artifact beside the saved runs, never inside the
// repository: an untracked file there would be taken for the user's own
// pending changes by the next run.
func pendenciesPath(journalDir, repo string) string {
	sum := sha1.Sum([]byte(repo))
	name := filepath.Base(repo) + "-" + hex.EncodeToString(sum[:4]) + ".md"
	return filepath.Join(filepath.Dir(journalDir), "pendencias", name)
}

// writePendencies saves the artifact after a real run. The file of an earlier
// run is removed once nothing is pending anymore.
func (o *Orchestrator) writePendencies(run Run) {
	o.mu.RLock()
	dir := o.journalDir
	o.mu.RUnlock()
	if run.DryRun || run.Repository == "" || dir == "" {
		return
	}
	file := pendenciesPath(dir, run.Repository)
	body := renderPendencies(run)
	if body == "" {
		_ = os.Remove(file)
		return
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err == nil {
		err = os.WriteFile(file, []byte(body), 0o644)
		if err == nil {
			o.logf(LevelWarn, "Pendências gravadas em %s.", file)
			return
		}
	}
	o.logf(LevelWarn, "Não foi possível gravar as pendências em %s.", file)
}
