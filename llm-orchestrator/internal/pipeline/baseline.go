package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/quality"
)

// Baseline is how the project commands behaved on the story's starting
// snapshot, before any agent changed it. It tells the failures a story
// introduced from the ones it found; a failure that was already there is
// still a failure, only labeled as such.
type Baseline struct {
	// Commit is the snapshot the commands ran on; empty without git.
	Commit string    `json:"commit,omitempty"`
	At     time.Time `json:"at"`
	// Env is the platform the commands ran on.
	Env     string           `json:"env"`
	Results []quality.Result `json:"results"`
	// Failing maps each failing command to the fingerprints of its failures
	// (empty when the output could not be parsed into failures).
	Failing map[string][]string `json:"failing,omitempty"`
	// Unknown are commands with no result: out of time, or not run because
	// an earlier layer failed.
	Unknown []string `json:"unknown,omitempty"`
	// Unavailable maps a failing command to the line showing a service it
	// needs was down: the failure is the environment's, not the code's.
	Unavailable map[string]string `json:"unavailable,omitempty"`
	// Tests, TestIDs and PassedCases are what characterizes the behavior
	// before the story: its test files, the criterion IDs they name and the
	// cases that passed ("command › case"). A refactoring keeps them all.
	Tests       []string `json:"tests,omitempty"`
	TestIDs     []string `json:"test_ids,omitempty"`
	PassedCases []string `json:"passed_cases,omitempty"`
}

// maxBaselineOutput keeps a baseline small in the journal.
const maxBaselineOutput = 600

// failureOrigin labels a failing command against the baseline: "preexistente"
// when every failure was already there, "nova" when the command passed or
// fails differently now, "" without a baseline result for the command.
func (b *Baseline) failureOrigin(command string, fingerprints []string) string {
	if b == nil {
		return ""
	}
	if slices.Contains(b.Unknown, command) {
		return ""
	}
	before, failed := b.Failing[command]
	if !failed {
		if slices.ContainsFunc(b.Results, func(r quality.Result) bool { return r.Name == command }) {
			return originNew
		}
		return ""
	}
	if len(before) == 0 || len(fingerprints) == 0 {
		return originPreexisting // the command failed before; no finer comparison
	}
	for _, f := range fingerprints {
		if !slices.Contains(before, f) {
			return originNew
		}
	}
	return originPreexisting
}

const (
	originPreexisting = "preexistente"
	originNew         = "nova"
)

// takeBaseline runs the project commands on the snapshot the story starts
// from. Stories starting from the same commit with the same commands share
// one baseline; without git it always runs. It returns false when ctx was
// canceled.
func (o *Orchestrator) takeBaseline(ctx context.Context, ss *session, st *storyState) bool {
	if ss.dry || ss.policy.SkipBaseline || len(ss.checks) == 0 {
		return true
	}
	commit := ""
	if ss.git != nil {
		commit, _ = ss.git.Head(ctx)
	}
	key := baselineKey(commit, ss.checks)
	if commit != "" {
		// The stories of a wave start together from one commit: the first
		// takes the baseline, the others wait for it instead of repeating it.
		o.baselineMu.Lock()
		defer o.baselineMu.Unlock()
		if v, ok := o.baselines.Load(key); ok {
			b := v.(*Baseline)
			// The environment may be up by now: a baseline the environment
			// failed proves nothing about this commit, so it runs again.
			if len(b.Unavailable) == 0 {
				o.setBaseline(st, b)
				o.logf(LevelInfo, "Linha de base de %s reutilizada (mesmo commit e comandos).", st.sc.Story.Title)
				return true
			}
			o.baselines.Delete(key)
			o.logf(LevelInfo, "Linha de base anterior de %s encontrou o ambiente indisponível; rodando de novo.", st.sc.Story.Title)
		}
	}
	o.logf(LevelInfo, "Linha de base de %s: rodando os comandos do projeto antes de qualquer mudança.", st.sc.Story.Title)
	b := &Baseline{
		Commit: commit, At: time.Now(), Env: runtime.GOOS + "/" + runtime.GOARCH, Failing: map[string][]string{},
		Tests: quality.TestFiles(ss.repo), TestIDs: quality.TestIDs(ss.repo),
	}
	timeout := time.Duration(ss.policy.CheckTimeoutS) * time.Second
	failedLayer := -1
	for _, c := range quality.InLayers(ss.checks) {
		if failedLayer >= 0 && quality.Layer(c) > failedLayer {
			b.Unknown = append(b.Unknown, c.Name)
			continue
		}
		r := o.runGated(ctx, ss, ss.repo, c, timeout)
		if ctx.Err() != nil {
			return false
		}
		switch {
		case r.TimedOut:
			b.Unknown = append(b.Unknown, c.Name)
		case !r.Passed:
			failedLayer = quality.Layer(c)
			failures := r.Failures
			if len(failures) == 0 {
				failures = quality.ParseFailures(c, failureText(r))
			}
			b.Failing[c.Name] = quality.Fingerprints(failures)
			parsed := r
			parsed.Failures = failures
			if why := quality.Unreachable(parsed); why != "" {
				if b.Unavailable == nil {
					b.Unavailable = map[string]string{}
				}
				b.Unavailable[c.Name] = why
			}
		}
		r.Output = tail(r.Output, maxBaselineOutput)
		for _, c := range r.Cases {
			if c.Status == quality.CasePassed && len(b.PassedCases) < maxBaselineCases {
				b.PassedCases = append(b.PassedCases, caseKey(r.Name, c.Name))
			}
		}
		r.Cases = nil // the passing cases are kept above; the counts stay
		b.Results = append(b.Results, r)
	}
	if commit != "" {
		o.baselines.Store(key, b)
	}
	o.setBaseline(st, b)
	o.warnBaseline(b)
	return true
}

func (o *Orchestrator) setBaseline(st *storyState, b *Baseline) {
	st.baseline = b
	o.updateStory(st.si, func(r *StoryRun) { r.Baseline = b })
}

func baselineKey(commit string, checks []quality.Command) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", commit)
	for _, c := range checks {
		fmt.Fprintf(h, "%s\x00%s\x00", c.Name, c.Run)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// warnBaseline says what already failed before the story and, apart, what
// failed only because the environment is down, which a person must fix.
func (o *Orchestrator) warnBaseline(b *Baseline) {
	if len(b.Failing) > 0 {
		o.logf(LevelWarn, "Antes da US já falhavam: %s. Essas falhas aparecem como preexistentes e continuam bloqueando.", strings.Join(sortedKeys(b.Failing), ", "))
	}
	for _, name := range sortedKeys(b.Unavailable) {
		o.logf(LevelWarn, "Ambiente indisponível na linha de base: %s falhou porque um serviço não respondeu (%s). Suba o banco ou o serviço e rode de novo; essas falhas não serão enviadas aos agentes.", name, b.Unavailable[name])
	}
}
