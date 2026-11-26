package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/quality"
)

// Change kinds: what a story does to the code decides what proves it.
const (
	ChangeBug      = "bug"      // a test must fail on the code before the fix
	ChangeFeature  = "feature"  // new behavior needs its scenarios tested
	ChangeRefactor = "refactor" // the tests that passed before still pass
	ChangeTrivial  = "trivial"  // no behavior change: no new test, with a reason
)

var changeKinds = []string{ChangeBug, ChangeFeature, ChangeRefactor, ChangeTrivial}

// changeKind settles the story's kind from Passo 0's answer, or from the
// story's words when Passo 0 gave none. A trivial change needs a reason:
// without one it is a feature, which asks for tests.
func changeKind(kind, reason, storyText string) (string, string) {
	k := strings.ToLower(strings.TrimSpace(kind))
	switch k {
	case "fix", "correção", "correcao", "defeito":
		k = ChangeBug
	case "funcionalidade", "new", "novo":
		k = ChangeFeature
	case "refatoração", "refatoracao", "refactoring":
		k = ChangeRefactor
	}
	if !slices.Contains(changeKinds, k) {
		k = inferChange(storyText)
	}
	if k == ChangeTrivial && strings.TrimSpace(reason) == "" {
		return ChangeFeature, "trivial sem justificativa tratada como funcionalidade"
	}
	return k, ""
}

func inferChange(text string) string {
	t := strings.ToLower(text)
	switch {
	case containsAny(t, "bug", "corrigir", "correção", "defeito", "erro ao", "falha ao", "regressão", "não funciona", "quebrad"):
		return ChangeBug
	case containsAny(t, "refator", "refactor"):
		return ChangeRefactor
	}
	return ChangeFeature
}

func changeLabel(kind string) string {
	switch kind {
	case ChangeBug:
		return "correção de bug"
	case ChangeRefactor:
		return "refatoração"
	case ChangeTrivial:
		return "edição trivial"
	}
	return "funcionalidade"
}

// setChange records the story's kind.
func (o *Orchestrator) setChange(st *storyState, kind, reason string) {
	st.change, st.changeReason = kind, reason
	o.updateStory(st.si, func(r *StoryRun) { r.Change, r.ChangeReason = kind, reason })
}

// Reproduction is the proof a bug fix carries: the story's tests run on the
// code before the fix and fail there for a behavior reason.
type Reproduction struct {
	// Commit is the snapshot the story started from, where the tests ran.
	Commit string   `json:"commit,omitempty"`
	Tests  []string `json:"tests,omitempty"`
	Status string   `json:"status"` // Repro* statuses
	Detail string   `json:"detail,omitempty"`
	// Failures are the behavior failures seen before the fix.
	Failures []string `json:"failures,omitempty"`
	// key identifies the tests' content the proof was made with.
	key string
}

// Reproduction statuses. Only ReproDone proves the bug was reproduced.
const (
	ReproDone       = "reproduzida"
	ReproNotDone    = "não reproduzida" // the tests pass on the code before the fix
	ReproNoTest     = "sem teste"
	ReproUnclear    = "inconclusiva"   // only build, infrastructure or preexisting failures
	ReproUnverified = "não verificada" // no git snapshot to run on
)

// changeIssues asks for the proof the story's kind needs, once the commands
// pass on the current code. It returns false when ctx was canceled.
func (o *Orchestrator) changeIssues(ctx context.Context, ss *session, st *storyState, a assessment) ([]Issue, bool) {
	if ss.dry || st.reused || !a.passed() {
		return nil, true
	}
	switch st.change {
	case ChangeBug:
		r, ok := o.reproduce(ctx, ss, st)
		if !ok {
			return nil, false
		}
		st.repro = r
		o.updateStory(st.si, func(sr *StoryRun) { rr := *r; sr.Reproduction = &rr })
		return o.reproductionIssues(st, r), true
	case ChangeRefactor:
		return o.characterizationIssues(ss, st, a), true
	case ChangeFeature:
		return o.untestedIssues(ss, st), true
	}
	return nil, true
}

// storyTests lists the test files the story added or changed.
func (o *Orchestrator) storyTests(ss *session, st *storyState) []string {
	var tests []string
	for _, f := range o.changedFiles(ss, st.started) {
		if _, err := os.Stat(filepath.Join(ss.repo, f)); err == nil && quality.IsTestFile(f) {
			tests = append(tests, f)
		}
	}
	slices.Sort(tests)
	return tests
}

// reproduce runs the story's tests on the snapshot the story started from.
// The proof is kept while the tests stay the same.
func (o *Orchestrator) reproduce(ctx context.Context, ss *session, st *storyState) (*Reproduction, bool) {
	tests := o.storyTests(ss, st)
	if len(tests) == 0 {
		return &Reproduction{Status: ReproNoTest, Detail: "nenhum arquivo de teste alterado nesta US"}, true
	}
	key := contentKey(ss.repo, tests)
	if st.repro != nil && st.repro.key == key {
		return st.repro, true
	}
	r := &Reproduction{Commit: st.startCommit, Tests: tests, key: key}
	if ss.git == nil || st.startCommit == "" {
		r.Status, r.Detail = ReproUnverified, "sem git não há como rodar os testes no código anterior à correção"
		return r, true
	}
	o.logf(LevelInfo, "Reproduzindo o bug: testes da US no commit %s, antes da correção.", short(st.startCommit))
	results, err := o.runOnSnapshot(ctx, ss, st.startCommit, tests)
	switch {
	case ctx.Err() != nil:
		return nil, false
	case err != nil:
		r.Status, r.Detail = ReproUnverified, err.Error()
		return r, true
	}
	r.Status, r.Failures = judgeReproduction(results, st.baseline)
	switch r.Status {
	case ReproNotDone:
		r.Detail = "todos os comandos passam no código anterior à correção"
	case ReproUnclear:
		r.Detail = "só falhas de build, de infraestrutura ou que já existiam: nenhuma falha de comportamento"
	}
	return r, true
}

// runOnSnapshot copies files into a worktree at commit and runs the project
// commands there, from the cheapest layer, stopping at the first that fails.
func (o *Orchestrator) runOnSnapshot(ctx context.Context, ss *session, commit string, files []string) ([]quality.Result, error) {
	prefix, err := ss.git.Prefix(ctx)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "faztudo-repro-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, "repo")
	if err := ss.git.AddWorktreeAt(ctx, dir, commit); err != nil {
		return nil, fmt.Errorf("worktree do commit de partida: %w", err)
	}
	defer func() { _ = ss.git.RemoveWorktree(context.WithoutCancel(ctx), dir) }()
	work := filepath.Join(dir, prefix)
	linkDependencies(ss.repo, work, ss.git.Nested())
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(ss.repo, f))
		if err != nil {
			return nil, err
		}
		dst := filepath.Join(work, f)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return nil, err
		}
	}
	timeout := time.Duration(ss.policy.CheckTimeoutS) * time.Second
	var results []quality.Result
	failedLayer := -1
	for _, c := range quality.InLayers(ss.checks) {
		if failedLayer >= 0 && quality.Layer(c) > failedLayer {
			break
		}
		r := o.runGated(ctx, ss, work, c, timeout)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		results = append(results, r)
		if !r.Passed {
			failedLayer = quality.Layer(c)
		}
	}
	return results, nil
}

// judgeReproduction finds behavior failures: a failing test or case that did
// not already fail before the story. A build error, a time-out or a failure
// the baseline already had shows nothing about the bug.
func judgeReproduction(results []quality.Result, b *Baseline) (string, []string) {
	var failures []string
	clean := true
	for _, r := range results {
		if r.Passed {
			continue
		}
		clean = false
		if r.TimedOut {
			continue
		}
		var before []string
		if b != nil {
			before = b.Failing[r.Name]
		}
		for _, c := range r.Cases {
			if c.Status == quality.CaseFailed {
				failures = append(failures, r.Name+": "+c.Name)
			}
		}
		fs := r.Failures
		if len(fs) == 0 {
			fs = quality.ParseFailures(quality.Command{Name: r.Name, Run: r.Command}, failureText(r))
		}
		for _, f := range fs {
			if f.Test != "" && !f.Compile && !slices.Contains(before, f.Fingerprint) {
				failures = append(failures, r.Name+": "+failureTitle(f))
			}
		}
	}
	switch {
	case clean:
		return ReproNotDone, nil
	case len(failures) == 0:
		return ReproUnclear, nil
	}
	slices.Sort(failures)
	return ReproDone, slices.Compact(failures)
}

func (o *Orchestrator) reproductionIssues(st *storyState, r *Reproduction) []Issue {
	switch r.Status {
	case ReproNoTest:
		return []Issue{{
			Agent:    defaultImplementer(o.stages, st.plan),
			Severity: "alta",
			Description: "Correção de bug sem teste que reproduza a falha. Escreva um teste que falhe no código anterior " +
				"à correção, pelo comportamento errado, e passe com ela.",
			Location:     "reprodução do bug",
			Class:        classMissingTest,
			Fingerprints: []string{quality.TextFingerprint("reprodução", ReproNoTest)},
			Titles:       []string{"Bug sem teste de reprodução"},
		}}
	case ReproNotDone:
		return []Issue{{
			Agent:    o.assignFile(r.Tests[0], st.plan),
			Severity: "alta",
			Description: fmt.Sprintf("Os testes alterados nesta US (%s) também passam no código anterior à correção (commit %s): "+
				"não reproduzem o bug. Ajuste-os para falhar sem a correção.", strings.Join(r.Tests, ", "), short(r.Commit)),
			Location:     "reprodução do bug",
			Class:        classMissingTest,
			Fingerprints: []string{quality.TextFingerprint("reprodução", ReproNotDone)},
			Titles:       []string{"Teste não reproduz o bug"},
		}}
	}
	return nil
}

// characterizationIssues keeps a refactoring honest: every test file, test ID
// and passing case the baseline had is still there, and still passes.
func (o *Orchestrator) characterizationIssues(ss *session, st *storyState, a assessment) []Issue {
	b := st.baseline
	if b == nil {
		return nil
	}
	var lost []string
	for _, f := range b.Tests {
		if _, err := os.Stat(filepath.Join(ss.repo, f)); err != nil {
			lost = append(lost, "arquivo "+f)
		}
	}
	ids := quality.TestIDs(ss.repo)
	for _, id := range b.TestIDs {
		if !slices.Contains(ids, id) {
			lost = append(lost, "caso "+id)
		}
	}
	passed := map[string]bool{}
	read := map[string]bool{}
	for _, r := range a.results {
		if !r.Report.Read() {
			continue
		}
		read[r.Name] = true
		for _, c := range r.Cases {
			if c.Status == quality.CasePassed {
				passed[caseKey(r.Name, c.Name)] = true
			}
		}
	}
	for _, k := range b.PassedCases {
		command, name, _ := strings.Cut(k, caseSep)
		if read[command] && !passed[k] {
			lost = append(lost, "teste "+name)
		}
	}
	if len(lost) == 0 {
		return nil
	}
	return []Issue{{
		Agent:    defaultImplementer(o.stages, st.plan),
		Severity: "alta",
		Description: fmt.Sprintf("Refatoração não pode perder testes de caracterização: %d sumiram ou não passam mais (%s). "+
			"Restaure-os; reescrever é permitido, mas o comportamento coberto continua coberto.", len(lost), strings.Join(clipList(lost, 10), ", ")),
		Location:     "testes de caracterização",
		Class:        classMissingTest,
		Fingerprints: []string{quality.TextFingerprint("caracterização", strings.Join(lost, ","))},
		Titles:       []string{"Refatoração perdeu testes de caracterização"},
	}}
}

// untestedIssues asks a feature that changed code for its tests, unless the
// specification already listed them (the traceability asks for those) or
// the repository has no test to follow.
func (o *Orchestrator) untestedIssues(ss *session, st *storyState) []Issue {
	if len(st.reqs) > 0 {
		return nil
	}
	var code []string
	for _, f := range o.changedFiles(ss, st.started) {
		switch {
		case quality.IsTestFile(f):
			return nil
		case quality.IsCodeFile(f):
			code = append(code, f)
		}
	}
	if len(code) == 0 {
		return nil
	}
	hasTests := st.baseline != nil && len(st.baseline.Tests) > 0
	if st.baseline == nil {
		hasTests = len(quality.TestFiles(ss.repo)) > 0
	}
	if !hasTests {
		return nil
	}
	return []Issue{{
		Agent:    o.assignFile(code[0], st.plan),
		Severity: "alta",
		Description: fmt.Sprintf("Mudança de comportamento sem teste (%s). Escreva os cenários relevantes da US, "+
			"inclusive negativos, no padrão de testes do projeto.", strings.Join(clipList(code, 5), ", ")),
		Location:     "testes da funcionalidade",
		Class:        classMissingTest,
		Fingerprints: []string{quality.TextFingerprint("funcionalidade", "sem teste")},
		Titles:       []string{"Funcionalidade sem teste"},
	}}
}

// caseSep joins a command and a case name in Baseline.PassedCases.
const caseSep = " › "

func caseKey(command, name string) string { return command + caseSep + name }

// maxBaselineCases bounds the passing cases a baseline keeps.
const maxBaselineCases = 5000

func contentKey(root string, files []string) string {
	h := sha256.New()
	for _, f := range files {
		data, _ := os.ReadFile(filepath.Join(root, f))
		fmt.Fprintf(h, "%s\x00%x\x00", f, sha256.Sum256(data))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func short(commit string) string {
	if len(commit) > 10 {
		return commit[:10]
	}
	return commit
}
