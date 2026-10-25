package pipeline

import (
	"bytes"
	"cmp"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// renderPendencies lists everything the run left unresolved: issues a gate
// could not get fixed, failed or canceled steps, and stories that were not
// delivered. It returns "" when there is nothing pending.
func renderPendencies(run Run) string {
	var b bytes.Buffer
	count := 0
	section := func(title string) { fmt.Fprintf(&b, "\n## %s\n", title) }

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
		section(fmt.Sprintf("%s — %s", cmp.Or(st.ID, st.Title), st.Title))
		header := b.Len()
		for _, s := range st.Steps {
			addStep("", s)
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
	if run.FinishedAt != nil {
		fmt.Fprintf(&head, " · encerrada em %s", run.FinishedAt.Format("02/01/2006 15:04"))
	}
	fmt.Fprintf(&head, " · %d item(ns) não resolvido(s)\n", count)
	return head.String() + b.String()
}

// notDelivered explains why a story was left undelivered: blocked in Passo 0,
// failed or canceled. A story already met by the existing code is not pending.
func notDelivered(st StoryRun) string {
	switch st.Status {
	case StepFailed, StepCanceled:
		return "execução interrompida antes de concluir"
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
