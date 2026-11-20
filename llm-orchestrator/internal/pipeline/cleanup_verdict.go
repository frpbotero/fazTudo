package pipeline

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/quality"
)

// takeSnapshot runs every project command, without stopping at the first
// failing layer: the baseline must know each command's own failures. It
// returns false when ctx was canceled.
func (o *Orchestrator) takeSnapshot(ctx context.Context, ss *session) ([]CheckSnapshot, []quality.Result, bool) {
	timeout := time.Duration(ss.policy.CheckTimeoutS) * time.Second
	var snaps []CheckSnapshot
	var results []quality.Result
	for _, c := range ss.checks {
		r := o.runGated(ctx, ss, ss.repo, c, timeout)
		if ctx.Err() != nil {
			return nil, nil, false
		}
		results = append(results, r)
		snaps = append(snaps, snapshotOf(c, r))
	}
	return snaps, results, true
}

// snapshotOf keeps what tells an old failure from a new one. A failure no
// parser recognised is identified by the command itself.
func snapshotOf(c quality.Command, r quality.Result) CheckSnapshot {
	s := CheckSnapshot{Name: c.Name, Passed: r.Passed, TimedOut: r.TimedOut}
	if r.Passed {
		return s
	}
	s.Fingerprints = quality.Fingerprints(r.Failures)
	if len(s.Fingerprints) == 0 {
		s.Fingerprints = []string{quality.TextFingerprint(c.Name, c.Run)}
	}
	return s
}

// compareSnapshots lists what got worse from before to after: a command that
// passed and now fails or times out, or a failure that did not exist.
func compareSnapshots(before, after []CheckSnapshot) []string {
	var reasons []string
	for _, a := range after {
		i := slices.IndexFunc(before, func(b CheckSnapshot) bool { return b.Name == a.Name })
		if a.Passed {
			continue
		}
		if i < 0 || before[i].Passed {
			what := "passou a falhar"
			if a.TimedOut {
				what = "passou a estourar o tempo"
			}
			reasons = append(reasons, fmt.Sprintf("Comando %s %s.", a.Name, what))
			continue
		}
		b := before[i]
		if a.TimedOut && !b.TimedOut {
			reasons = append(reasons, fmt.Sprintf("Comando %s passou a estourar o tempo.", a.Name))
			continue
		}
		if n := countNew(b.Fingerprints, a.Fingerprints); n > 0 && !a.TimedOut {
			reasons = append(reasons, fmt.Sprintf("Comando %s tem %d falha(s) nova(s) em relação à linha de base.", a.Name, n))
		}
	}
	return reasons
}

func countNew(before, after []string) int {
	n := 0
	for _, f := range after {
		if !slices.Contains(before, f) {
			n++
		}
	}
	return n
}

// outsideArea lists the changed files that fall outside area ("." covers
// the whole repository).
func outsideArea(area string, changed []string) []string {
	if area == "." {
		return nil
	}
	prefix := strings.TrimSuffix(area, "/") + "/"
	var out []string
	for _, f := range changed {
		if !strings.HasPrefix(f, prefix) {
			out = append(out, f)
		}
	}
	return out
}

// missingIDs lists the acceptance-criterion IDs that disappeared from tests.
func missingIDs(before, after []string) []string {
	var out []string
	for _, id := range before {
		if !slices.Contains(after, id) {
			out = append(out, id)
		}
	}
	return out
}

// cleanupReasons gathers every rule a cleanup broke; none means approved.
func cleanupReasons(area string, changed []string, idsBefore, idsAfter []string, before, after []CheckSnapshot) []string {
	var reasons []string
	if out := outsideArea(area, changed); len(out) > 0 {
		reasons = append(reasons, fmt.Sprintf("Arquivos alterados fora da área %s: %s.", area, strings.Join(clipList(out, 10), ", ")))
	}
	if gone := missingIDs(idsBefore, idsAfter); len(gone) > 0 {
		reasons = append(reasons, fmt.Sprintf("Testes de critério de aceite removidos: %s.", strings.Join(clipList(gone, 10), ", ")))
	}
	return append(reasons, compareSnapshots(before, after)...)
}

// clipList keeps the first n items and says how many were left out.
func clipList(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	return append(slices.Clone(items[:n]), fmt.Sprintf("e mais %d", len(items)-n))
}
