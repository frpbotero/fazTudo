package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/workspace"
)

// Issue categories a reviewer may give; style is advice unless the project
// makes it binding.
const (
	CategoryStyle = "estilo"
)

// isStyle reports whether a reviewer filed the issue as style or naming.
func isStyle(is Issue) bool {
	c := strings.ToLower(strings.TrimSpace(is.Category))
	return c == CategoryStyle || c == "style" || c == "nomenclatura" || c == "legibilidade"
}

// proportional keeps the review's demands to what blocks a delivery: style
// issues become advice, unless the policy makes the project's style binding.
// A verdict left with nothing to fix approves.
func proportional(v Verdict, policy workspace.Policy) (Verdict, []Issue) {
	if policy.StyleBlocking {
		return v, nil
	}
	var advice []Issue
	v.Issues = slices.DeleteFunc(slices.Clone(v.Issues), func(is Issue) bool {
		if isStyle(is) && !is.Blocking() {
			advice = append(advice, is)
			return true
		}
		return false
	})
	if v.Status == VerdictChangesRequested && len(v.Issues) == 0 {
		v.Status = VerdictApproved
	}
	return v, advice
}

var reFileLine = regexp.MustCompile(`([\w./@-]+\.[A-Za-z0-9]{1,6})(?::(\d+))?`)

// lineWindow is how far apart two findings on the same file may be and still
// be one: reviewers cite the start of a function or the exact line.
const lineWindow = 3

// consolidateIssues merges the findings several reviewers made about the
// same cause: same implementer, same file and nearby lines, or the same
// description. The merged issue keeps the highest severity, every location
// and who raised it.
func consolidateIssues(issues []Issue) []Issue {
	var out []Issue
	var members [][]Issue // the issues merged into each one, as reported
	for _, is := range issues {
		i := slices.IndexFunc(members, func(group []Issue) bool {
			return slices.ContainsFunc(group, func(o Issue) bool { return sameCause(o, is) })
		})
		if i < 0 {
			out = append(out, is)
			members = append(members, []Issue{is})
			continue
		}
		members[i] = append(members[i], is)
		m := &out[i]
		if severityRank(is.Severity) > severityRank(m.Severity) {
			m.Severity = is.Severity
		}
		for _, s := range is.Sources {
			if !slices.Contains(m.Sources, s) {
				m.Sources = append(m.Sources, s)
			}
		}
		if is.Location != "" && is.Location != m.Location && !strings.Contains(m.Location, is.Location) {
			m.Location = strings.TrimPrefix(m.Location+"; "+is.Location, "; ")
		}
		said := slices.ContainsFunc(members[i][:len(members[i])-1], func(o Issue) bool {
			return normalizeQuestion(o.Description) == normalizeQuestion(is.Description)
		})
		if !said {
			m.Description += " | " + is.Description
		}
		if isStyle(*m) && !isStyle(is) {
			m.Category = is.Category // one substantive report makes the cause substantive
		}
	}
	return out
}

func sameCause(a, b Issue) bool {
	if a.Agent != b.Agent {
		return false
	}
	if normalizeQuestion(a.Description) == normalizeQuestion(b.Description) {
		return true
	}
	fa, la, oka := fileLine(a.Location)
	fb, lb, okb := fileLine(b.Location)
	if !oka || !okb || fa != fb || la == 0 || lb == 0 {
		return false
	}
	d := la - lb
	return d >= -lineWindow && d <= lineWindow
}

func fileLine(loc string) (string, int, bool) {
	m := reFileLine.FindStringSubmatch(loc)
	if m == nil {
		return "", 0, false
	}
	line, _ := strconv.Atoi(m[2])
	return strings.TrimPrefix(m[1], "./"), line, true
}

func severityRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "crítica", "critica", "crítico", "critico", "critical", "blocker", "bloqueante":
		return 4
	case "alta", "alto", "high":
		return 3
	case "média", "media", "medium":
		return 2
	case "baixa", "baixo", "low":
		return 1
	}
	return 0
}

// undoReviewerEdits takes back what a read-only agent changed: reviewers
// point at problems, the implementers fix them. The CLI's read-only mode
// should prevent it; this is the check that it did.
func (o *Orchestrator) undoReviewerEdits(ctx context.Context, ss *session, ref stepRef) {
	at := o.stepAt(ref)
	if at.Kind == "fix" {
		// A fix step names its gate but is run by an implementer: its edits
		// are the fix, not a reviewer's overreach.
		return
	}
	stage, ok := o.lookupStage(retryStageID(at))
	if !ok || !stage.ReadOnly || stage.Diagnose || ss.dry {
		return
	}
	started := at.StartedAt
	if started == nil {
		return
	}
	since := *started
	if ss.git != nil {
		// A second of slack for coarse file clocks: git confirms below.
		since = since.Add(-time.Second)
	}
	changed := o.changedFiles(ss, since)
	if ss.git != nil {
		// Every agent step ends in a commit, so what the reviewer touched is
		// what differs from HEAD now. The clock alone also catches the
		// implementer's files committed just before the review started.
		dirty, err := ss.git.ChangedPaths(ctx)
		if err != nil {
			o.logf(LevelWarn, "%s: não foi possível conferir se o revisor alterou arquivos (%v).", stage.Title, err)
			return
		}
		changed = slices.DeleteFunc(changed, func(f string) bool { return !slices.Contains(dirty, f) })
		// A deleted file has no clock to read: git alone tells it is gone.
		protected := ss.guard.Files()
		for _, f := range dirty {
			if _, err := os.Lstat(filepath.Join(ss.repo, f)); os.IsNotExist(err) && !slices.Contains(protected, f) {
				changed = append(changed, f)
			}
		}
	}
	if len(changed) == 0 {
		return
	}
	list := strings.Join(clipList(changed, 5), ", ")
	note := "revisor alterou arquivos e, sem git, não foi possível desfazer; confira: " + list
	if ss.git != nil {
		note = "revisor alterou arquivos, desfeito: " + list
		if err := ss.git.RestoreFiles(ctx, changed); err != nil {
			note = "revisor alterou arquivos e não foi possível desfazer (" + err.Error() + "): " + list
		}
	}
	o.updateStep(ref, func(s *Step) { s.Error = strings.TrimPrefix(s.Error+"; "+note, "; ") })
	o.logf(LevelWarn, "%s: %s.", stage.Title, note)
}

func (o *Orchestrator) stepAt(ref stepRef) Step {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if s := o.stepLocked(ref); s != nil {
		return *s
	}
	return Step{}
}

// reviewLimit bounds the reviews running at once; zero is the default.
func reviewLimit(p workspace.Policy) int {
	if p.MaxParallelReviews > 0 {
		return p.MaxParallelReviews
	}
	return defaultParallelReviews
}

const defaultParallelReviews = 3

// reviewProvider is the provider the policy prefers for a gate's review.
func reviewProvider(ss *session, stage Stage) provider.ID {
	if !stage.Gate || stage.Verifier {
		return ""
	}
	return provider.ID(ss.policy.ReviewProvider)
}
