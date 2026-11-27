// Package pipeline runs the multi-agent engineering flow and keeps an
// observable snapshot of its progress.
package pipeline

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/scm"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/vcs"
	"llm-orchestrator/internal/workspace"
)

// ErrAlreadyRunning is returned by Start while a run is in progress.
var ErrAlreadyRunning = errors.New("já existe uma execução em andamento")

// ErrNoStories is returned by Start when no story was given.
var ErrNoStories = errors.New("nenhuma User Story informada")

// ErrNoFailedStep is returned when there is no failed stage that can be
// resumed from the last execution.
var ErrNoFailedStep = errors.New("nenhuma etapa com falha para refazer")

// flowOffNote marks a stage the configured flow turns off.
const flowOffNote = "Desativada no fluxo configurado"

const (
	maxLogs = 500
	// maxFixRounds is how many fix-and-verify rounds a gate may trigger
	// before the story moves on with the issues left as pending.
	maxFixRounds = 2
	// agentAttempts is how many times a failing agent call is tried before
	// the run stops; the CLI may crash, time out or lose the network.
	agentAttempts   = 3
	agentRetryDelay = 20 * time.Second
)

// Executor dispatches a task to an LLM provider.
type Executor interface {
	Execute(ctx context.Context, req provider.Request, logf provider.Logf) (*provider.Response, error)
	SetDryRun(bool)
}

// Preflighter is an Executor that can tell, before a run starts, whether
// its providers can serve the run's policy (provider.Router does).
type Preflighter interface {
	Preflight(ctx context.Context, need provider.Requirements) ([]string, error)
}

// preflight refuses a real run no provider can serve and returns what will
// work in a weaker way, to be logged once the run starts.
func (o *Orchestrator) preflight(opts StartOptions) ([]string, error) {
	p, ok := o.exec.(Preflighter)
	if !ok || opts.DryRun {
		return nil, nil
	}
	policy := o.ws.Policy()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	warnings, err := p.Preflight(ctx, provider.Requirements{StrictCommands: policy.StrictCommands, MaxTurns: policy.MaxTurns > 0})
	if err != nil {
		return nil, fmt.Errorf("verificação dos providers: %w", err)
	}
	return warnings, nil
}

// Orchestrator runs one pipeline at a time and notifies observers on change.
type Orchestrator struct {
	// baselines caches the baselines by commit and commands, so the stories
	// of a wave starting from the same commit share one.
	baselines  sync.Map
	baselineMu sync.Mutex
	// skillOverrides are the run's repository complements to the skills.
	skillOverrides map[string]SkillOverride
	// appVersion goes into every run's manifest.
	appVersion string
	// scmFor finds the source host of a repository, for review mode.
	scmFor func(ctx context.Context, dir string) (scm.Provider, error)
	// agentSlots bound the run's agent calls at once.
	agentSlots *fairSlots
	// lanes are the stories running side by side: a failure of one of them
	// stops that story, not the run, unless it is global.
	lanes    map[int]bool
	exec     Executor
	ws       *workspace.Manager
	stages   []Stage
	onChange func()
	logger   *slog.Logger
	runCheck func(ctx context.Context, dir string, c quality.Command, timeout time.Duration) quality.Result
	// sweepStacks and lockStackSlot keep disposable test stacks in check.
	sweepStacks func(ctx context.Context, roots, patterns []string) ([]string, error)
	// lockResource holds a named shared resource across processes and lanes.
	lockResource  func(ctx context.Context, name string, onWait func()) (func(), error)
	lockStackSlot func(ctx context.Context, onWait func()) (func(), error)
	// attempts and retryDelay control how failing agent calls are retried.
	attempts   int
	retryDelay time.Duration

	// journalDir keeps each repository's last run on disk; isJournalOwner is
	// set once this window starts a run, the only case in which it writes.
	journalDir     string
	journalDirty   chan struct{}
	journalMu      sync.Mutex
	isJournalOwner bool

	// usagePath is the token log (tokens.jsonl); see SetUsageLog.
	usageMu   sync.Mutex
	usagePath string

	// baseStages are the stages as given; stages and texts add the customized
	// prompts, resolved when a run starts so a run never changes midway.
	baseStages []Stage
	texts      promptTexts

	mu      sync.RWMutex
	prompts map[string]string
	limits  agentLimits
	run     Run
	last    StartOptions
	cancel  context.CancelFunc
	done    chan struct{}
}

// defaultSweepStacks and defaultLockStackSlot reach Docker and the machine-wide
// lock; tests replace them.
var (
	defaultSweepStacks   = quality.SweepStacks
	defaultLockStackSlot = workspace.LockStackSlot
)

// New creates an orchestrator. The discovery stage always runs first (or is
// reused from the session cache), followed by stages. onChange is called
// (without locks held) after every state mutation and must not block.
func New(exec Executor, ws *workspace.Manager, stages []Stage, onChange func(), logger *slog.Logger) *Orchestrator {
	o := &Orchestrator{exec: exec, ws: ws, baseStages: stages, stages: resolveStages(stages, nil), texts: resolveTexts(nil),
		onChange: onChange, logger: logger, runCheck: quality.Run,
		sweepStacks: defaultSweepStacks, lockStackSlot: defaultLockStackSlot, lockResource: workspace.LockResource,
		attempts: agentAttempts, retryDelay: agentRetryDelay,
		scmFor: func(ctx context.Context, dir string) (scm.Provider, error) {
			return scm.Detect(ctx, dir, scm.ExecRunner)
		}}
	o.run.Discovery = newStep(DiscoveryStage)
	o.run.Stories = []StoryRun{{Status: StepPending, Steps: o.freshSteps()}}
	o.run.Current = -1
	o.run.Logs = []LogEntry{{Time: time.Now(), Level: LevelInfo, Message: "Sistema pronto. Configure o repositório e informe uma User Story."}}
	return o
}

// Snapshot returns a deep copy of the current run.
func (o *Orchestrator) Snapshot() Run {
	o.mu.RLock()
	defer o.mu.RUnlock()
	r := o.run
	r.Stories = slices.Clone(o.run.Stories)
	for i := range r.Stories {
		r.Stories[i].Steps = slices.Clone(r.Stories[i].Steps)
	}
	r.Logs = slices.Clone(o.run.Logs)
	if o.run.Cleanup != nil {
		c := *o.run.Cleanup
		c.Baseline, c.Final = slices.Clone(c.Baseline), slices.Clone(c.Final)
		c.Reasons, c.Changed = slices.Clone(c.Reasons), slices.Clone(c.Changed)
		r.Cleanup = &c
	}
	if o.run.Analysis != nil {
		analysis := *o.run.Analysis
		r.Analysis = &analysis
	}
	r.Integrations = slices.Clone(o.run.Integrations)
	r.Resume = o.resumePointLocked()
	return r
}

// busyLocked reports whether a run is active or still winding down: parallel
// stories may be stopping after the run already finished.
func (o *Orchestrator) busyLocked() bool {
	if o.run.Running {
		return true
	}
	if o.done == nil {
		return false
	}
	select {
	case <-o.done:
		return false
	default:
		return true
	}
}

// SetRepository changes the target repository. It is refused during a run.
func (o *Orchestrator) SetRepository(path string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.busyLocked() {
		return "", ErrAlreadyRunning
	}
	before := o.ws.Path()
	resolved, err := o.ws.SetPath(path)
	if err == nil && resolved != before {
		o.loadJournalLocked(resolved)
	}
	return resolved, err
}

// Start launches a new run in the background. The simulation mode is applied
// only once the run is accepted, so a rejected start never alters a live run.
func (o *Orchestrator) Start(opts StartOptions) error {
	if len(opts.Stories) == 0 {
		return ErrNoStories
	}
	o.mu.RLock()
	busy := o.busyLocked()
	o.mu.RUnlock()
	if busy {
		return ErrAlreadyRunning
	}
	// Outside the lock: detecting CLI versions runs them.
	warnings, err := o.preflight(opts)
	if err != nil {
		return err
	}
	manifest := o.buildManifest(context.Background(), opts)
	o.mu.Lock()
	if o.busyLocked() {
		o.mu.Unlock()
		return ErrAlreadyRunning
	}
	repo := o.ws.Path()
	if repo == "" {
		o.mu.Unlock()
		return workspace.ErrNotConfigured
	}
	unlock, err := workspace.LockRepository(repo)
	if err != nil {
		o.mu.Unlock()
		return err
	}
	customized := o.usePrompts()
	if opts.Rediscover {
		o.ws.Invalidate()
	}
	o.exec.SetDryRun(opts.DryRun)
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	o.done = make(chan struct{})
	runs := make([]StoryRun, len(opts.Stories))
	for i, st := range opts.Stories {
		runs[i] = StoryRun{ID: st.ID, Title: st.Title, Status: StepPending, Steps: o.freshSteps()}
	}
	o.run = Run{
		Running:    true,
		Source:     opts.Source,
		Repository: repo,
		DryRun:     opts.DryRun,
		StartedAt:  &now,
		Discovery:  newStep(DiscoveryStage),
		Stories:    runs,
		Current:    -1,
		Manifest:   &manifest,
		ID:         newRunID(now),
	}
	if len(runs) > 1 {
		analysis := newStep(AnalysisStage)
		o.run.Analysis = &analysis
	}
	o.last = opts
	o.isJournalOwner = true
	done := o.done
	o.mu.Unlock()

	mode := "execução real"
	if opts.DryRun {
		mode = "simulação"
	}
	o.logf(LevelInfo, "Execução iniciada em %s: %d US (origem: %s, modo: %s).", repo, len(opts.Stories), opts.Source, mode)
	o.logger.Info("pipeline started", "repository", repo, "stories", len(opts.Stories), "source", opts.Source, "dry_run", opts.DryRun)
	o.logCustomPrompts(customized)
	for _, w := range warnings {
		o.logf(LevelWarn, "%s", w)
	}

	go func() {
		defer close(done)
		defer unlock()
		defer cancel()
		defer o.sweepRun(repo, opts.DryRun)
		o.sweepRun(repo, opts.DryRun)
		o.execute(ctx, repo, opts)
	}()
	return nil
}

// RetryFailed resumes the failed story at the stage that owns the failure.
// Completed stages before it are kept as context; generated fix/verify steps
// from that stage onward are discarded and will be rebuilt by the gate.
func (o *Orchestrator) RetryFailed(story int, stepID string) error {
	o.mu.RLock()
	last := o.last
	o.mu.RUnlock()
	var cur RunManifest
	if len(last.Stories) > 0 {
		cur = o.buildManifest(context.Background(), last)
	}
	o.mu.Lock()
	if o.busyLocked() {
		o.mu.Unlock()
		return ErrAlreadyRunning
	}
	if o.run.Kind == RunCleanup {
		o.mu.Unlock()
		return ErrCleanupNotResumable
	}
	if story == -1 && o.isResumableLocked() && len(o.last.Stories) > 0 && o.runStepStopped(stepID) {
		opts := o.last
		o.mu.Unlock()
		return o.Start(opts)
	}
	if !o.isResumableLocked() || story < 0 || story >= len(o.run.Stories) || len(o.last.Stories) != len(o.run.Stories) {
		o.mu.Unlock()
		return ErrNoFailedStep
	}

	failed := stepIndex(o.run.Stories[story].Steps, stepID)
	if failed < 0 || !o.isStoppedLocked(o.run.Stories[story].Steps[failed].Status) {
		o.mu.Unlock()
		return ErrNoFailedStep
	}
	baseID := retryStageID(o.run.Stories[story].Steps[failed])
	stageAt := slices.IndexFunc(o.stages, func(s Stage) bool { return s.ID == baseID })
	if stageAt < 0 {
		o.mu.Unlock()
		return ErrNoFailedStep
	}
	// Results saved before the stop hold only while what they depend on did.
	invs := o.invalidations(o.run.Manifest, cur, story)
	if why, moved := branchMoved(context.Background(), o.ws.Path(), o.run.Stories[story]); moved && !o.run.DryRun {
		if at := slices.IndexFunc(o.stages, func(s Stage) bool { return s.Verifier }); at >= 0 {
			invs = append(invs, Invalidation{Reason: why, From: at, Stage: o.stages[at].ID})
		}
	}
	requested := stageAt
	stageAt = earliest(stageAt, invs)
	o.run.ResumeChecks = invs
	o.run.Manifest = &cur

	unlock, err := workspace.LockRepository(o.ws.Path())
	if err != nil {
		o.mu.Unlock()
		return err
	}

	// Keep stable artifacts before the retry point and rebuild the rest of the
	// observable plan. This also removes stale dynamic correction rounds.
	previous := o.run.Stories[story]
	fresh := o.freshSteps()
	for i := 0; i < stageAt; i++ {
		if old := stepIndex(previous.Steps, fresh[i].ID); old >= 0 {
			fresh[i] = previous.Steps[old]
		}
	}
	if previous.Plan != nil {
		for i := stageAt; i < len(fresh); i++ {
			if o.stages[i].Selectable && !previous.Plan.Includes(o.stages[i].ID) {
				fresh[i].Status = StepOmitted
				fresh[i].Note = "Não selecionado pelo arquiteto"
			}
		}
	}
	o.run.Stories[story].Steps = fresh
	o.run.Stories[story].Status = StepRunning
	o.run.Stories[story].Validation = ""
	o.run.Stories[story].Integration, o.run.Stories[story].IntegrationNote = "", ""
	started := time.Now()
	o.run.Stories[story].StartedAt = &started
	o.run.Stories[story].DurationMS = 0
	for i := range o.run.Stories {
		if i == story || isFinished(o.run.Stories[i].Status) && !skippedByDependency(o.run.Stories[i]) {
			continue // delivered stories of a parallel wave are kept
		}
		o.run.Stories[i].Status = StepPending
		o.run.Stories[i].Validation = ""
		o.run.Stories[i].Integration, o.run.Stories[i].IntegrationNote = "", ""
		o.run.Stories[i].StartedAt = nil
		o.run.Stories[i].DurationMS = 0
		o.run.Stories[i].Plan = nil
		o.run.Stories[i].Trace = nil
		o.run.Stories[i].Branch = ""
		o.run.Stories[i].Steps = o.freshSteps()
	}
	o.run.Running = true
	o.run.Result = ""
	o.run.Validation = ""
	o.run.FinishedAt = nil
	o.run.Current = story
	o.isJournalOwner = true
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	o.done = make(chan struct{})
	done := o.done
	opts := o.last
	customized := o.usePrompts()
	o.mu.Unlock()
	o.notify()
	o.logCustomPrompts(customized)
	for _, inv := range invs {
		o.logf(LevelWarn, "Retomada: %s; refazendo a partir de %s.", inv.Reason, o.stages[inv.From].Title)
	}
	if stageAt < requested {
		o.logf(LevelInfo, "Resultados salvos antes de %s continuam valendo; os seguintes são refeitos.", o.stages[stageAt].Title)
	}
	o.logf(LevelInfo, "Refazendo %q a partir de %s.", previous.Title, o.stages[stageAt].Title)

	go func() {
		defer close(done)
		defer unlock()
		defer cancel()
		defer o.sweepRun(o.ws.Path(), opts.DryRun)
		o.sweepRun(o.ws.Path(), opts.DryRun)
		o.retryExecute(ctx, opts, story, stageAt, started)
	}()
	return nil
}

// isResumableLocked reports whether the last run stopped before the end:
// it failed, or it was canceled (stopped, or the app closed mid-run).
func (o *Orchestrator) isResumableLocked() bool {
	return !o.run.Running && (o.run.Result == ResultFailed || o.run.Result == ResultCanceled)
}

// isStoppedLocked reports whether a step with status is where the run stopped:
// the failed step, or in a canceled run the interrupted (canceled) one.
func (o *Orchestrator) isStoppedLocked(status StepStatus) bool {
	return status == StepFailed || (o.run.Result == ResultCanceled && status == StepCanceled)
}

// runStepStopped reports whether the run-level step id (discovery or
// analysis) is where the run stopped; resuming it starts the run again.
func (o *Orchestrator) runStepStopped(id string) bool {
	switch {
	case id == DiscoveryStage.ID:
		return o.isStoppedLocked(o.run.Discovery.Status)
	case id == AnalysisStage.ID && o.run.Analysis != nil:
		return o.isStoppedLocked(o.run.Analysis.Status)
	}
	return false
}

func retryStageID(step Step) string {
	if step.Kind == "stream" {
		return step.Agent
	}
	if step.Gate != "" {
		return step.Gate
	}
	if step.Kind == "verify" {
		if at := strings.Index(step.ID, "-verify-"); at > 0 {
			return step.ID[:at]
		}
	}
	return step.ID
}

// Stop cancels the active run. It reports whether a run was active.
func (o *Orchestrator) Stop() bool {
	o.mu.RLock()
	running, cancel := o.run.Running, o.cancel
	o.mu.RUnlock()
	if !running || cancel == nil {
		return false
	}
	cancel()
	return true
}

// Wait blocks until the active run (if any) has finished or ctx expires.
func (o *Orchestrator) Wait(ctx context.Context) error {
	o.mu.RLock()
	done := o.done
	o.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// session is what every story of a run shares.
type session struct {
	repo   string
	dry    bool
	policy workspace.Policy
	// patterns are the protected-file patterns, kept for worktree guards.
	patterns []string
	checks   []quality.Command
	guard    *quality.Guard
	git      *vcs.Repo
	report   *workspace.Report
	// instructions are the repository's agent instruction files.
	instructions string
	// bases are the branches the run started from; waves merge into them.
	bases map[string]string
}

func (ss *session) rules() rules {
	return rules{Checks: ss.checks, Protected: ss.guard.Files(), MaxLine: ss.policy.MaxLineLength}
}

// storyState is the working state of the story being developed.
type storyState struct {
	si         int
	sc         storyContext
	started    time.Time
	plan       Plan
	planText   string
	reqs       []quality.Requirement
	verifiedAt time.Time // when a verifier last approved the repository
	// verifiedKey is what the approved repository held (content and commands).
	verifiedKey string
	blocked     string // Passo 0 reason when the story cannot be developed yet
	pending     int    // issues a gate could not get fixed within its rounds
	// inconclusive counts gates that never produced a valid verdict.
	inconclusive int
	// risk is what the policy's risk rules found so far; it only grows.
	risk workspace.Risk
	// baseline is how the commands behaved before the story changed anything.
	baseline *Baseline
	// waiting counts the open product decisions Passo 0 found.
	waiting int
	// reused marks a story Passo 0 found done: only executable gates run.
	reused bool
	// change is the story's kind; changeReason justifies a trivial one.
	change, changeReason string
	// startCommit is the snapshot the story started from, where a bug's tests
	// must fail; repro is the last reproduction made.
	startCommit string
	repro       *Reproduction
	// acceptedAt is when the functional QA last accepted every scenario.
	acceptedAt time.Time
	// measured tells whether a benchmark ran for the performance gate, and
	// benchmarks is what it printed.
	measured     bool
	benchmarks   string
	benchResults []BenchResult
	// stageAt is the index of the stage running; late lists the planning
	// stages the risk required only after they had passed.
	stageAt int
	late    []string
	red     []string // commands that failed last, retried first
	skipped bool     // the last assessment left costlier commands out
	// lastFailures and stalls track whether a fix loop is getting anywhere.
	lastFailures int
	stalls       int
}

func (o *Orchestrator) execute(ctx context.Context, repo string, opts StartOptions) {
	ss, ok := o.prepare(ctx, repo, opts)
	if !ok {
		return
	}
	graph := o.updateGraph(ctx, ss)
	report, ok := o.discover(ctx, ss, graph)
	if !ok || !o.guardInputs(ss, discoveryRef) {
		return
	}
	report.Graph = graph
	ss.report = report
	if ss.policy.Candidates > 1 && !ss.dry && ss.git == nil {
		o.logf(LevelWarn, "Best-of-N precisa de git; cada implementador fará uma única implementação.")
	}

	waves, ok := o.analyzeSequence(ctx, ss, opts)
	if !ok || !o.runWaves(ctx, ss, opts, waves, nil) {
		return
	}
	o.finishSucceeded("Pipeline concluído. Todos os agentes entregaram seus artefatos.")
}

// closeStory records how a developed story ended: delivered, delivered with
// pending issues, or blocked in Passo 0.
func (o *Orchestrator) closeStory(ss *session, st *storyState, total int) {
	o.annotateNeeds(ss, st) // the diff may have raised the risk since the plan
	if o.invalidateAcceptance(ss, st) {
		o.logf(LevelWarn, "%s: código alterado depois da aceitação funcional; os cenários precisam ser aceitos de novo.", st.sc.Story.Title)
	}
	status := StepDone
	switch {
	case st.waiting > 0:
		status = StepWaiting
	case st.blocked != "":
		status = StepSkipped
		o.logf(LevelWarn, "%s bloqueada no Passo 0: %s", st.sc.Story.Title, st.blocked)
	case st.pending > 0:
		status = StepWarning
		o.logf(LevelWarn, "%s entregue com %d pendência(s) não resolvida(s).", st.sc.Story.Title, st.pending)
	case st.inconclusive > 0:
		status = StepWarning
		o.logf(LevelWarn, "%s entregue sem veredito válido em %d portão(ões); validação inconclusiva.", st.sc.Story.Title, st.inconclusive)
	case total > 1:
		o.logf(LevelSuccess, "%s entregue (%d de %d).", st.sc.Story.Title, st.si+1, total)
	}
	var missingNote string
	o.updateStory(st.si, func(r *StoryRun) {
		r.Status = status
		r.DurationMS = time.Since(st.started).Milliseconds()
		r.Validation = storyValidation(r.Steps)
		// Nothing was built for a reused story: the risk's stages, which
		// judge a change, do not apply; its executable gates still did.
		if st.change == ChangeBug && st.repro != nil && (st.repro.Status == ReproUnclear || st.repro.Status == ReproUnverified) {
			// The fix passes, but nothing showed the bug it fixes.
			r.Validation = worseValidation(r.Validation, ValidationInconclusive)
			if r.Status == StepDone {
				r.Status = StepWarning
			}
		}
		if missing := o.missingRequired(st, r.Steps); len(missing) > 0 && st.blocked == "" && st.waiting == 0 && !st.reused {
			r.Validation = worseValidation(r.Validation, ValidationInconclusive)
			missingNote = strings.Join(missing, ", ")
		}
		if st.blocked != "" || st.waiting > 0 || o.run.DryRun {
			// Nothing was checked for real: a blocked story was not built and
			// a simulation runs no command nor model.
			r.Validation = ValidationNotEvaluated
		}
		if slices.ContainsFunc(r.Acceptance, func(a ScenarioResult) bool { return a.Status == ScenarioInvalidated }) {
			r.Validation = worseValidation(r.Validation, ValidationInconclusive)
		}
		if (missingNote != "" || r.Validation == ValidationInconclusive) && r.Status == StepDone {
			r.Status = StepWarning
		}
	})
	if missingNote != "" {
		o.logf(LevelWarn, "%s: etapa(s) obrigatória(s) pela política de risco sem resultado (%s); validação inconclusiva.", st.sc.Story.Title, missingNote)
	}
}

// finishSucceeded ends a run that went through every story, pointing out the
// ones left with pending issues.
// The run's Result only says it finished; Validation, set here, says whether
// the delivery was approved.
func (o *Orchestrator) finishSucceeded(msg string) {
	o.mu.Lock()
	var pending []string
	for _, st := range o.run.Stories {
		if st.Status == StepWarning {
			pending = append(pending, cmp.Or(st.ID, st.Title))
		}
	}
	var waiting []string
	for _, st := range o.run.Stories {
		if st.Status == StepWaiting {
			waiting = append(waiting, cmp.Or(st.ID, st.Title))
		}
	}
	validation := runValidation(o.run.Stories)
	o.run.Validation = validation
	dry := o.run.DryRun
	o.mu.Unlock()
	if len(waiting) > 0 {
		msg += fmt.Sprintf(" Aguardando decisão de produto em: %s.", strings.Join(waiting, ", "))
	}
	switch {
	case len(pending) > 0:
		o.finish(ResultSucceeded, LevelWarn, fmt.Sprintf("Pipeline concluído com pendências em: %s. Validação %s.", strings.Join(pending, ", "), validation.Label()))
	case dry:
		o.finish(ResultSucceeded, LevelSuccess, msg+" Simulação: nenhuma entrega foi validada.")
	case validation != ValidationPassed:
		o.finish(ResultSucceeded, LevelWarn, fmt.Sprintf("%s Validação %s.", msg, validation.Label()))
	default:
		o.finish(ResultSucceeded, LevelSuccess, msg)
	}
}

// retryExecute rebuilds the transient session around the repository as it was
// left by the failed attempt, then continues the failed story and the queue.
func (o *Orchestrator) retryExecute(ctx context.Context, opts StartOptions, story, stageAt int, started time.Time) {
	repo := o.ws.Path()
	ss := o.retrySession(ctx, repo, opts)
	if ss == nil {
		return
	}
	graph := o.updateGraph(ctx, ss)
	report, ok := o.discover(ctx, ss, graph) // the saved one, or a new one
	if !ok {
		return
	}
	report.Graph = graph
	ss.report = report

	waves := o.Snapshot().Waves
	if len(waves) == 0 {
		waves = sequentialWaves(len(opts.Stories))
	}
	from := slices.IndexFunc(waves, func(w []int) bool { return slices.Contains(w, story) })
	resume := &resumePoint{si: story, stageAt: stageAt, started: started}
	if stageAt < 0 {
		resume = nil // the story starts over, on a new branch
	}
	if !o.runWaves(ctx, ss, opts, waves[max(from, 0):], resume) {
		return
	}
	o.finishSucceeded("Pipeline concluído após refazer a etapa com falha.")
}

func (o *Orchestrator) retrySession(ctx context.Context, repo string, opts StartOptions) *session {
	o.exec.SetDryRun(opts.DryRun)
	policy := o.ws.Policy()
	ss := &session{repo: repo, dry: opts.DryRun, policy: policy, checks: o.ws.Checks(), instructions: workspace.ReadInstructions(repo)}
	o.useLimits(policy, ss.checks)
	o.useSlots(policy)
	ss.patterns = protectedPatterns(policy, opts)
	ss.guard = quality.NewGuard(repo, ss.patterns)
	if !opts.DryRun && policy.Git {
		g, _, err := vcs.Open(ctx, repo)
		if err != nil {
			o.logf(LevelWarn, "Versionamento indisponível ao refazer a etapa: %v.", err)
		} else {
			ss.git = g
			ss.bases = o.basesOfRun(policy)
		}
	}
	return ss
}

// runBases are the branches each repository started from, which waves merge
// into. A repository checked out on a FazTudo branch (a resumed interrupted
// run) has no known base unless the policy names one; it is then not merged.
func (o *Orchestrator) runBases(ctx context.Context, g *vcs.Repo, policy workspace.Policy) map[string]string {
	bases := g.CurrentBranches(ctx)
	for repo, branch := range bases {
		if !strings.HasPrefix(branch, vcs.BranchPrefix) {
			continue
		}
		if configured := policy.BaseBranches[repo]; configured != "" {
			bases[repo] = configured
			continue
		}
		delete(bases, repo)
		o.logf(LevelWarn, "Repositório %s começa em uma branch do FazTudo e não tem branch base configurada; o merge na base não será feito.", repo)
	}
	return bases
}

// basesOfRun are the branches the run being resumed started from: saved with
// the run, else the policy's base branches.
func (o *Orchestrator) basesOfRun(policy workspace.Policy) map[string]string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if len(o.run.Bases) > 0 {
		return maps.Clone(o.run.Bases)
	}
	return maps.Clone(policy.BaseBranches)
}

// protectedPatterns adds the loaded story document to the policy's
// protected patterns.
func protectedPatterns(policy workspace.Policy, opts StartOptions) []string {
	patterns := append(slices.Clone(policy.Protected), overridePattern)
	if name := path.Base(filepath.ToSlash(strings.TrimSpace(opts.DocumentName))); name != "." && name != "/" {
		patterns = append(slices.Clone(patterns), name)
	}
	return patterns
}

func (o *Orchestrator) restoreStoryState(si int, started time.Time, opts StartOptions, ss *session) *storyState {
	st := &storyState{
		si: si, started: started,
		sc:   o.newStoryContext(si, opts, ss),
		plan: fullPlan(o.stages, "default"),
		reqs: quality.ExtractRequirements(opts.Preamble + "\n\n" + opts.Stories[si].Content),
	}
	o.mu.RLock()
	runStory := o.run.Stories[si]
	if runStory.Risk != nil {
		st.risk = *runStory.Risk
	}
	st.baseline = runStory.Baseline
	st.change, st.changeReason = runStory.Change, runStory.ChangeReason
	st.startCommit, st.repro = runStory.StartCommit, runStory.Reproduction
	if runStory.Plan != nil {
		st.plan = *runStory.Plan
		// A plan saved before scenarios had IDs gets them as a new one would.
		st.plan.Acceptance = slices.Clone(st.plan.Acceptance)
		numberScenarios(st.plan.Acceptance)
	}
	steps := slices.Clone(runStory.Steps)
	o.mu.RUnlock()
	if i := stepIndex(steps, "arch"); i >= 0 {
		st.planText = steps[i].Output
	}
	if i := stepIndex(steps, "spec"); i >= 0 && steps[i].Output != "" {
		o.applySpec(st, &provider.Response{Output: steps[i].Output})
	}
	return st
}

// prepare snapshots the protected input files and, in real runs, puts the
// repository under git. A dirty working tree fails the run so the agents'
// commits never mix with the user's pending changes.
func (o *Orchestrator) prepare(ctx context.Context, repo string, opts StartOptions) (*session, bool) {
	policy := o.ws.Policy()
	ss := &session{repo: repo, dry: opts.DryRun, policy: policy, checks: o.ws.Checks(), instructions: workspace.ReadInstructions(repo)}
	o.useLimits(policy, ss.checks)
	o.useSlots(policy)
	if ss.instructions != "" {
		o.logf(LevelInfo, "Instruções do projeto (AGENTS.md/CLAUDE.md/Spec Kit) repassadas a todos os agentes.")
	}

	overrides := projectOverrides(repo)
	o.mu.Lock()
	o.skillOverrides = overrides
	o.mu.Unlock()
	for name, ov := range overrides {
		o.logf(LevelInfo, "Skill %s com complemento do projeto: %s (sha256 %s).", name, ov.Path, short(ov.Hash))
	}
	for _, p := range policy.Profiles {
		o.logf(LevelInfo, "Perfil de stack %s escolhido na política.", p)
	}

	ss.patterns = protectedPatterns(policy, opts)
	ss.guard = quality.NewGuard(repo, ss.patterns)
	if files := ss.guard.Files(); len(files) > 0 {
		o.mu.Lock()
		o.run.Protected = files
		o.mu.Unlock()
		o.logf(LevelInfo, "Arquivos de entrada protegidos: %s.", strings.Join(files, ", "))
	}
	if len(ss.checks) == 0 {
		o.logf(LevelWarn, "Nenhum comando de verificação configurado ou detectado; o portão executável fará só as checagens estáticas.")
	}
	o.warnRiskVersusFlow(policy)
	if opts.DryRun || !policy.Git {
		return ss, true
	}

	g, initialised, err := vcs.Open(ctx, repo)
	if err != nil {
		o.logf(LevelWarn, "Versionamento desativado nesta execução: %v.", err)
		return ss, true
	}
	if initialised {
		o.logf(LevelInfo, "Repositório git inicializado; o estado atual virou o commit inicial.")
	}
	dirty, err := g.DirtyPaths(ctx)
	if err == nil && len(dirty) > 0 {
		var saved []string
		saved, dirty, err = g.SaveInterrupted(ctx, dirty, "faztudo: salva trabalho de execução interrompida")
		if len(saved) > 0 {
			o.logf(LevelWarn, "Alterações de uma execução interrompida salvas em commit (%s); a execução continua a partir delas.",
				strings.Join(dirtyLabels(saved), ", "))
		}
	}
	if err != nil || len(dirty) > 0 {
		if err == nil {
			err = fmt.Errorf("%w (%s)", vcs.ErrDirty, strings.Join(dirtyLabels(dirty), ", "))
		}
		o.abortRemaining(-1, StepCanceled)
		o.finish(ResultFailed, LevelError, err.Error())
		return nil, false
	}
	if len(policy.BaseBranches) > 0 {
		if err := g.SwitchBranches(ctx, policy.BaseBranches); err != nil {
			o.abortRemaining(-1, StepCanceled)
			o.finish(ResultFailed, LevelError, fmt.Sprintf("Não foi possível trocar para a branch base: %v.", err))
			return nil, false
		}
		o.logf(LevelInfo, "Branch base: %s.", baseLabels(ctx, g))
	}
	if nested := g.Nested(); len(nested) > 0 {
		o.logf(LevelInfo, "Repositórios aninhados versionados junto: %s.", strings.Join(nested, ", "))
	}
	ss.git = g
	ss.bases = o.runBases(ctx, g, policy)
	o.mu.Lock()
	o.run.BaseBranch = g.Branch(ctx)
	o.run.Bases = ss.bases
	o.mu.Unlock()
	return ss, true
}

// baseLabels names the branch each repository starts from.
func baseLabels(ctx context.Context, g *vcs.Repo) string {
	labels := []string{"repositório principal em " + g.Branch(ctx)}
	for _, n := range g.Nested() {
		labels = append(labels, n+" em "+vcs.At(g.Path(n)).Branch(ctx))
	}
	return strings.Join(labels, ", ")
}

func dirtyLabels(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		if p == "." {
			p = "repositório principal"
		}
		out[i] = p
	}
	return out
}

// developStoryFrom runs the stages for one story from startAt: planner first,
// then only the selected agents, with fix rounds whenever a gate requests
// changes.
func (o *Orchestrator) developStoryFrom(ctx context.Context, ss *session, st *storyState, startAt int, createBranch bool) bool {
	if createBranch {
		if !o.startBranch(ctx, ss, st) {
			return false
		}
	}

	if startAt == 0 {
		o.mu.RLock()
		list := o.last.Stories
		o.mu.RUnlock()
		if built := o.dependencySnapshots(st.si, list); built != nil {
			o.updateStory(st.si, func(r *StoryRun) { r.BuiltOn = built })
		}
		if ss.git != nil {
			st.startCommit, _ = ss.git.Head(ctx)
			o.updateStory(st.si, func(r *StoryRun) { r.StartCommit = st.startCommit })
		}
		kind, _ := changeKind("", "", st.sc.Story.Title+"\n"+st.sc.Story.Content)
		o.setChange(st, kind, "")
		if !o.takeBaseline(ctx, ss, st) {
			return false
		}
	}
	o.planAudit(ss, st)
	o.assessStoryRisk(ss, st, nil)
	o.annotateNeeds(ss, st)
	streamed := false
	for stageIndex, stage := range o.stages {
		if stageIndex < startAt {
			continue
		}
		st.stageAt = stageIndex
		if stage.Gate || stage.Verifier {
			// The real diff may raise the risk set from the story's text.
			o.assessStoryRisk(ss, st, o.changedFiles(ss, st.started))
		}
		if stage.Selectable && !st.plan.Includes(stage.ID) {
			continue // already marked as omitted when the plan was applied
		}
		if ss.policy.StageDisabled(stage.ID) && !st.requiredByRisk(stage.ID) {
			continue // marked as omitted when the story's steps were created
		}
		if st.reused && !stage.Verifier {
			continue // skipped when Passo 0 found the story done
		}
		ref := stepRef{st.si, stage.ID}
		if waves := st.plan.parallelWaves(); stage.Implementer && waves != nil && !ss.policy.Sequential {
			// The first implementer runs every front; the others are covered.
			if !streamed && !o.runStreams(ctx, ss, st, ref, waves) {
				return false
			}
			streamed = true
			continue
		}
		if stage.Verifier {
			ok := false
			switch stage.Commands {
			case "migrations":
				ok = o.runCommandGate(ctx, ss, st, stage, ss.policy.MigrationChecks,
					"sem comandos de migration (`migration_checks`): integridade, compatibilidade e recuperação não foram testadas")
			default:
				ok = o.runVerifier(ctx, ss, st, stage)
			}
			if !ok {
				return false
			}
			continue
		}
		if stage.Measured {
			bench, alive := o.measure(ctx, ss, st)
			if !alive {
				return false
			}
			st.benchmarks = bench
		}

		var resp *provider.Response
		var ok bool
		lenses := activeLenses(stage.Lenses, st.plan)
		switch {
		case len(lenses) > 1 && ss.policy.ParallelLenses:
			resp, ok = o.runLenses(ctx, ss, st, stage, lenses, ref)
		case stage.Implementer && ss.policy.Candidates > 1 && canIsolate(ss):
			resp, ok = o.runCandidates(ctx, ss, st, stage, ref)
		default:
			resp, ok = o.runStage(ctx, ref, provider.Request{
				Role:     stage.Role,
				System:   stage.System,
				Prompt:   buildPrompt(stage, o.stageContext(ss, st, stage), ss.report, o.stepsBefore(ref)),
				Tier:     stage.Tier,
				WorkDir:  ss.repo,
				ReadOnly: stage.ReadOnly,
				Prefer:   reviewProvider(ss, stage),
			})
		}
		if !ok || !o.afterAgent(ctx, ss, st, ref) {
			return false
		}
		o.logf(LevelSuccess, "%s concluída via %s em %s.", stage.Title, resp.Name, resp.Duration.Round(time.Millisecond))

		switch {
		case stage.Assessor:
			if o.applyAssessment(st, ref, resp) {
				return true
			}
		case stage.Specifier:
			o.applySpec(st, resp)
		case stage.Planner:
			st.plan, st.planText = o.applyPlan(st.si, st.sc, resp), resp.Output
			o.enforceRisk(ss, st) // the architect may add stages, not drop mandatory ones
			o.enforceSpecialties(st)
			o.annotateNeeds(ss, st)
		case stage.Specialist:
			o.applySpecialist(st, ref, stage, resp)
		case stage.Gate:
			recheck := func(ctx context.Context, ref stepRef, issues []Issue, fixes []Step, round int) (evaluation, bool) {
				if ev, ok := o.checkFixes(ctx, ss, st, ref); !ok {
					return evaluation{}, false
				} else if ev != nil {
					return *ev, true
				}
				if stage.Measured {
					// The fix is judged on new measurements, not the old ones.
					bench, alive := o.measure(ctx, ss, st)
					if !alive {
						return evaluation{}, false
					}
					st.benchmarks = bench
				}
				resp, ok := o.runStage(ctx, ref, provider.Request{
					Role:     stage.Role,
					System:   stage.System,
					Prompt:   buildVerifyPrompt(o.texts.verifyTask, stage, o.stageContext(ss, st, stage), ss.report, issues, fixes, round),
					Tier:     stage.Tier,
					WorkDir:  ss.repo,
					ReadOnly: stage.ReadOnly,
					Prefer:   reviewProvider(ss, stage),
				})
				if !ok || !o.afterAgent(ctx, ss, st, ref) {
					return evaluation{}, false
				}
				return o.gateEvaluation(ss, st, stage, resp), true
			}
			if !o.enforceGate(ctx, ss, st, stage, o.gateEvaluation(ss, st, stage, resp), recheck) {
				return false
			}
		}
	}
	return true
}

// stageContext is the story's context as stage reads it: the functional QA
// also gets the executed evidence.
func (o *Orchestrator) stageContext(ss *session, st *storyState, stage Stage) storyContext {
	sc := st.sc
	if stage.Gate && !ss.dry {
		sc.Changed = o.changedFiles(ss, st.started)
	}
	switch {
	case stage.Acceptor:
		sc.Evidence = o.acceptanceEvidence(st)
	case stage.Measured:
		sc.Evidence = st.benchmarks
	}
	return sc
}

// planAudit stands in for the architect when the flow turns it off (audit of
// code already written): every enabled reviewer runs and nothing is built.
func (o *Orchestrator) planAudit(ss *session, st *storyState) {
	var planner *Stage
	for i := range o.stages {
		if o.stages[i].Planner {
			planner = &o.stages[i]
		}
	}
	if planner == nil || !ss.policy.StageDisabled(planner.ID) {
		return
	}
	plan := Plan{Source: "audit", Reason: "Auditoria do código existente: sem arquiteto nem desenvolvimento."}
	for _, s := range o.stages {
		if s.Selectable && !s.Implementer && !ss.policy.StageDisabled(s.ID) {
			plan.Agents = append(plan.Agents, s.ID)
		}
	}
	st.plan = plan
	o.updateStory(st.si, func(r *StoryRun) { r.Plan = &plan })
}

// startBranch creates the story's branch from the current HEAD, so stories of
// a sequence build on each other.
// maxBranchAttempts bounds the suffixes tried when a story's branch name is
// taken (two runs of the same story in the same second).
const maxBranchAttempts = 5

// startBranch puts the story on a branch of its own. Without one the run
// stops: developing on the base would put unverified work where only an
// approved candidate may go.
func (o *Orchestrator) startBranch(ctx context.Context, ss *session, st *storyState) bool {
	if ss.git == nil {
		return true
	}
	name := vcs.BranchName(storyLabel(st.sc.Story), time.Now())
	var err error
	for i := 1; i <= maxBranchAttempts; i++ {
		branch := name
		if i > 1 {
			branch = fmt.Sprintf("%s-%d", name, i)
		}
		if err = ss.git.CreateBranch(ctx, branch); err == nil {
			o.updateStory(st.si, func(r *StoryRun) { r.Branch = branch })
			o.logf(LevelInfo, "Branch %s criada para %s.", branch, st.sc.Story.Title)
			return true
		}
	}
	o.failRun(fmt.Errorf("não foi possível criar a branch de %s (%v); a US não é desenvolvida na branch base", st.sc.Story.Title, err))
	return false
}

func storyLabel(s stories.Story) string {
	if s.ID != "" {
		return s.ID
	}
	return s.Title
}

// afterAgent runs after every agent step: it enforces the input-file guard
// and commits the step's changes.
func (o *Orchestrator) afterAgent(ctx context.Context, ss *session, st *storyState, ref stepRef) bool {
	if !o.guardInputs(ss, ref) {
		return false
	}
	o.undoReviewerEdits(ctx, ss, ref)
	if ss.git == nil {
		return true
	}
	title := o.stepTitle(ref)
	c, err := ss.git.CommitAll(ctx, fmt.Sprintf("faztudo(%s): %s", storyLabel(st.sc.Story), title), dependencyPaths(ss.git.Nested())...)
	switch {
	case err != nil:
		o.logf(LevelWarn, "Commit de \"%s\" falhou: %v.", title, err)
	case c != nil:
		o.updateStep(ref, func(s *Step) { s.Commit = c })
		o.logf(LevelInfo, "Commit %s: %s (%d arquivo(s)).", c.Hash, title, len(c.Files))
	}
	if head, err := ss.git.Head(ctx); err == nil && ref.story >= 0 {
		o.updateStory(ref.story, func(r *StoryRun) { r.Head = head })
	}
	return true
}

// guardInputs fails the step when an agent changed a protected input file,
// after restoring the original content.
func (o *Orchestrator) guardInputs(ss *session, ref stepRef) bool {
	changed := ss.guard.Check()
	if len(changed) == 0 {
		return true
	}
	list := strings.Join(changed, ", ")
	err := fmt.Errorf("o agente alterou arquivo(s) de entrada protegido(s): %s; o conteúdo original foi restaurado", list)
	if rerr := ss.guard.Restore(changed); rerr != nil {
		err = fmt.Errorf("o agente alterou arquivo(s) de entrada protegido(s): %s; falha ao restaurar: %v", list, rerr)
	}
	o.failStep(ref, 0, err)
	return false
}
