package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/vcs"
)

// Candidate is one alternative implementation produced by best-of-N.
type Candidate struct {
	ID       string `json:"id"`
	Strategy string `json:"strategy"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Files    int    `json:"files"`
	Issues   int    `json:"issues"`
	Passed   bool   `json:"passed"`
	Chosen   bool   `json:"chosen,omitempty"`
	// Note explains a disqualification (no changes, protected file, error).
	Note string `json:"note,omitempty"`
}

// candidateStrategies steer each alternative implementation differently, so
// the candidates explore distinct solutions instead of near-identical ones.
var candidateStrategies = []string{
	"Menor mudança que atende toda a US. Máximo reuso do código existente.",
	"Solução robusta: erros tratados, teste para cada caso da especificação.",
	"Legibilidade primeiro: funções pequenas, nomes claros, mínimo de abstração.",
}

// SelectorStage compares the qualified candidates when the checks leave a tie.
var SelectorStage = Stage{
	ID:       "selector",
	Title:    "Seleção de candidatos",
	Role:     "Candidate Selector",
	Tier:     provider.TierHeavy,
	ReadOnly: true,
	System:   "Compara implementações da mesma tarefa, escolhe a melhor. Ordem: atende toda a US; correta; simples e legível; segue convenções.",
}

// dependencyDirs are ignored directories linked into each worktree so the
// project commands find installed dependencies.
var dependencyDirs = []string{"node_modules", ".venv", "venv"}

const maxCandidateDiff = 15_000

// candidate is the working state of one alternative.
type candidate struct {
	Candidate
	dir    string // worktree root
	work   string // the repository directory inside the worktree
	hash   string
	commit *vcs.Commit
	output string
	assess assessment
	ok     bool
}

// runCandidates runs an implementer N times in parallel, each in its own git
// worktree with a different strategy, checks every result and fast-forwards
// the repository to the best one.
func (o *Orchestrator) runCandidates(ctx context.Context, ss *session, st *storyState, stage Stage, ref stepRef) (*provider.Response, bool) {
	n := min(ss.policy.Candidates, len(candidateStrategies))
	started := time.Now()
	o.updateStep(ref, func(s *Step) {
		s.Status = StepRunning
		s.StartedAt = &started
		s.Note = fmt.Sprintf("%d candidatos em paralelo", n)
	})
	o.logf(LevelInfo, "%s: %d implementações candidatas em worktrees separadas.", stage.Title, n)

	fail := func(err error) (*provider.Response, bool) {
		if ctx.Err() != nil {
			o.cancelStep(ref, time.Since(started))
		} else {
			o.failStep(ref, time.Since(started), err)
		}
		return nil, false
	}

	base, err := ss.git.Head(ctx)
	if err != nil {
		return fail(err)
	}
	prefix, err := ss.git.Prefix(ctx)
	if err != nil {
		return fail(err)
	}
	tmp, err := os.MkdirTemp("", "faztudo-candidates-")
	if err != nil {
		return fail(err)
	}
	cands := make([]*candidate, n)
	defer func() {
		for _, c := range cands {
			if c != nil {
				_ = ss.git.RemoveWorktree(context.WithoutCancel(ctx), c.dir)
			}
		}
		os.RemoveAll(tmp)
	}()

	letters := "ABC"
	for i := range cands {
		dir := filepath.Join(tmp, string(letters[i]))
		if err := ss.git.AddWorktree(ctx, dir); err != nil {
			return fail(fmt.Errorf("worktree do candidato %c: %w", letters[i], err))
		}
		c := &candidate{Candidate: Candidate{ID: string(letters[i]), Strategy: candidateStrategies[i]}, dir: dir, work: filepath.Join(dir, prefix)}
		cands[i] = c
		linkDependencies(ss.repo, c.work, ss.git.Nested())
	}

	prompt := buildPrompt(stage, st.sc, ss.report, o.stepsBefore(ref))
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i, c := range cands {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := o.limited(provider.Request{
				Role:    fmt.Sprintf("%s · candidato %s", stage.Role, c.ID),
				System:  stage.System,
				Prompt:  prompt + "\n\n## Estratégia\n\n" + c.Strategy,
				Tier:    stage.Tier,
				WorkDir: c.work,
			})
			resp, err := o.exec.Execute(ctx, req, o.providerLog)
			o.recordUsage(ref, req, resp, 1)
			if errors.Is(err, provider.ErrTurnLimit) {
				resp, err = o.turnLimited(ref, req, resp, err)
			}
			if err != nil {
				errs[i] = err
				return
			}
			c.output, c.Provider, c.Model = resp.Output, resp.Name, resp.Model
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}

	protected := ss.guard.Files()
	for i, c := range cands {
		if errs[i] != nil {
			c.Note = "falhou: " + errs[i].Error()
			continue
		}
		work := vcs.At(c.work)
		commit, err := work.CommitAll(ctx, fmt.Sprintf("faztudo(%s): %s — candidato %s", storyLabel(st.sc.Story), stage.Title, c.ID), dependencyPaths(work.Nested())...)
		switch {
		case err != nil:
			c.Note = "commit falhou: " + err.Error()
			continue
		case commit == nil:
			// Still a valid outcome (the work may already be done), but it
			// loses ties to candidates that changed something.
			c.Note, c.hash = "nenhuma alteração", base
		default:
			c.hash, c.commit = commit.Hash, commit
			c.Commit, c.Files = commit.Hash, len(commit.Files)
		}
		var changed []string
		for _, f := range filesOf(c.commit) {
			path := strings.TrimPrefix(f.Path, prefix)
			if slices.Contains(protected, path) {
				c.Note = "alterou arquivo de entrada protegido: " + path
				break
			}
			changed = append(changed, path)
		}
		if c.Note != "" && c.commit != nil {
			continue
		}
		o.logf(LevelInfo, "Verificando o candidato %s.", c.ID)
		a, ok := o.assess(ctx, ss, st, c.work, changed)
		if !ok {
			return fail(ctx.Err())
		}
		c.assess, c.ok = a, true
		c.Issues, c.Passed = len(a.issues), a.passed()
		o.logf(LevelInfo, "Candidato %s: %d problema(s).", c.ID, c.Issues)
	}

	chosen, reason := o.chooseCandidate(ctx, ss, base, cands)
	if chosen == nil {
		return fail(errors.New("nenhum candidato produziu uma implementação válida"))
	}
	if err := ss.git.FastForward(ctx, chosen.hash); err != nil {
		return fail(fmt.Errorf("aplicar o candidato %s: %w", chosen.ID, err))
	}
	chosen.Chosen = true
	o.logf(LevelSuccess, "Candidato %s selecionado: %s", chosen.ID, reason)

	summary := make([]Candidate, n)
	for i, c := range cands {
		summary[i] = c.Candidate
	}
	elapsed := time.Since(started)
	output := fmt.Sprintf("Candidato %s selecionado (%s)\n\nMotivo: %s\n\n%s", chosen.ID, chosen.Strategy, reason, chosen.output)
	o.updateStep(ref, func(s *Step) {
		s.Status = StepDone
		s.Provider, s.Model = chosen.Provider, chosen.Model
		s.Output = output
		s.Note = fmt.Sprintf("Candidato %s de %d", chosen.ID, n)
		s.Candidates = summary
		s.Commit = chosen.commit
		s.DurationMS = elapsed.Milliseconds()
	})
	if traces := chosen.assess.traces; len(traces) > 0 {
		o.updateStory(st.si, func(r *StoryRun) { r.Trace = traces })
	}
	if chosen.Passed {
		// The repository now holds exactly what was just verified.
		st.verifiedAt = time.Now()
	}
	return &provider.Response{Output: output, Name: chosen.Provider, Model: chosen.Model, Duration: elapsed}, true
}

// chooseCandidate keeps the candidates with the fewest issues and asks the
// selector to break ties by comparing their diffs.
func (o *Orchestrator) chooseCandidate(ctx context.Context, ss *session, base string, cands []*candidate) (*candidate, string) {
	var valid []*candidate
	for _, c := range cands {
		if c.ok {
			valid = append(valid, c)
		}
	}
	if len(valid) == 0 {
		return nil, ""
	}
	// Fewest issues first; among equals, candidates that changed something.
	rank := func(c *candidate) int {
		r := c.Issues * 2
		if c.commit == nil {
			r++
		}
		return r
	}
	best := slices.MinFunc(valid, func(a, b *candidate) int { return rank(a) - rank(b) })
	var tied []*candidate
	for _, c := range valid {
		if rank(c) == rank(best) {
			tied = append(tied, c)
		}
	}
	reason := fmt.Sprintf("menor número de problemas nas verificações (%d)", best.Issues)
	if len(tied) == 1 {
		return best, reason
	}

	var b strings.Builder
	writeInstructions(&b, ss.instructions) // the better candidate is the one that follows them
	b.WriteString("## Candidatos\n\nEmpate nas verificações automáticas. Compare os diffs.\n\n")
	for _, c := range tied {
		diff, err := ss.git.Diff(ctx, base, c.hash, maxCandidateDiff)
		if err != nil {
			diff = err.Error()
		}
		fmt.Fprintf(&b, "### Candidato %s\n\nEstratégia: %s\n\n```diff\n%s\n```\n\n", c.ID, c.Strategy, diff)
	}
	b.WriteString("## Sua tarefa\n\nEscolha a melhor. Fim da resposta: bloco ```json obrigatório:\n" +
		"```json\n{\"choice\": \"A\", \"reason\": \"motivo em uma frase\"}\n```")

	req := o.limited(provider.Request{
		Role: SelectorStage.Role, System: o.texts.selector.System, Prompt: b.String(),
		Tier: SelectorStage.Tier, WorkDir: ss.repo, ReadOnly: true,
	})
	resp, err := o.exec.Execute(ctx, req, o.providerLog)
	o.recordUsage(stepRef{-1, SelectorStage.ID}, req, resp, 1)
	if err != nil {
		o.logf(LevelWarn, "Seletor indisponível (%v); usando o candidato %s.", err, tied[0].ID)
		return tied[0], reason
	}
	type choice struct {
		Choice string `json:"choice"`
		Reason string `json:"reason"`
	}
	if v, ok := lastJSON(resp.Output, func(c choice) bool { return c.Choice != "" }); ok {
		for _, c := range tied {
			if strings.EqualFold(strings.TrimSpace(v.Choice), c.ID) {
				return c, strings.TrimSpace(v.Reason)
			}
		}
	}
	return tied[0], reason
}

func filesOf(c *vcs.Commit) []vcs.FileChange {
	if c == nil {
		return nil
	}
	return c.Files
}

// dependencyPaths lists the dependency directories of a repository and of its
// nested repositories, relative to it.
func dependencyPaths(nested []string) []string {
	paths := slices.Clone(dependencyDirs)
	for _, n := range nested {
		for _, name := range dependencyDirs {
			paths = append(paths, n+"/"+name)
		}
	}
	return paths
}

// linkDependencies symlinks installed dependencies from the repository into a
// worktree, where ignored directories do not exist.
func linkDependencies(repo, work string, nested []string) {
	for _, name := range dependencyPaths(nested) {
		src := filepath.Join(repo, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if _, err := os.Lstat(filepath.Join(work, name)); err == nil {
			continue
		}
		_ = os.Symlink(src, filepath.Join(work, name))
	}
}
