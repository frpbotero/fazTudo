package pipeline

import (
	"cmp"
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
	"llm-orchestrator/internal/vcs"
	"llm-orchestrator/internal/workspace"
)

// Stream is one development front of the architect's plan. Fronts whose
// dependencies are met and whose files do not overlap run in parallel.
type Stream struct {
	ID        string   `json:"id"`
	Agent     string   `json:"agent"`
	Title     string   `json:"title"`
	Scope     string   `json:"scope,omitempty"`
	Files     []string `json:"files,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
}

const streamCoveredNote = "Dividido em frentes paralelas"

var reStreamID = regexp.MustCompile(`[^a-z0-9]+`)

func streamStepID(s Stream) string { return "stream-" + s.ID }

// validStreams keeps the fronts assigned to an implementer, with unique IDs
// and dependencies that point to other listed fronts.
func validStreams(raw []Stream, stages []Stage) []Stream {
	var streams []Stream
	taken := map[string]bool{}
	for i, s := range raw {
		s.Agent = strings.ToLower(strings.TrimSpace(s.Agent))
		if !isImplementer(stages, s.Agent) {
			continue
		}
		s.ID = uniqueStreamID(streamSlug(s.ID, i), taken)
		s.Title = cmp.Or(strings.TrimSpace(s.Title), s.ID)
		s.Scope = strings.TrimSpace(s.Scope)
		s.Files = trimmedPaths(s.Files)
		s.DependsOn = slugs(s.DependsOn)
		streams = append(streams, s)
	}
	return pruneDependencies(streams)
}

func streamSlug(id string, index int) string {
	return cmp.Or(slugify(id), fmt.Sprintf("f%d", index+1))
}

func slugify(id string) string {
	return strings.Trim(reStreamID.ReplaceAllString(strings.ToLower(strings.TrimSpace(id)), "-"), "-")
}

func uniqueStreamID(base string, taken map[string]bool) string {
	id := base
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	taken[id] = true
	return id
}

func slugs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if slug := slugify(id); slug != "" {
			out = append(out, slug)
		}
	}
	return out
}

func trimmedPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p = strings.TrimPrefix(strings.TrimSpace(p), "./"); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pruneDependencies drops dependencies on unknown fronts and on the front itself.
func pruneDependencies(streams []Stream) []Stream {
	known := map[string]bool{}
	for _, s := range streams {
		known[s.ID] = true
	}
	for i := range streams {
		var deps []string
		for _, d := range streams[i].DependsOn {
			if known[d] && d != streams[i].ID && !slices.Contains(deps, d) {
				deps = append(deps, d)
			}
		}
		streams[i].DependsOn = deps
	}
	return streams
}

// coverAgents gives every selected implementer the architect left without a
// front a front of its own, so splitting the work never drops an agent.
func coverAgents(streams []Stream, plan Plan, stages []Stage) []Stream {
	if len(streams) == 0 {
		return nil
	}
	taken := map[string]bool{}
	for _, s := range streams {
		taken[s.ID] = true
	}
	for _, stage := range stages {
		hasFront := slices.ContainsFunc(streams, func(s Stream) bool { return s.Agent == stage.ID })
		if stage.Implementer && plan.Includes(stage.ID) && !hasFront {
			streams = append(streams, Stream{ID: uniqueStreamID(stage.ID, taken), Agent: stage.ID, Title: stage.Title})
		}
	}
	return streams
}

// streamWaves orders the fronts in waves: a front runs once its dependencies
// ran in an earlier wave, and never beside a front claiming one of its files.
// A dependency cycle is broken in declaration order.
func streamWaves(streams []Stream) [][]Stream {
	done := map[string]bool{}
	remaining := slices.Clone(streams)
	var waves [][]Stream
	for len(remaining) > 0 {
		wave, rest := nextWave(remaining, done)
		if len(wave) == 0 {
			wave, rest = remaining[:1], remaining[1:]
		}
		for _, s := range wave {
			done[s.ID] = true
		}
		waves = append(waves, wave)
		remaining = rest
	}
	return waves
}

func nextWave(streams []Stream, done map[string]bool) (wave, rest []Stream) {
	for _, s := range streams {
		isReady := !slices.ContainsFunc(s.DependsOn, func(d string) bool { return !done[d] })
		if isReady && !sharesFiles(s, wave) {
			wave = append(wave, s)
		} else {
			rest = append(rest, s)
		}
	}
	return wave, rest
}

func sharesFiles(s Stream, others []Stream) bool {
	return slices.ContainsFunc(others, func(o Stream) bool {
		return slices.ContainsFunc(s.Files, func(a string) bool {
			return slices.ContainsFunc(o.Files, func(b string) bool { return pathsOverlap(a, b) })
		})
	})
}

// pathsOverlap reports whether two paths are the same or one contains the other.
func pathsOverlap(a, b string) bool {
	a, b = strings.TrimSuffix(a, "/"), strings.TrimSuffix(b, "/")
	return a == b || strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/")
}

// parallelWaves returns the plan's waves when at least one of them runs
// fronts in parallel; otherwise the implementers run as regular stages.
func (p Plan) parallelWaves() [][]Stream {
	waves := streamWaves(p.Streams)
	if !slices.ContainsFunc(waves, func(w []Stream) bool { return len(w) > 1 }) {
		return nil
	}
	return waves
}

// streamJob is one front ready to run.
type streamJob struct {
	stream Stream
	stage  Stage
	ref    stepRef
	req    provider.Request
	dir    string // worktree root, when isolated
	work   string // repository directory the agent works in
}

// runStreams develops the plan's fronts wave by wave. Fronts of a wave run in
// parallel, each in its own git worktree, and are merged into the story
// branch when the wave ends.
func (o *Orchestrator) runStreams(ctx context.Context, ss *session, st *storyState, anchor stepRef, waves [][]Stream) bool {
	o.insertStreamSteps(st.si, anchor.id, waves)
	o.coverImplementers(st.si, st.plan)
	o.logf(LevelInfo, "Plano dividido em %d frente(s) e %d onda(s); %s.", len(st.plan.Streams), len(waves), streamModeLabel(ss))
	if ss.policy.Candidates > 1 {
		o.logf(LevelInfo, "Frentes paralelas ativas: cada frente faz uma única implementação (sem best-of-N).")
	}
	for _, wave := range waves {
		if !o.runWave(ctx, ss, st, o.streamJobs(ss, st, wave)) {
			return false
		}
	}
	return true
}

func canIsolate(ss *session) bool {
	return ss.git != nil && !ss.dry
}

func streamModeLabel(ss *session) string {
	switch {
	case canIsolate(ss):
		return "frentes da mesma onda em paralelo, cada uma em worktree git própria"
	case ss.dry:
		return "simulação com frentes em paralelo"
	}
	return "sem git (desativado), frentes em sequência"
}

func (o *Orchestrator) insertStreamSteps(si int, beforeID string, waves [][]Stream) {
	var steps []Step
	for w, wave := range waves {
		for _, s := range wave {
			stage := o.stageByID(s.Agent)
			steps = append(steps, Step{
				ID: streamStepID(s), Title: "Frente: " + s.Title, Role: stage.Role, Tier: stage.Tier,
				Status: StepPending, Kind: "stream", Agent: stage.ID,
				Note: fmt.Sprintf("Onda %d · %s", w+1, stage.Title),
			})
		}
	}
	o.mu.Lock()
	story := &o.run.Stories[si]
	at := max(stepIndex(story.Steps, beforeID), 0)
	story.Steps = slices.Insert(story.Steps, at, steps...)
	o.mu.Unlock()
	o.notify()
}

// coverImplementers hides the implementer stages whose work the fronts took over.
func (o *Orchestrator) coverImplementers(si int, plan Plan) {
	o.mu.Lock()
	steps := o.run.Stories[si].Steps
	for _, stage := range o.stages {
		if !stage.Implementer || (stage.Selectable && !plan.Includes(stage.ID)) {
			continue
		}
		if i := stepIndex(steps, stage.ID); i >= 0 {
			steps[i].Status, steps[i].Note = StepOmitted, streamCoveredNote
		}
	}
	o.mu.Unlock()
	o.notify()
}

func (o *Orchestrator) stageByID(id string) Stage {
	if i := slices.IndexFunc(o.stages, func(s Stage) bool { return s.ID == id }); i >= 0 {
		return o.stages[i]
	}
	return Stage{ID: id, Title: id, Role: id}
}

// streamJobs builds the requests of a wave. Every front sees the steps that
// ran before the wave, including the fronts of earlier waves.
func (o *Orchestrator) streamJobs(ss *session, st *storyState, wave []Stream) []streamJob {
	previous := o.stepsBefore(stepRef{st.si, streamStepID(wave[0])})
	jobs := make([]streamJob, len(wave))
	for i, s := range wave {
		stage := o.stageByID(s.Agent)
		jobs[i] = streamJob{stream: s, stage: stage, ref: stepRef{st.si, streamStepID(s)}, work: ss.repo}
		jobs[i].req = provider.Request{
			Role:    stage.Role + " · " + s.Title,
			System:  stage.System,
			Prompt:  buildStreamPrompt(stage, s, wave, st.sc, ss.report, previous),
			Tier:    stage.Tier,
			WorkDir: ss.repo,
		}
	}
	return jobs
}

func buildStreamPrompt(stage Stage, s Stream, wave []Stream, sc storyContext, discovery *workspace.Report, previous []Step) string {
	var b strings.Builder
	b.WriteString(buildPrompt(stage, sc, discovery, previous))
	fmt.Fprintf(&b, "\n\n## Sua frente: %s (`%s`)\n\n", s.Title, s.ID)
	if s.Scope != "" {
		b.WriteString(s.Scope + "\n\n")
	}
	if len(s.Files) > 0 {
		fmt.Fprintf(&b, "Arquivos desta frente: %s.\n", strings.Join(quoteAll(s.Files), ", "))
	}
	if len(s.DependsOn) > 0 {
		fmt.Fprintf(&b, "Depende de: %s (já integradas no repositório).\n", strings.Join(s.DependsOn, ", "))
	}
	for _, other := range wave {
		if other.ID != s.ID {
			fmt.Fprintf(&b, "Em paralelo, outro agente faz \"%s\"%s: não mexa nisso.\n", other.Title, filesSuffix(other.Files))
		}
	}
	b.WriteString("Implemente só esta frente. O contrato com as outras frentes é o EDD.")
	return b.String()
}

func filesSuffix(files []string) string {
	if len(files) == 0 {
		return ""
	}
	return " (" + strings.Join(quoteAll(files), ", ") + ")"
}

func (o *Orchestrator) runWave(ctx context.Context, ss *session, st *storyState, jobs []streamJob) bool {
	switch {
	case len(jobs) == 1 || (!ss.dry && !canIsolate(ss)):
		return o.runWaveInSequence(ctx, ss, st, jobs)
	case ss.dry:
		return o.runWaveShared(ctx, ss, st, jobs)
	}
	return o.runWaveIsolated(ctx, ss, st, jobs)
}

func (o *Orchestrator) runWaveInSequence(ctx context.Context, ss *session, st *storyState, jobs []streamJob) bool {
	for _, job := range jobs {
		resp, ok := o.runStage(ctx, job.ref, job.req)
		if !ok || !o.afterAgent(ctx, ss, st, job.ref) {
			return false
		}
		o.logf(LevelSuccess, "Frente \"%s\" concluída via %s.", job.stream.Title, resp.Name)
	}
	return true
}

// runWaveShared runs the fronts in parallel in the repository itself; used in
// simulation, where agents change nothing.
func (o *Orchestrator) runWaveShared(ctx context.Context, ss *session, st *storyState, jobs []streamJob) bool {
	if !o.executeWave(ctx, jobs) {
		return false
	}
	for _, job := range jobs {
		if !o.afterAgent(ctx, ss, st, job.ref) {
			return false
		}
	}
	return true
}

func (o *Orchestrator) runWaveIsolated(ctx context.Context, ss *session, st *storyState, jobs []streamJob) bool {
	prefix, err := ss.git.Prefix(ctx)
	if err != nil {
		o.logf(LevelWarn, "Worktrees indisponíveis (%v); frentes desta onda em sequência.", err)
		return o.runWaveInSequence(ctx, ss, st, jobs)
	}
	cleanup, err := o.addWorktrees(ctx, ss, jobs, prefix)
	defer cleanup()
	if err != nil {
		o.logf(LevelWarn, "Worktrees indisponíveis (%v); frentes desta onda em sequência.", err)
		return o.runWaveInSequence(ctx, ss, st, resetWorkDirs(jobs, ss.repo))
	}
	o.logf(LevelInfo, "Onda com %d frentes em paralelo, cada uma em worktree isolada.", len(jobs))
	if !o.executeWave(ctx, jobs) {
		return false
	}
	for _, job := range jobs {
		if !o.mergeStream(ctx, ss, st, job, prefix) {
			return false
		}
	}
	return true
}

// addWorktrees gives every job its own worktree of the story branch's HEAD.
// The returned cleanup removes them and is safe to call on error.
func (o *Orchestrator) addWorktrees(ctx context.Context, ss *session, jobs []streamJob, prefix string) (func(), error) {
	tmp, err := os.MkdirTemp("", "faztudo-streams-")
	if err != nil {
		return func() {}, err
	}
	cleanup := func() {
		for _, job := range jobs {
			if job.dir != "" {
				_ = ss.git.RemoveWorktree(context.WithoutCancel(ctx), job.dir)
			}
		}
		os.RemoveAll(tmp)
	}
	for i := range jobs {
		dir := filepath.Join(tmp, jobs[i].stream.ID)
		if err := ss.git.AddWorktree(ctx, dir); err != nil {
			return cleanup, fmt.Errorf("worktree da frente %s: %w", jobs[i].stream.ID, err)
		}
		jobs[i].dir, jobs[i].work = dir, filepath.Join(dir, prefix)
		jobs[i].req.WorkDir = jobs[i].work
		// The prompt names the directory the agent works in, never the repository.
		jobs[i].req.Prompt = strings.Replace(jobs[i].req.Prompt, "`"+ss.repo+"`", "`"+jobs[i].work+"`", 1)
		linkDependencies(ss.repo, jobs[i].work, ss.git.Nested())
	}
	return cleanup, nil
}

func resetWorkDirs(jobs []streamJob, repo string) []streamJob {
	out := slices.Clone(jobs)
	for i := range out {
		out[i].work, out[i].req.WorkDir = repo, repo
	}
	return out
}

// executeWave runs every job concurrently and stops the run on the first
// failure, after all of them returned.
func (o *Orchestrator) executeWave(ctx context.Context, jobs []streamJob) bool {
	errs := make([]error, len(jobs))
	elapsed := make([]time.Duration, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			o.markRunning(job.ref, started)
			resp, err := o.executeRetrying(ctx, job.ref, job.req)
			elapsed[i], errs[i] = time.Since(started), err
			if err == nil {
				o.recordDone(job.ref, resp, elapsed[i])
				o.logf(LevelSuccess, "Frente \"%s\" concluída via %s.", job.stream.Title, resp.Name)
			}
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			o.stopOnError(ctx, jobs[i].ref, elapsed[i], err)
			return false
		}
	}
	return true
}

// mergeStream commits a front's worktree and merges it into the story branch.
// A front that cannot be committed, or that conflicts with the fronts merged
// before it, is redone in the repository, on top of their combined result.
func (o *Orchestrator) mergeStream(ctx context.Context, ss *session, st *storyState, job streamJob, prefix string) bool {
	msg := fmt.Sprintf("faztudo(%s): frente %s", storyLabel(st.sc.Story), job.stream.Title)
	work := vcs.At(job.work)
	commit, err := work.CommitAll(ctx, msg, dependencyPaths(work.Nested())...)
	if err != nil {
		o.logf(LevelWarn, "Commit da frente \"%s\" falhou (%v); refazendo no repositório da US.", job.stream.Title, err)
		return o.redoStream(ctx, ss, st, job, "")
	}
	if commit == nil {
		return true
	}
	if path := protectedChange(commit, prefix, ss.guard.Files()); path != "" {
		o.failStep(job.ref, 0, fmt.Errorf("o agente alterou arquivo de entrada protegido: %s", path))
		return false
	}
	if err := ss.git.Merge(ctx, commit.Hash, msg); err != nil {
		o.logf(LevelWarn, "Frente \"%s\" conflita com outra frente (%v); refazendo sobre o resultado integrado.", job.stream.Title, err)
		return o.redoStream(ctx, ss, st, job, conflictNote)
	}
	o.updateStep(job.ref, func(s *Step) { s.Commit = commit })
	o.logf(LevelInfo, "Commit %s: frente \"%s\" integrada (%d arquivo(s)).", commit.Hash, job.stream.Title, len(commit.Files))
	return true
}

func protectedChange(c *vcs.Commit, prefix string, protected []string) string {
	for _, f := range c.Files {
		if path := strings.TrimPrefix(f.Path, prefix); slices.Contains(protected, path) {
			return path
		}
	}
	return ""
}

const conflictNote = "\n\n[CONFLITO] Sua implementação anterior desta frente conflitou com outra frente paralela já integrada. " +
	"Refaça a frente sobre o código atual, preservando o que as outras frentes entregaram."

func (o *Orchestrator) redoStream(ctx context.Context, ss *session, st *storyState, job streamJob, note string) bool {
	job.req.WorkDir = ss.repo
	job.req.Prompt = strings.Replace(job.req.Prompt, "`"+job.work+"`", "`"+ss.repo+"`", 1) + note
	_, ok := o.runStage(ctx, job.ref, job.req)
	return ok && o.afterAgent(ctx, ss, st, job.ref)
}
