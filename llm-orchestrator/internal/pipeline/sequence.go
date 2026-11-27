package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/vcs"
	"llm-orchestrator/internal/workspace"
)

// AnalysisStage maps the dependencies between the stories of a document, so
// independent stories are developed in parallel.
var AnalysisStage = Stage{
	ID:       "analysis",
	Title:    "Análise de dependências entre US",
	Role:     "Delivery Planner",
	Tier:     provider.TierHeavy,
	ReadOnly: true,
	System:   "Planejador de entregas. Só leitura: não crie, edite nem apague arquivos.",
	Task: "Mapeie as dependências entre as US para decidir o que roda em paralelo:\n" +
		"1. B depende de A se usa entidade, endpoint, tela, migration ou regra que A cria ou altera.\n" +
		"2. US que mexem nos mesmos arquivos ou módulos, com risco de conflito: trate como dependência (a anterior primeiro).\n" +
		"3. Áreas do código (pastas, módulos) que cada US deve tocar.\n" +
		"US independentes rodam em paralelo, cada uma em sua branch, e são integradas no fim da onda. Na dúvida, dependência.",
}

// IntegrationStage resolves the merge conflicts between stories developed in
// parallel.
var IntegrationStage = Stage{
	ID:    "integration",
	Title: "Integração das US paralelas",
	Role:  "Integration Engineer",
	Tier:  provider.TierStandard,
	System: "Resolve conflitos de merge entre US desenvolvidas em paralelo. Preserva o comportamento dos dois lados. " +
		"Sem git commit/merge/push (orquestrador conclui o merge).",
	Rules: true,
}

const analysisInstructions = "\n\nFim da resposta: bloco ```json obrigatório:\n" +
	"```json\n{\"stories\": [{\"id\": \"US-02\", \"depends_on\": [\"US-01\"], \"areas\": [\"src/cart/\"]}], \"reason\": \"resumo das ondas\"}\n```\n" +
	"Uma entrada por US, com a chave exata listada. depends_on vazio = pode rodar em paralelo com as demais da onda."

var analysisRef = stepRef{story: -1, id: AnalysisStage.ID}

// maxAnalysisStory caps each story sent to the dependency analysis.
const maxAnalysisStory = 2_500

// storyKeys names each story for the analysis: its ID when unique, its
// position otherwise.
func storyKeys(list []stories.Story) []string {
	count := map[string]int{}
	for _, s := range list {
		count[strings.ToLower(strings.TrimSpace(s.ID))]++
	}
	keys := make([]string, len(list))
	for i, s := range list {
		id := strings.TrimSpace(s.ID)
		keys[i] = id
		if id == "" || count[strings.ToLower(id)] > 1 {
			keys[i] = fmt.Sprintf("#%d", i+1)
		}
	}
	return keys
}

func buildAnalysisPrompt(task string, opts StartOptions, keys []string, discovery *workspace.Report, instructions string) string {
	var b strings.Builder
	if discovery != nil {
		fmt.Fprintf(&b, "## Repositório\n\n`%s`\n\n", discovery.Path)
		if summary := strings.TrimSpace(discovery.Summary); summary != "" {
			fmt.Fprintf(&b, "### Discovery\n\n%s\n\n", clip(summary, maxArtifactBytes))
		}
	}
	writeInstructions(&b, instructions)
	if opts.Preamble != "" {
		fmt.Fprintf(&b, "## Contexto do documento\n\n%s\n\n", clip(opts.Preamble, maxPreambleBytes))
	}
	b.WriteString("## User Stories\n\n")
	for i, s := range opts.Stories {
		fmt.Fprintf(&b, "### Chave `%s`: %s\n\n%s\n\n", keys[i], s.Title, clip(strings.TrimSpace(s.Content), maxAnalysisStory))
	}
	b.WriteString("## Sua tarefa\n\n")
	b.WriteString(task)
	b.WriteString(analysisInstructions)
	return b.String()
}

// storyDependency is the analysis' view of one story.
type storyDependency struct {
	ID        string   `json:"id"`
	DependsOn []string `json:"depends_on"`
	Areas     []string `json:"areas"`
}

type sequenceAnalysis struct {
	Stories []storyDependency `json:"stories"`
	Reason  string            `json:"reason"`
}

// parseAnalysis turns the analysis into one front per story. A story the
// analysis left out depends on the one before it.
func parseAnalysis(output string, keys []string) ([]Stream, bool) {
	a, ok := lastJSON(output, func(a sequenceAnalysis) bool { return a.Stories != nil })
	if !ok {
		return nil, false
	}
	byKey := map[string]storyDependency{}
	for _, d := range a.Stories {
		byKey[strings.ToLower(strings.TrimSpace(d.ID))] = d
	}
	nodes := make([]Stream, len(keys))
	for i, key := range keys {
		nodes[i] = Stream{ID: strings.ToLower(key)}
		d, listed := byKey[nodes[i].ID]
		switch {
		case listed:
			nodes[i].DependsOn = lowerAll(d.DependsOn)
			nodes[i].Files = trimmedPaths(d.Areas)
		case i > 0:
			nodes[i].DependsOn = []string{strings.ToLower(keys[i-1])}
		}
	}
	return pruneDependencies(nodes), true
}

func lowerAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
}

// referencedDependencies approximates the analysis in simulation: a story
// depends on the earlier stories whose ID it mentions.
func referencedDependencies(list []stories.Story, keys []string) []Stream {
	nodes := make([]Stream, len(list))
	for i, s := range list {
		nodes[i] = Stream{ID: strings.ToLower(keys[i])}
		for j := range i {
			if id := strings.TrimSpace(list[j].ID); id != "" && mentions(s.Content, id) {
				nodes[i].DependsOn = append(nodes[i].DependsOn, strings.ToLower(keys[j]))
			}
		}
	}
	return pruneDependencies(nodes)
}

func mentions(text, id string) bool {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(id) + `\b`).MatchString(text)
}

// chainDependencies keeps the document order: each story after the previous.
func chainDependencies(keys []string) []Stream {
	nodes := make([]Stream, len(keys))
	for i, key := range keys {
		nodes[i] = Stream{ID: strings.ToLower(key)}
		if i > 0 {
			nodes[i].DependsOn = []string{strings.ToLower(keys[i-1])}
		}
	}
	return nodes
}

// storyWaves converts the dependency waves into story indexes.
func storyWaves(nodes []Stream) [][]int {
	index := map[string]int{}
	for i, n := range nodes {
		index[n.ID] = i
	}
	var waves [][]int
	for _, wave := range streamWaves(nodes) {
		var w []int
		for _, n := range wave {
			w = append(w, index[n.ID])
		}
		slices.Sort(w)
		waves = append(waves, w)
	}
	return waves
}

func sequentialWaves(n int) [][]int {
	waves := make([][]int, n)
	for i := range waves {
		waves[i] = []int{i}
	}
	return waves
}

// analyzeSequence decides which stories run in parallel. A single story, a
// repository without git isolation, or an analysis without a readable answer
// keeps the document order.
func (o *Orchestrator) analyzeSequence(ctx context.Context, ss *session, opts StartOptions) ([][]int, bool) {
	if len(opts.Stories) > 1 && ss.policy.Sequential {
		o.logf(LevelInfo, "Desenvolvimento sequencial: as US seguem a ordem do documento, uma de cada vez.")
	} else if len(opts.Stories) > 1 && !canIsolate(ss) && !ss.dry {
		o.logf(LevelInfo, "Sem git, as US não rodam em paralelo; seguem a ordem do documento.")
	}
	if len(opts.Stories) < 2 || ss.policy.Sequential || (!canIsolate(ss) && !ss.dry) {
		// Left pending, the step would be canceled with the run and become
		// the resume point, so resuming would start everything over.
		o.updateStep(analysisRef, func(s *Step) { s.Status, s.Note = StepSkipped, "Sem análise: as US seguem a ordem do documento" })
		o.setWaves(sequentialWaves(len(opts.Stories)), nil, nil)
		return sequentialWaves(len(opts.Stories)), true
	}
	keys := storyKeys(opts.Stories)
	resp, ok := o.runStage(ctx, analysisRef, provider.Request{
		Role:     AnalysisStage.Role,
		System:   o.texts.analysis.System,
		Prompt:   buildAnalysisPrompt(o.texts.analysis.Task, opts, keys, ss.report, ss.instructions),
		Tier:     AnalysisStage.Tier,
		WorkDir:  ss.repo,
		ReadOnly: true,
	})
	if !ok || !o.guardInputs(ss, analysisRef) {
		return nil, false
	}
	nodes, parsed := parseAnalysis(resp.Output, keys)
	switch {
	case parsed:
	case resp.Simulated:
		nodes = referencedDependencies(opts.Stories, keys)
	default:
		nodes = chainDependencies(keys)
		o.logf(LevelWarn, "A análise não retornou dependências em JSON; as US seguem em sequência.")
	}
	waves := storyWaves(nodes)
	o.setWaves(waves, nodes, keys)
	o.logWaves(waves, opts.Stories)
	return waves, true
}

// setWaves records each story's wave and dependencies for the UI and retries.
func (o *Orchestrator) setWaves(waves [][]int, nodes []Stream, keys []string) {
	display := map[string]string{}
	for _, k := range keys {
		display[strings.ToLower(k)] = k
	}
	o.mu.Lock()
	o.run.Waves = waves
	for w, wave := range waves {
		for _, si := range wave {
			o.run.Stories[si].Wave = w + 1
			if nodes != nil {
				o.run.Stories[si].DependsOn = displayKeys(nodes[si].DependsOn, display)
			}
		}
	}
	o.mu.Unlock()
	o.notify()
}

func displayKeys(ids []string, display map[string]string) []string {
	var out []string
	for _, id := range ids {
		out = append(out, display[id])
	}
	return out
}

func (o *Orchestrator) logWaves(waves [][]int, list []stories.Story) {
	parallel := slices.ContainsFunc(waves, func(w []int) bool { return len(w) > 1 })
	if !parallel {
		o.logf(LevelInfo, "Análise de dependências: as US seguem em sequência.")
		return
	}
	var parts []string
	for w, wave := range waves {
		var labels []string
		for _, si := range wave {
			labels = append(labels, storyLabel(list[si]))
		}
		parts = append(parts, fmt.Sprintf("onda %d: %s", w+1, strings.Join(labels, ", ")))
	}
	o.logf(LevelInfo, "Análise de dependências: %s.", strings.Join(parts, "; "))
}

// resumePoint is where a retried story continues.
type resumePoint struct {
	si      int
	stageAt int
	started time.Time
}

// runWaves develops the stories wave by wave; stories already finished (by an
// earlier attempt) are kept.
func (o *Orchestrator) runWaves(ctx context.Context, ss *session, opts StartOptions, waves [][]int, resume *resumePoint) bool {
	for _, wave := range waves {
		var ok bool
		if len(wave) > 1 && (canIsolate(ss) || ss.dry) {
			ok = o.runStoriesInParallel(ctx, ss, opts, wave, resume)
		} else {
			ok = o.runStoriesInPlace(ctx, ss, opts, wave, resume)
		}
		if !ok {
			return false
		}
	}
	return true
}

func (o *Orchestrator) runStoriesInPlace(ctx context.Context, ss *session, opts StartOptions, wave []int, resume *resumePoint) bool {
	for _, si := range wave {
		if o.storyFinished(si) {
			continue
		}
		if reason := o.unmetDependency(si, opts.Stories); reason != "" {
			o.skipStory(si, reason)
			continue
		}
		st, startAt, fresh := o.storyTask(ss, opts, si, resume)
		if !o.developStoryFrom(ctx, ss, st, startAt, fresh) {
			return false
		}
		o.closeStory(ss, st, len(opts.Stories))
		if !o.promoteStory(ctx, ss, st) {
			return false
		}
	}
	return true
}

// storyTask prepares a story: resumed from its failed stage or started anew.
func (o *Orchestrator) storyTask(ss *session, opts StartOptions, si int, resume *resumePoint) (st *storyState, startAt int, fresh bool) {
	if resume != nil && resume.si == si {
		return o.restoreStoryState(si, resume.started, opts, ss), resume.stageAt, false
	}
	// The file system's clock: the story's changed files are those stamped
	// after this, and a time.Now() could postdate the first agent's writes.
	started := quality.FileClock(ss.repo)
	o.updateStory(si, func(r *StoryRun) { r.Status = StepRunning; r.StartedAt = &started })
	if total := len(opts.Stories); total > 1 {
		o.logf(LevelInfo, "Iniciando %s (%d de %d).", opts.Stories[si].Title, si+1, total)
	}
	st = &storyState{
		si: si, started: started, sc: o.newStoryContext(si, opts, ss),
		plan: fullPlan(o.stages, "default"),
		reqs: quality.ExtractRequirements(opts.Preamble + "\n\n" + opts.Stories[si].Content),
	}
	return st, 0, true
}

func (o *Orchestrator) storyFinished(si int) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return isFinished(o.run.Stories[si].Status)
}

func isFinished(s StepStatus) bool {
	return s == StepDone || s == StepWarning || s == StepSkipped || s == StepWaiting
}

// newStoryContext places a story among the ones delivered in earlier waves
// and the ones developed beside it.
func (o *Orchestrator) newStoryContext(si int, opts StartOptions, ss *session) storyContext {
	sc := storyContext{
		Preamble: opts.Preamble, Story: opts.Stories[si], Position: si + 1, Total: len(opts.Stories),
		Rules: ss.rules(), Instructions: ss.instructions,
		Audit:       ss.policy.StageDisabled("arch"),
		AuditNotice: o.texts.auditNotice,
		Profiles:    slices.Clone(ss.policy.Profiles),
		Overrides:   o.overrides(),
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	sc.Answers = slices.Clone(o.run.Stories[si].Answers)
	wave := storyWave(o.run.Stories, si)
	for i, s := range opts.Stories {
		switch w := storyWave(o.run.Stories, i); {
		case w < wave:
			sc.Delivered = append(sc.Delivered, s)
		case w == wave && i != si:
			sc.Parallel = append(sc.Parallel, s)
		}
	}
	if len(sc.Parallel) > 0 {
		sc.PortBase = lanePortBase + lanePortSpan*si
	}
	return sc
}

// storyWave is the story's wave, or its position when no analysis ran.
func storyWave(runs []StoryRun, si int) int {
	if si < len(runs) && runs[si].Wave > 0 {
		return runs[si].Wave
	}
	return si + 1
}

// storyLane is a story developed in its own worktree.
type storyLane struct {
	st      *storyState
	ss      *session
	startAt int
	fresh   bool
	dir     string // worktree root; "" when the story shares the repository
	ok      bool
}

// runStoriesInParallel develops a wave's stories at the same time, each on its
// own branch and worktree, then integrates them.
func (o *Orchestrator) runStoriesInParallel(ctx context.Context, ss *session, opts StartOptions, wave []int, resume *resumePoint) bool {
	lanes, cleanup, err := o.openLanes(ctx, ss, opts, wave, resume)
	defer cleanup()
	if err != nil {
		o.logf(LevelWarn, "Worktrees indisponíveis (%v); US desta onda em sequência.", err)
		return o.runStoriesInPlace(ctx, ss, opts, wave, resume)
	}
	o.logf(LevelInfo, "%d US em paralelo nesta onda.", len(lanes))
	waveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	running := make([]int, len(lanes))
	for i, l := range lanes {
		running[i] = l.st.si
	}
	o.setLanes(running, true)
	var wg sync.WaitGroup
	for _, lane := range lanes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if lane.ok = o.developStoryFrom(waveCtx, lane.ss, lane.st, lane.startAt, lane.fresh); lane.ok {
				o.closeStory(lane.ss, lane.st, len(opts.Stories))
				return
			}
			if !o.Snapshot().Running {
				cancel() // a global failure, or the run was stopped: the wave stops
			}
		}()
	}
	wg.Wait()
	o.setLanes(running, false)
	var failed []string
	var done []int
	for _, l := range lanes {
		if l.ok {
			done = append(done, l.st.si)
		} else {
			failed = append(failed, l.st.sc.Story.Title)
		}
	}
	if len(failed) == 0 {
		return o.integrateWave(ctx, ss, opts, wave)
	}
	if ctx.Err() != nil || !o.Snapshot().Running {
		return false
	}
	// A story's own failure: the others of the wave are integrated, then the
	// run stops there, resumable from the failed story.
	if !o.integrateWave(ctx, ss, opts, done) {
		return false
	}
	o.finish(ResultFailed, LevelError, fmt.Sprintf("Falha em %s; as demais US da onda foram integradas. Continue de onde parou.", strings.Join(failed, ", ")))
	return false
}

// openLanes gives each unfinished story of the wave its own worktree on its
// branch (the existing one when resuming). Without git isolation (simulation)
// the stories share the repository. cleanup saves leftovers and removes the
// worktrees.
func (o *Orchestrator) openLanes(ctx context.Context, ss *session, opts StartOptions, wave []int, resume *resumePoint) ([]*storyLane, func(), error) {
	var lanes []*storyLane
	tmp := ""
	cleanup := func() {
		kept := false
		for _, l := range lanes {
			if l.dir != "" && !o.closeLane(ctx, ss, l) {
				kept = true
			}
		}
		if tmp != "" && !kept {
			os.RemoveAll(tmp)
		}
	}
	var prefix string
	if canIsolate(ss) {
		var err error
		if prefix, err = ss.git.Prefix(ctx); err != nil {
			return nil, cleanup, err
		}
		if tmp, err = os.MkdirTemp("", "faztudo-stories-"); err != nil {
			return nil, cleanup, err
		}
	}
	for _, si := range wave {
		if o.storyFinished(si) {
			continue
		}
		if reason := o.unmetDependency(si, opts.Stories); reason != "" {
			o.skipStory(si, reason)
			continue
		}
		lane := &storyLane{ss: ss}
		if tmp != "" {
			setup := laneSetup{si: si, story: opts.Stories[si], dir: filepath.Join(tmp, fmt.Sprint(si)), prefix: prefix,
				isResuming: resume != nil && resume.si == si}
			if err := o.isolateLane(ctx, ss, lane, setup); err != nil {
				return lanes, cleanup, err
			}
		}
		lane.st, lane.startAt, lane.fresh = o.storyTask(lane.ss, opts, si, resume)
		lane.fresh = lane.fresh && lane.dir == ""
		lanes = append(lanes, lane)
	}
	return lanes, cleanup, nil
}

// laneSetup locates a story's worktree.
type laneSetup struct {
	si          int
	story       stories.Story
	dir, prefix string
	isResuming  bool
}

// isolateLane checks the story's branch out in its own worktree: the existing
// branch when resuming, a new one from HEAD otherwise.
func (o *Orchestrator) isolateLane(ctx context.Context, ss *session, lane *storyLane, setup laneSetup) error {
	branch := o.storyBranch(setup.si)
	var err error
	if setup.isResuming && branch != "" {
		err = ss.git.AddExistingWorktree(ctx, setup.dir, branch)
	} else {
		branch = vcs.BranchName(storyLabel(setup.story), time.Now())
		err = ss.git.AddBranchWorktree(ctx, setup.dir, branch)
	}
	if err != nil {
		return fmt.Errorf("worktree de %s: %w", storyLabel(setup.story), err)
	}
	work := filepath.Join(setup.dir, setup.prefix)
	linkDependencies(ss.repo, work, ss.git.Nested())
	lane.dir, lane.ss = setup.dir, ss.inWorktree(work)
	o.updateStory(setup.si, func(r *StoryRun) { r.Branch = branch })
	o.logf(LevelInfo, "Branch %s em worktree própria para %s.", branch, setup.story.Title)
	return nil
}

func (o *Orchestrator) storyBranch(si int) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.run.Stories[si].Branch
}

// closeLane commits what an interrupted story left in its worktree, so a
// retry resumes from it, and removes the worktree; the branch stays. When the
// commit fails the worktree is kept, so the work is not lost; it reports
// whether the worktree was removed.
func (o *Orchestrator) closeLane(ctx context.Context, ss *session, l *storyLane) bool {
	ctx = context.WithoutCancel(ctx)
	msg := fmt.Sprintf("faztudo(%s): salva trabalho interrompido", storyLabel(l.st.sc.Story))
	if _, err := l.ss.git.CommitAll(ctx, msg, dependencyPaths(l.ss.git.Nested())...); err != nil {
		o.logf(LevelWarn, "Não foi possível salvar o trabalho pendente de %s (%v); a worktree %s foi mantida para recuperação manual.",
			l.st.sc.Story.Title, err, l.dir)
		return false
	}
	_ = ss.git.RemoveWorktree(ctx, l.dir)
	return true
}

// inWorktree is the session of a story developed in the worktree work.
func (ss *session) inWorktree(work string) *session {
	c := *ss
	c.repo = work
	c.git = vcs.At(work)
	c.guard = quality.NewGuard(work, ss.patterns)
	if ss.report != nil {
		report := *ss.report
		report.Path = work
		c.report = &report
	}
	return &c
}

// integrateWave merges the wave's approved story branches into a new
// integration branch and promotes it to the base only after the project
// commands pass on the combination: stories that pass alone may still break
// together. Stories that may not be promoted are left out of the candidate;
// a candidate that fails keeps the base as it was.
func (o *Orchestrator) integrateWave(ctx context.Context, ss *session, opts StartOptions, wave []int) bool {
	if ss.git == nil {
		return true
	}
	var candidates []int
	for _, si := range wave {
		run := o.storyRun(si)
		if run.Integration == IntegrationBlocked || run.Status == StepWaiting {
			continue // already kept out by an unmet dependency, or not developed yet
		}
		if reason := promotable(run, ss.policy); reason != "" {
			o.markIntegration(si, IntegrationBlocked, reason)
			o.logf(LevelWarn, "%s fica fora da integração: %s. A branch %s fica para revisão.", run.Title, reason, run.Branch)
			continue
		}
		candidates = append(candidates, si)
	}
	if len(candidates) == 0 {
		return true
	}
	var labels []string
	for _, si := range candidates {
		labels = append(labels, storyLabel(opts.Stories[si]))
	}
	started := time.Now()
	branch := vcs.BranchName("integra "+strings.Join(labels, " "), time.Now())
	if err := ss.git.CreateBranch(ctx, branch); err != nil {
		o.failRun(fmt.Errorf("criar a branch de integração %s: %w", branch, err))
		return false
	}
	o.mu.Lock()
	o.run.Integrations = append(o.run.Integrations, branch)
	o.mu.Unlock()
	o.logf(LevelInfo, "Branch %s criada para integrar %s.", branch, strings.Join(labels, ", "))
	tried := labels
	var merged []int
	labels = nil
	for _, si := range candidates {
		if !o.mergeStory(ctx, ss, opts, si) {
			return false
		}
		if o.storyRun(si).Integration != IntegrationBlocked {
			merged = append(merged, si)
			labels = append(labels, storyLabel(opts.Stories[si]))
		}
	}
	p := Promotion{Stories: labels, Branch: branch, started: started}
	if len(merged) == 0 {
		p.Stories = tried
		return o.blockPromotion(ctx, ss, nil, p, "nenhuma US da onda pôde ser integrada")
	}
	checks, reason, alive := o.verifyCandidate(ctx, ss, strings.Join(labels, ", "))
	if !alive {
		return false
	}
	p.Checks = checks
	if reason == "" {
		reason = o.baseMoved(ctx, ss, branch)
	}
	p.Commit, _ = ss.git.Head(ctx)
	if reason != "" {
		return o.blockPromotion(ctx, ss, merged, p, "conjunto integrado: "+reason)
	}
	if o.needsReview(ss, merged) {
		return o.openReview(ctx, ss, strings.Join(labels, ", "), &p, merged)
	}
	if !o.mergeIntoBase(ctx, ss, strings.Join(labels, ", "), &p, merged) {
		return false
	}
	p.Status = IntegrationIntegrated
	o.recordPromotion(p, merged)
	o.updateGraph(ctx, ss) // the next wave sees the code just integrated
	return true
}

func (o *Orchestrator) mergeStory(ctx context.Context, ss *session, opts StartOptions, si int) bool {
	branch, story := o.storyBranch(si), opts.Stories[si]
	if branch == "" {
		return true
	}
	if sha, err := ss.git.Rev(ctx, branch); err == nil && ss.git.IsAncestor(ctx, sha, "HEAD") {
		o.logf(LevelInfo, "%s já estava integrada; merge não repetido.", story.Title)
		return true
	}
	conflicts, err := ss.git.StartMerge(ctx, branch, fmt.Sprintf("faztudo: integra %s", storyLabel(story)))
	switch {
	case err != nil:
		o.leaveUnintegrated(si, fmt.Sprintf("merge da branch %s falhou: %v", branch, err))
		return true
	case len(conflicts) == 0:
		o.logf(LevelSuccess, "%s integrada.", story.Title)
		return true
	}
	return o.resolveConflicts(ctx, ss, opts, si, conflicts)
}

// resolveConflicts asks an agent to resolve a story's merge conflicts. When
// it cannot, the merge is aborted and the story is left on its own branch.
func (o *Orchestrator) resolveConflicts(ctx context.Context, ss *session, opts StartOptions, si int, conflicts []string) bool {
	ref := stepRef{si, IntegrationStage.ID}
	o.appendStep(si, Step{
		ID: ref.id, Title: IntegrationStage.Title, Role: IntegrationStage.Role, Tier: IntegrationStage.Tier,
		Status: StepPending, Kind: "merge", Note: fmt.Sprintf("%d arquivo(s) em conflito", len(conflicts)),
	})
	o.logf(LevelWarn, "%s conflita com outra US da onda (%s); acionando a integração.", opts.Stories[si].Title, strings.Join(conflicts, ", "))
	started := time.Now()
	o.markRunning(ref, started)
	resp, err := o.executeRetrying(ctx, ref, provider.Request{
		Role:    IntegrationStage.Role,
		System:  o.texts.integration.System,
		Prompt:  buildIntegrationPrompt(o.newStoryContext(si, opts, ss), ss.report, conflicts),
		Tier:    IntegrationStage.Tier,
		WorkDir: ss.repo,
	})
	if ctx.Err() != nil {
		ss.git.AbortMerge(ctx)
		o.cancelStep(ref, time.Since(started))
		return false
	}
	if err == nil {
		o.recordDone(ref, resp, time.Since(started))
		err = o.concludeMerge(ctx, ss, ref, conflicts)
	}
	if err != nil {
		ss.git.AbortMerge(ctx)
		o.updateStep(ref, func(s *Step) { s.Status, s.Error = StepWarning, err.Error() })
		o.leaveUnintegrated(si, err.Error())
	}
	return true
}

func (o *Orchestrator) concludeMerge(ctx context.Context, ss *session, ref stepRef, conflicts []string) error {
	if changed := ss.guard.Check(); len(changed) > 0 {
		if err := ss.guard.Restore(changed); err != nil {
			return fmt.Errorf("arquivo de entrada protegido alterado: %v", err)
		}
	}
	if left := filesWithMarkers(ctx, ss.repo, conflicts); len(left) > 0 {
		return fmt.Errorf("conflito não resolvido em %s", strings.Join(left, ", "))
	}
	c, err := ss.git.FinishMerge(ctx)
	if err != nil {
		return err
	}
	o.updateStep(ref, func(s *Step) { s.Commit = c })
	o.logf(LevelSuccess, "Conflitos resolvidos; commit %s.", c.Hash)
	return nil
}

var reConflictMarker = regexp.MustCompile(`(?m)^(<{7}|>{7}) `)

// filesWithMarkers lists the files that still hold conflict markers. Paths
// are relative to the repository root, which may sit above dir.
func filesWithMarkers(ctx context.Context, dir string, files []string) []string {
	root := dir
	if top, err := vcs.At(dir).Prefix(ctx); err == nil && top != "" {
		root = strings.TrimSuffix(dir, "/"+strings.TrimSuffix(top, "/"))
	}
	var left []string
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err == nil && reConflictMarker.Match(data) {
			left = append(left, f)
		}
	}
	return left
}

func buildIntegrationPrompt(sc storyContext, discovery *workspace.Report, conflicts []string) string {
	var b strings.Builder
	writeContext(&b, sc, discovery)
	if len(sc.Parallel) > 0 {
		b.WriteString("## US desenvolvidas em paralelo, já integradas\n\n")
		for _, s := range sc.Parallel {
			fmt.Fprintf(&b, "- %s\n", s.Title)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## Conflitos de merge\n\n%s\n\n", strings.Join(quoteAll(conflicts), ", "))
	b.WriteString("## Sua tarefa\n\nO merge da branch desta US está em andamento. Resolva cada conflito preservando o que as duas US entregam; " +
		"remova todos os marcadores (<<<<<<<, =======, >>>>>>>). Rode os testes afetados. Termine com uma linha por arquivo: como resolveu.")
	return b.String()
}

// leaveUnintegrated marks a story whose branch could not be merged.
func (o *Orchestrator) leaveUnintegrated(si int, reason string) {
	o.markIntegration(si, IntegrationBlocked, reason)
	o.logf(LevelWarn, "%s não integrada (%s); a branch %s fica para merge manual.", o.storyTitle(si), reason, o.storyBranch(si))
}

func (o *Orchestrator) storyTitle(si int) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.run.Stories[si].Title
}

// appendStep adds a step at the end of story si.
func (o *Orchestrator) appendStep(si int, step Step) {
	o.mu.Lock()
	o.run.Stories[si].Steps = append(o.run.Stories[si].Steps, step)
	o.mu.Unlock()
	o.notify()
}

// failRun ends the run on an error that belongs to no step.
func (o *Orchestrator) failRun(err error) {
	o.abortRemaining(-1, StepCanceled)
	o.finish(ResultFailed, LevelError, err.Error())
}
