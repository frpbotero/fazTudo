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

// StepStatus is the lifecycle state of a stage.
type StepStatus string

const (
	StepPending  StepStatus = "pending"
	StepRunning  StepStatus = "running"
	StepDone     StepStatus = "done"
	StepSkipped  StepStatus = "skipped"
	StepOmitted  StepStatus = "omitted" // not selected by the architect
	StepFailed   StepStatus = "failed"
	StepWarning  StepStatus = "warning" // finished with unresolved issues; the run goes on
	StepCanceled StepStatus = "canceled"
)

// Result is the final outcome of a run.
type Result string

const (
	ResultSucceeded Result = "succeeded"
	ResultFailed    Result = "failed"
	ResultCanceled  Result = "canceled"
)

// Log levels exposed to the UI.
const (
	LevelInfo    = "info"
	LevelSuccess = "success"
	LevelWarn    = "warn"
	LevelError   = "error"
)

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

// Step is the observable state of one stage.
type Step struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Role     string        `json:"role"`
	Tier     provider.Tier `json:"tier"`
	Status   StepStatus    `json:"status"`
	Provider string        `json:"provider,omitempty"`
	Model    string        `json:"model,omitempty"`
	Effort   string        `json:"effort,omitempty"`
	Output   string        `json:"output,omitempty"`
	Note     string        `json:"note,omitempty"`
	Kind     string        `json:"kind,omitempty"`  // "" (stage), "fix", "verify", "investigate" or "stream"
	Agent    string        `json:"agent,omitempty"` // implementer running a stream
	Round    int           `json:"round,omitempty"` // fix/verification round
	Gate     string        `json:"gate,omitempty"`  // gate that requested a fix
	Issues   []Issue       `json:"issues,omitempty"`
	// Validation is a gate's final outcome, recorded on the gate's own step;
	// empty on steps that judge nothing.
	Validation Validation `json:"validation,omitempty"`
	// Checks and Findings are the results of an executable gate.
	Checks   []quality.Result  `json:"checks,omitempty"`
	Findings []quality.Finding `json:"findings,omitempty"`
	// Candidates are the alternatives compared by best-of-N.
	Candidates []Candidate `json:"candidates,omitempty"`
	// Commit records the changes the step made, when git is enabled.
	Commit     *vcs.Commit `json:"commit,omitempty"`
	Error      string      `json:"error,omitempty"`
	StartedAt  *time.Time  `json:"started_at,omitempty"`
	DurationMS int64       `json:"duration_ms,omitempty"`
}

// LogEntry is a structured event shown in the UI console.
type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// StoryRun is the progress of one user story through the stages.
type StoryRun struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Status StepStatus `json:"status"`
	// Validation is whether the delivery was approved, apart from Status,
	// which only says the story finished. Empty until the story closes.
	Validation Validation `json:"validation,omitempty"`
	// Risk is what the policy's risk rules found: level, reasons and the
	// stages they made mandatory.
	Risk *workspace.Risk `json:"risk,omitempty"`
	// Integration is whether the story entered the base branch; the note
	// says why it was kept out.
	Integration     Integration `json:"integration,omitempty"`
	IntegrationNote string      `json:"integration_note,omitempty"`
	StartedAt       *time.Time  `json:"started_at,omitempty"`
	DurationMS      int64       `json:"duration_ms,omitempty"`
	Plan            *Plan       `json:"plan,omitempty"`
	Branch          string      `json:"branch,omitempty"`
	// Wave is the 1-based group of stories developed in parallel; DependsOn
	// lists the stories it waits for, as the dependency analysis found them.
	Wave      int      `json:"wave,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
	// Trace links the tests the specification requires to the test files.
	Trace []quality.Trace `json:"trace,omitempty"`
	// Causes are the problems found, followed across fix rounds.
	Causes []Cause `json:"causes,omitempty"`
	Steps  []Step  `json:"steps"`
}

// Run describes the current or last pipeline execution: a shared discovery
// followed by one or more stories developed in sequence.
type Run struct {
	// Kind is RunCleanup for a code health run, empty for a story run.
	Kind       string     `json:"kind,omitempty"`
	Running    bool       `json:"running"`
	Source     string     `json:"source,omitempty"`
	Repository string     `json:"repository,omitempty"`
	DryRun     bool       `json:"dry_run"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Result     Result     `json:"result,omitempty"`
	// Validation aggregates the stories' outcomes: Result says the run
	// finished, Validation whether what it delivered was approved.
	Validation Validation `json:"validation,omitempty"`
	BaseBranch string     `json:"base_branch,omitempty"`
	Protected  []string   `json:"protected,omitempty"` // input files guarded during the run
	Discovery  Step       `json:"discovery"`
	// Analysis maps the dependencies between the stories of a document; it
	// runs only when there are several.
	Analysis *Step `json:"analysis,omitempty"`
	// Waves group the story indexes developed in parallel, in order.
	Waves [][]int `json:"waves,omitempty"`
	// Integrations are the branches that merged the stories of each wave.
	Integrations []string `json:"integrations,omitempty"`
	// Promotions are the decisions on merging candidates into the base.
	Promotions []Promotion `json:"promotions,omitempty"`
	Stories    []StoryRun  `json:"stories"`
	Current    int         `json:"current"` // index into Stories; -1 while discovering
	Logs       []LogEntry  `json:"logs"`
	// Bases are the branches each repository started from (keyed "." and by
	// nested path); every wave is merged into them before the next begins.
	Bases map[string]string `json:"bases,omitempty"`
	// Resume is where a stopped run continues; nil while running or done.
	Resume *ResumePoint `json:"resume,omitempty"`
	// Cleanup holds a code health run's options, checks and verdict.
	Cleanup *CleanupInfo `json:"cleanup,omitempty"`
}

// StartOptions configures a run.
type StartOptions struct {
	// Stories are developed in order; each one sees the previous as delivered.
	Stories []stories.Story
	// Preamble is document-level context shared by every story.
	Preamble string
	Source   string
	DryRun   bool
	// Rediscover forces a new discovery even if the session has one cached.
	Rediscover bool
	// DocumentName is the name of the loaded story file; a file with that
	// name in the repository is protected like the policy's patterns.
	DocumentName string
}

// stepRef addresses a step by ID: story -1 is the discovery step.
type stepRef struct {
	story int
	id    string
}

var discoveryRef = stepRef{story: -1, id: DiscoveryStage.ID}

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
	exec     Executor
	ws       *workspace.Manager
	stages   []Stage
	onChange func()
	logger   *slog.Logger
	runCheck func(ctx context.Context, dir string, c quality.Command, timeout time.Duration) quality.Result
	// sweepStacks and lockStackSlot keep disposable test stacks in check.
	sweepStacks   func(ctx context.Context, roots, patterns []string) ([]string, error)
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
		sweepStacks: defaultSweepStacks, lockStackSlot: defaultLockStackSlot,
		attempts: agentAttempts, retryDelay: agentRetryDelay}
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
		if i == story || isFinished(o.run.Stories[i].Status) {
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
	blocked    string    // Passo 0 reason when the story cannot be developed yet
	pending    int       // issues a gate could not get fixed within its rounds
	// inconclusive counts gates that never produced a valid verdict.
	inconclusive int
	// risk is what the policy's risk rules found so far; it only grows.
	risk    workspace.Risk
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
func (o *Orchestrator) closeStory(st *storyState, total int) {
	status := StepDone
	switch {
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
		if missing := o.missingRequired(st, r.Steps); len(missing) > 0 && st.blocked == "" {
			r.Validation = worseValidation(r.Validation, ValidationInconclusive)
			missingNote = strings.Join(missing, ", ")
		}
		if st.blocked != "" || o.run.DryRun {
			// Nothing was checked for real: a blocked story was not built and
			// a simulation runs no command nor model.
			r.Validation = ValidationNotEvaluated
		}
		if missingNote != "" && r.Status == StepDone {
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
	validation := runValidation(o.run.Stories)
	o.run.Validation = validation
	dry := o.run.DryRun
	o.mu.Unlock()
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
	patterns := policy.Protected
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
	if runStory.Plan != nil {
		st.plan = *runStory.Plan
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
	if ss.instructions != "" {
		o.logf(LevelInfo, "Instruções do projeto (AGENTS.md/CLAUDE.md/Spec Kit) repassadas a todos os agentes.")
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

	o.planAudit(ss, st)
	o.assessStoryRisk(ss, st, nil)
	streamed := false
	for stageIndex, stage := range o.stages {
		if stageIndex < startAt {
			continue
		}
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
			if !o.runVerifier(ctx, ss, st, stage) {
				return false
			}
			continue
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
				Prompt:   buildPrompt(stage, st.sc, ss.report, o.stepsBefore(ref)),
				Tier:     stage.Tier,
				WorkDir:  ss.repo,
				ReadOnly: stage.ReadOnly,
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
		case stage.Gate:
			recheck := func(ctx context.Context, ref stepRef, issues []Issue, fixes []Step, round int) (evaluation, bool) {
				if ev, ok := o.checkFixes(ctx, ss, st, ref); !ok {
					return evaluation{}, false
				} else if ev != nil {
					return *ev, true
				}
				resp, ok := o.runStage(ctx, ref, provider.Request{
					Role:     stage.Role,
					System:   stage.System,
					Prompt:   buildVerifyPrompt(o.texts.verifyTask, stage, st.sc, ss.report, issues, fixes, round),
					Tier:     stage.Tier,
					WorkDir:  ss.repo,
					ReadOnly: stage.ReadOnly,
				})
				if !ok || !o.afterAgent(ctx, ss, st, ref) {
					return evaluation{}, false
				}
				return o.llmEvaluation(resp, st.plan), true
			}
			if !o.enforceGate(ctx, ss, st, stage, o.llmEvaluation(resp, st.plan), recheck) {
				return false
			}
		}
	}
	return true
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

// applyAssessment records the Passo 0 decision. It returns true when the
// story ends here: already implemented (reuse) or blocked.
func (o *Orchestrator) applyAssessment(st *storyState, ref stepRef, resp *provider.Response) bool {
	a := parseAssessment(resp.Output)
	reason := a.Reason
	if reason == "" {
		reason = "sem motivo informado"
	}
	switch a.Decision {
	case DecisionReuse:
		o.updateStep(ref, func(s *Step) { s.Note = "Já atendida: " + reason })
		o.skipRemaining(st.si, "Já atendida pelo código existente (Passo 0)")
		o.logf(LevelSuccess, "%s já está implementada; nada a desenvolver.", st.sc.Story.Title)
		return true
	case DecisionBlock:
		o.updateStep(ref, func(s *Step) { s.Note = "Bloqueada: " + reason })
		o.skipRemaining(st.si, "US bloqueada no Passo 0")
		st.blocked = reason
		return true
	}
	o.updateStep(ref, func(s *Step) { s.Note = "Decisão: " + decisionLabel(a.Decision) })
	o.logf(LevelInfo, "Passo 0: %s.", decisionLabel(a.Decision))
	return false
}

func decisionLabel(d string) string {
	switch d {
	case DecisionExtend:
		return "estender (só o delta)"
	case DecisionRefactor:
		return "refatorar"
	}
	return "implementar"
}

// skipRemaining marks the story's pending steps as skipped.
func (o *Orchestrator) skipRemaining(si int, note string) {
	o.mu.Lock()
	for j := range o.run.Stories[si].Steps {
		if s := &o.run.Stories[si].Steps[j]; s.Status == StepPending {
			s.Status, s.Note = StepSkipped, note
		}
	}
	o.mu.Unlock()
	o.notify()
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

// testList is the spec stage's machine-readable list of required tests.
type testList struct {
	Tests []quality.Requirement `json:"tests"`
}

// applySpec adds the tests listed by the spec stage to the IDs found in the
// story document.
func (o *Orchestrator) applySpec(st *storyState, resp *provider.Response) {
	if listed, ok := lastJSON(resp.Output, func(l testList) bool { return l.Tests != nil }); ok {
		for i := range listed.Tests {
			listed.Tests[i].Kind = strings.ToLower(strings.TrimSpace(listed.Tests[i].Kind))
		}
		st.reqs = quality.MergeRequirements(st.reqs, listed.Tests)
	}
	if len(st.reqs) > 0 {
		o.logf(LevelInfo, "%d caso(s) de teste com ID serão rastreados até os arquivos de teste.", len(st.reqs))
	}
}

// applyPlan parses the planner output and marks unselected agents as omitted.
func (o *Orchestrator) applyPlan(si int, sc storyContext, resp *provider.Response) Plan {
	plan, ok := parsePlan(resp.Output, o.stages)
	switch {
	case ok:
	case resp.Simulated:
		plan = heuristicPlan(sc.Story.Content, o.stages)
	default:
		plan = fullPlan(o.stages, "fallback")
		o.logf(LevelWarn, "O arquiteto não retornou um plano em JSON; todos os agentes serão executados.")
	}

	policy := o.ws.Policy()
	plan.Agents = slices.DeleteFunc(plan.Agents, policy.StageDisabled)
	var names []string
	o.mu.Lock()
	st := &o.run.Stories[si]
	st.Plan = &plan
	for _, stage := range o.stages {
		if !stage.Selectable {
			continue
		}
		if plan.Includes(stage.ID) {
			names = append(names, stage.Title)
			continue
		}
		if i := stepIndex(st.Steps, stage.ID); i >= 0 && !policy.StageDisabled(stage.ID) {
			st.Steps[i].Status = StepOmitted
			st.Steps[i].Note = "Não selecionado pelo arquiteto"
		}
	}
	o.mu.Unlock()
	o.notify()

	if len(names) == 0 {
		names = append(names, "nenhum agente opcional")
	}
	o.logf(LevelInfo, "Plano do arquiteto: %s.", strings.Join(names, ", "))
	return plan
}

// evaluation is a gate's outcome: the verdict, when one could be read, and
// the report forwarded to whoever fixes the issues.
type evaluation struct {
	verdict   Verdict
	parsed    bool
	simulated bool
	report    string
	// problem says why no valid verdict could be read.
	problem string
	// noEvidence marks an approval backed by nothing executed, such as a
	// verifier with no command to run: it is not evaluated, not passed.
	noEvidence bool
}

// maxFormatRecoveries bounds how many times a gate is asked again, only for
// the verdict, when its answer carried none that honors the contract.
const maxFormatRecoveries = 2

// recheckFunc re-evaluates a gate in the verification step ref after fixes.
type recheckFunc func(ctx context.Context, ref stepRef, issues []Issue, fixes []Step, round int) (evaluation, bool)

func (o *Orchestrator) llmEvaluation(resp *provider.Response, plan Plan) evaluation {
	v, problem := readVerdict(resp.Output, o.stages, plan)
	return evaluation{verdict: v, parsed: problem == "", problem: problem, simulated: resp.Simulated, report: resp.Output}
}

// enforceGate reads a gate's verdict and, while it requests changes, sends the
// issues to the responsible implementers and re-evaluates the gate.
func (o *Orchestrator) enforceGate(ctx context.Context, ss *session, st *storyState, gate Stage, ev evaluation, recheck recheckFunc) bool {
	stepID := gate.ID
	rounds := maxFixRounds
	if ss.policy.MaxFixRounds > 0 {
		rounds = ss.policy.MaxFixRounds
	}
	if gate.Verifier {
		rounds = maxVerifierRounds
	}
	st.lastFailures, st.stalls = 0, 0
	for round := 1; ; round++ {
		base := stepID
		for try := 1; !ev.parsed && !ev.simulated && try <= maxFormatRecoveries; try++ {
			var alive bool
			if ev, stepID, alive = o.recoverVerdict(ctx, ss, st, gate, base, stepID, ev, try); !alive {
				return false
			}
		}
		if !ev.parsed {
			if ev.simulated {
				o.setValidation(st, gate, ValidationNotEvaluated)
				return true
			}
			return o.inconclusiveGate(st, stepID, gate, ev.problem)
		}
		verdict := ev.verdict
		o.updateStep(stepRef{st.si, stepID}, func(s *Step) {
			s.Issues = verdict.Issues
			if verdict.NeedsChanges() {
				s.Note = fmt.Sprintf("%d problema(s) encontrado(s)", len(verdict.Issues))
			} else {
				s.Note = "Aprovado"
			}
		})
		if !verdict.NeedsChanges() {
			o.closeCauses(st, nil)
			if ev.noEvidence {
				o.setValidation(st, gate, ValidationNotEvaluated)
			} else {
				o.setValidation(st, gate, ValidationPassed)
			}
			if round > 1 {
				o.logf(LevelSuccess, "%s aprovou as correções.", gate.Title)
			}
			return true
		}

		fixable := o.triage(ss, st, verdict.Issues)
		settle := func(msg string) bool {
			o.closeCauses(st, verdict.Issues)
			return o.settleGate(st, stepID, gate, verdict.Issues, msg)
		}
		switch {
		case round > rounds:
			return settle(fmt.Sprintf("%d problema(s) persistem após %d rodadas de correção", len(verdict.Issues), rounds))
		case len(fixable) == 0:
			return settle(o.settleNote(st, verdict.Issues))
		case st.stalled(countCauses(verdict.Issues)):
			return settle(fmt.Sprintf("%d problema(s) sem redução há %d rodadas", len(verdict.Issues), maxStalledRounds))
		}
		fixable, afterID, alive := o.investigate(ctx, ss, st, gate, stepID, fixable, round)
		if !alive {
			return false
		}
		if len(fixable) == 0 {
			return settle(o.settleNote(st, verdict.Issues))
		}
		tier := o.repairTier(st, fixable)
		o.countAttempts(st, fixable)
		fixes := o.scheduleFixes(st.si, gate, afterID, fixable, round)
		if len(fixes) == 0 {
			return settle(fmt.Sprintf("%d problema(s) sem implementador para corrigi-los", len(verdict.Issues)))
		}
		verifyID := fmt.Sprintf("%s-verify-%d", gate.ID, round+1)
		o.insertSteps(st.si, fixes[len(fixes)-1].step.ID, Step{
			ID: verifyID, Title: fmt.Sprintf("%s — rodada %d", gate.Title, round+1), Role: gate.Role, Tier: gate.Tier,
			Status: StepPending, Kind: "verify", Round: round + 1,
		})

		fixesStarted := quality.FileClock(ss.repo)
		var done []Step
		for _, f := range fixes {
			ref := stepRef{st.si, f.step.ID}
			fixResp, ok := o.runStage(ctx, ref, provider.Request{
				Role:    f.stage.Role,
				System:  f.stage.System,
				Prompt:  buildFixPrompt(o.texts.fixTask, gate, st.sc, ss.report, st.planText, ev.report, repairBrief(f.step.Issues, o.causes(st.si)), f.step.Issues, round),
				Tier:    fixTier(f.stage, tier),
				WorkDir: ss.repo,
			})
			if !ok || !o.afterAgent(ctx, ss, st, ref) {
				return false
			}
			o.recordFix(st, f.step.Issues, fixResp.Output)
			f.step.Output = fixResp.Output
			done = append(done, f.step)
			o.logf(LevelSuccess, "%s concluída via %s.", f.step.Title, fixResp.Name)
		}

		if !ss.dry && len(o.changedFiles(ss, fixesStarted)) == 0 {
			o.updateStep(stepRef{st.si, verifyID}, func(s *Step) { s.Note = "Sem alterações desde a última verificação" })
			stepID = verifyID
			return settle("as correções não alteraram nenhum arquivo")
		}
		if ev, alive = recheck(ctx, stepRef{st.si, verifyID}, verdict.Issues, done, round+1); !alive {
			return false
		}
		stepID = verifyID
	}
}

// settleGate keeps the run going when a gate's issues outlast the fix rounds:
// the step is marked with the pending issues and the next stages and stories
// still run, so an unattended run never stops on one stubborn problem.
func (o *Orchestrator) settleGate(st *storyState, stepID string, gate Stage, issues []Issue, msg string) bool {
	o.updateStep(stepRef{st.si, stepID}, func(s *Step) {
		s.Status = StepWarning
		s.Error = msg + ". Pendências registradas; o pipeline segue."
	})
	o.setValidation(st, gate, ValidationFailed)
	st.pending += len(issues)
	o.logf(LevelWarn, "%s: %s. Seguindo com as pendências registradas.", gate.Title, msg)
	return true
}

// recoverVerdict asks a gate again, read-only, only for the verdict its last
// answer lacked. The request is a new step after stepID; base names it.
func (o *Orchestrator) recoverVerdict(ctx context.Context, ss *session, st *storyState, gate Stage, base, stepID string, ev evaluation, try int) (evaluation, string, bool) {
	o.logf(LevelWarn, "%s não retornou um veredito válido (%s); pedindo só o veredito (tentativa %d de %d).",
		gate.Title, ev.problem, try, maxFormatRecoveries)
	id := fmt.Sprintf("%s-formato-%d", base, try)
	o.insertSteps(st.si, stepID, Step{
		ID: id, Title: fmt.Sprintf("%s — formato do veredito (%d)", gate.Title, try), Role: gate.Role, Tier: gate.Tier,
		Status: StepPending, Kind: "verify",
	})
	ref := stepRef{st.si, id}
	resp, ok := o.runStage(ctx, ref, provider.Request{
		Role:     gate.Role,
		System:   gate.System,
		Prompt:   buildFormatPrompt(ev.problem, ev.report),
		Tier:     gate.Tier,
		WorkDir:  ss.repo,
		ReadOnly: true,
	})
	if !ok || !o.afterAgent(ctx, ss, st, ref) {
		return evaluation{}, id, false
	}
	next := o.llmEvaluation(resp, st.plan)
	// The recovered verdict is about the original review: keep its report
	// for whoever fixes the issues.
	if next.parsed {
		next.report = ev.report
	} else {
		next.report = cmp.Or(strings.TrimSpace(ev.report), resp.Output)
	}
	return next, id, true
}

// inconclusiveGate ends a gate that never produced a valid verdict: the run
// goes on, but the story cannot count as approved.
func (o *Orchestrator) inconclusiveGate(st *storyState, stepID string, gate Stage, problem string) bool {
	msg := fmt.Sprintf("Veredito inválido após %d tentativa(s) de recuperação: %s. Validação inconclusiva; o pipeline segue.", maxFormatRecoveries, problem)
	o.updateStep(stepRef{st.si, stepID}, func(s *Step) {
		s.Status = StepWarning
		s.Error = msg
	})
	o.setValidation(st, gate, ValidationInconclusive)
	st.inconclusive++
	o.logf(LevelWarn, "%s: %s", gate.Title, msg)
	return true
}

// setValidation records a gate's final outcome on the gate's own step, which
// a retry keeps, unlike the dynamic rounds that led to it.
func (o *Orchestrator) setValidation(st *storyState, gate Stage, v Validation) {
	o.updateStep(stepRef{st.si, gate.ID}, func(s *Step) { s.Validation = v })
}

// fixTier raises a fix to the heavy model for a cause's second attempt.
func fixTier(stage Stage, tier provider.Tier) provider.Tier {
	if tier == provider.TierHeavy {
		return tier
	}
	return stage.Tier
}

type scheduledFix struct {
	stage Stage
	step  Step
}

// scheduleFixes groups issues by implementer and inserts one pending fix
// step per implementer right after the gate step.
func (o *Orchestrator) scheduleFixes(si int, gate Stage, afterID string, issues []Issue, round int) []scheduledFix {
	policy := o.ws.Policy()
	var fixes []scheduledFix
	var names []string
	for _, impl := range o.stages {
		if !impl.Implementer || policy.StageDisabled(impl.ID) {
			continue
		}
		var mine []Issue
		for _, is := range issues {
			if is.Agent == impl.ID {
				mine = append(mine, is)
			}
		}
		if len(mine) == 0 {
			continue
		}
		step := Step{
			ID:     fmt.Sprintf("fix-%s-%d-%s", gate.ID, round, impl.ID),
			Title:  fmt.Sprintf("Correção: %s (%s, rodada %d)", impl.Title, gate.Title, round),
			Role:   impl.Role,
			Tier:   impl.Tier,
			Status: StepPending,
			Kind:   "fix",
			Round:  round,
			Gate:   gate.ID,
			Issues: mine,
		}
		o.insertSteps(si, afterID, step)
		afterID = step.ID
		fixes = append(fixes, scheduledFix{stage: impl, step: step})
		names = append(names, fmt.Sprintf("%s (%d)", impl.Title, len(mine)))
	}
	if len(fixes) > 0 {
		o.logf(LevelWarn, "%s solicitou %d correção(ões); acionando %s.", gate.Title, len(issues), strings.Join(names, ", "))
	}
	return fixes
}

// discover returns the session's discovery for repo, running it only when
// there is no cached report.
func (o *Orchestrator) discover(ctx context.Context, ss *session, graph string) (*workspace.Report, bool) {
	repo := ss.repo
	fp := workspace.TakeFingerprint(repo)
	if ss.git != nil {
		fp.Commit, _ = ss.git.Head(ctx)
	}
	reason := "primeiro discovery deste repositório"
	var previous *workspace.Report
	var changes []workspace.FileChange
	if cached := o.ws.Discovery(); cached != nil && cached.Path == repo {
		if reason, changes = cached.Staleness(fp, !ss.dry); reason == "" {
			return o.reuseDiscovery(*cached, fp), true
		}
		previous = cached
		o.logf(LevelInfo, "Discovery desatualizado: %s.", reason)
	}

	stacks, err := workspace.Scan(repo)
	if err != nil {
		o.failStep(discoveryRef, 0, fmt.Errorf("varredura do repositório: %w", err))
		return nil, false
	}
	o.logf(LevelInfo, "Varredura inicial: %d stack(s) identificada(s).", len(stacks))

	origin := workspace.OriginFull
	prompt := buildDiscoveryPrompt(o.texts.discovery.Task, repo, stacks, graph, ss.instructions)
	if incrementalDiscovery(previous, changes, ss.dry) {
		origin = workspace.OriginIncremental
		prompt = buildIncrementalDiscoveryPrompt(prompt, previous.Summary, changes)
		o.logf(LevelInfo, "Atualizando o discovery só para os %d arquivo(s) que mudaram.", len(changes))
	}
	resp, ok := o.runStage(ctx, discoveryRef, provider.Request{
		Role:     DiscoveryStage.Role,
		System:   o.texts.discovery.System,
		Prompt:   prompt,
		Tier:     DiscoveryStage.Tier,
		WorkDir:  repo,
		ReadOnly: true,
	})
	if !ok {
		return nil, false
	}

	now := time.Now()
	report := workspace.Report{
		Path:        repo,
		Stacks:      stacks,
		Summary:     resp.Output,
		Provider:    resp.Name,
		Model:       resp.Model,
		Simulated:   resp.Simulated,
		CreatedAt:   now,
		Fingerprint: &fp,
		Origin:      origin,
		Reason:      reason,
		ValidatedAt: &now,
	}
	o.updateStep(discoveryRef, func(s *Step) { s.Note = discoveryNote(report) })
	o.ws.Store(report)
	o.logf(LevelSuccess, "Discovery concluído e salvo.")
	return &report, true
}

// maxIncrementalChanges is how many changed files an incremental discovery
// update takes; beyond it the repository is mapped again from scratch.
const maxIncrementalChanges = 15

// incrementalDiscovery tells whether updating the previous report for the
// changed files is safe: a real report with a fingerprint, a few changed
// files, and none removed (a removed manifest may take a whole stack away).
func incrementalDiscovery(previous *workspace.Report, changes []workspace.FileChange, dry bool) bool {
	if previous == nil || previous.Fingerprint == nil || previous.Simulated || dry || strings.TrimSpace(previous.Summary) == "" {
		return false
	}
	if len(changes) == 0 || len(changes) > maxIncrementalChanges {
		return false
	}
	return !slices.ContainsFunc(changes, func(c workspace.FileChange) bool { return c.Change == "removido" })
}

// reuseDiscovery serves the cached report, confirmed valid for fp.
func (o *Orchestrator) reuseDiscovery(cached workspace.Report, fp workspace.Fingerprint) *workspace.Report {
	now := time.Now()
	cached.ValidatedAt, cached.Fingerprint = &now, &fp
	o.ws.Store(cached)
	o.updateStep(discoveryRef, func(s *Step) {
		s.Status = StepSkipped
		s.Provider, s.Model = cached.Provider, cached.Model
		s.Output = cached.Summary
		s.Note = fmt.Sprintf("Reutilizado (%s): nenhum manifesto, lockfile, instrução ou contrato mudou", cached.CreatedAt.Format("02/01 15:04"))
	})
	o.logf(LevelInfo, "Discovery reutilizado (realizado em %s): %d arquivo(s) relevante(s) conferido(s), nenhum mudou. Refazer mapeia de novo.",
		cached.CreatedAt.Format("02/01 15:04"), len(fp.Files))
	return &cached
}

func discoveryNote(r workspace.Report) string {
	if r.Origin == workspace.OriginIncremental {
		return "Atualizado: " + r.Reason
	}
	return "Completo: " + r.Reason
}

// updateCodeGraph builds the code graph; tests replace it.
var updateCodeGraph = workspace.UpdateGraph

// updateGraph refreshes the repository's code graph, which agents query
// instead of scanning files. Without graphify the run goes on, only slower.
// Simulation leaves the repository untouched.
func (o *Orchestrator) updateGraph(ctx context.Context, ss *session) string {
	if ss.dry {
		return ""
	}
	graph, err := updateCodeGraph(ctx, ss.repo)
	if err != nil {
		o.logf(LevelWarn, "Grafo do código indisponível: %v", err)
		return ""
	}
	o.logf(LevelInfo, "Grafo do código atualizado: %s.", graph)
	return graph
}

// runStage executes a step and records its outcome. A failing call (CLI
// crash, timeout, network) is retried with a growing delay; after the last
// attempt, or on cancellation, it finishes the run and returns false.
func (o *Orchestrator) runStage(ctx context.Context, ref stepRef, req provider.Request) (*provider.Response, bool) {
	started := time.Now()
	o.markRunning(ref, started)
	resp, err := o.executeRetrying(ctx, ref, req)
	elapsed := time.Since(started)
	if err != nil {
		o.stopOnError(ctx, ref, elapsed, err)
		return nil, false
	}
	o.recordDone(ref, resp, elapsed)
	o.markTurnLimited(ref, resp, o.limited(req).MaxTurns)
	return resp, true
}

func (o *Orchestrator) markRunning(ref stepRef, started time.Time) {
	o.updateStep(ref, func(s *Step) {
		s.Status = StepRunning
		s.StartedAt = &started
	})
}

// executeRetrying calls the provider, retrying failures until the attempts
// run out or ctx is canceled.
func (o *Orchestrator) executeRetrying(ctx context.Context, ref stepRef, req provider.Request) (*provider.Response, error) {
	req = o.limited(req)
	prompt := req.Prompt
	for attempt := 1; ; attempt++ {
		resp, err := o.exec.Execute(ctx, req, o.providerLog)
		o.recordUsage(ref, req, resp, attempt)
		if errors.Is(err, provider.ErrTurnLimit) {
			return o.turnLimited(ref, req, resp, err)
		}
		if err == nil || ctx.Err() != nil || attempt >= o.attempts {
			return resp, err
		}
		wait := o.retryDelay * time.Duration(attempt)
		o.logf(LevelWarn, "%s falhou (tentativa %d de %d): %s. Nova tentativa em %s.",
			o.stepTitle(ref), attempt, o.attempts, clipRunes(err.Error(), 300), wait)
		o.updateStep(ref, func(s *Step) { s.Note = fmt.Sprintf("Tentativa %d de %d", attempt+1, o.attempts) })
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		req.Prompt = prompt + resumeNote
	}
}

func (o *Orchestrator) recordDone(ref stepRef, resp *provider.Response, elapsed time.Duration) {
	o.updateStep(ref, func(s *Step) {
		s.Status = StepDone
		s.Provider = resp.Name
		s.Model = resp.Model
		s.Effort = resp.Effort
		s.Output = resp.Output
		s.DurationMS = elapsed.Milliseconds()
	})
	resp.Duration = elapsed
}

// stopOnError ends the run after a failed step: canceled when ctx was,
// failed otherwise.
func (o *Orchestrator) stopOnError(ctx context.Context, ref stepRef, elapsed time.Duration, err error) {
	if ctx.Err() != nil {
		o.cancelStep(ref, elapsed)
		return
	}
	o.failStep(ref, elapsed, err)
}

// resumeNote tells a retried agent that a previous attempt may have left work
// (or a stuck process) behind.
const resumeNote = "\n\n[RETOMADA] A tentativa anterior desta tarefa falhou ou excedeu o tempo. " +
	"Verifique o estado atual dos arquivos e continue de onde parou. Não deixe servidor nem watcher em primeiro plano."

func (o *Orchestrator) cancelStep(ref stepRef, elapsed time.Duration) {
	o.updateStep(ref, func(s *Step) { s.Status = StepCanceled; s.DurationMS = elapsed.Milliseconds() })
	o.abortRemaining(ref.story, StepCanceled)
	o.finish(ResultCanceled, LevelWarn, "Execução cancelada pelo usuário.")
}

// failStep marks the step failed and ends the run. A zero elapsed keeps the
// duration already recorded.
func (o *Orchestrator) failStep(ref stepRef, elapsed time.Duration, err error) {
	var title string
	o.updateStep(ref, func(s *Step) {
		s.Status = StepFailed
		s.Error = err.Error()
		if elapsed > 0 {
			s.DurationMS = elapsed.Milliseconds()
		}
		title = s.Title
	})
	o.abortRemaining(ref.story, StepFailed)
	o.finish(ResultFailed, LevelError, fmt.Sprintf("Falha na etapa \"%s\": %v", title, err))
}

// finish ends the run; when parallel stories stop one after the other, the
// first outcome is kept.
func (o *Orchestrator) finish(result Result, level, msg string) {
	now := time.Now()
	o.mu.Lock()
	if !o.run.Running {
		o.mu.Unlock()
		return
	}
	o.run.Running = false
	o.run.Result = result
	o.run.FinishedAt = &now
	o.mu.Unlock()
	o.writePendencies(o.Snapshot())
	o.logf(level, "%s", msg)
	o.logger.Info("pipeline finished", "result", result)
}

// stepsBefore returns the steps of a story that precede ref and produced output.
func (o *Orchestrator) stepsBefore(ref stepRef) []Step {
	o.mu.RLock()
	defer o.mu.RUnlock()
	steps := o.run.Stories[ref.story].Steps
	var out []Step
	for _, s := range steps[:max(stepIndex(steps, ref.id), 0)] {
		if s.Output != "" {
			out = append(out, s)
		}
	}
	return out
}

// insertSteps adds steps right after the step afterID of story si.
func (o *Orchestrator) insertSteps(si int, afterID string, steps ...Step) {
	o.mu.Lock()
	st := &o.run.Stories[si]
	i := stepIndex(st.Steps, afterID) + 1
	st.Steps = slices.Insert(st.Steps, i, steps...)
	o.mu.Unlock()
	o.notify()
}

func (o *Orchestrator) stepTitle(ref stepRef) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if step := o.stepLocked(ref); step != nil {
		return step.Title
	}
	return ref.id
}

func stepIndex(steps []Step, id string) int {
	return slices.IndexFunc(steps, func(s Step) bool { return s.ID == id })
}

// abortRemaining marks every pending step as canceled and closes story si
// with the given status; other stories in progress or pending are canceled.
func (o *Orchestrator) abortRemaining(si int, current StepStatus) {
	o.mu.Lock()
	if o.run.Discovery.Status == StepPending {
		o.run.Discovery.Status = StepCanceled
	}
	if a := o.run.Analysis; a != nil && a.Status == StepPending {
		a.Status = StepCanceled
	}
	for i := range o.run.Stories {
		st := &o.run.Stories[i]
		for j := range st.Steps {
			if st.Steps[j].Status == StepPending {
				st.Steps[j].Status = StepCanceled
			}
		}
		switch st.Status {
		case StepRunning:
			st.Status = StepCanceled
			if i == si {
				st.Status = current
			}
			if st.StartedAt != nil {
				st.DurationMS = time.Since(*st.StartedAt).Milliseconds()
			}
		case StepPending:
			st.Status = StepCanceled
		}
	}
	o.mu.Unlock()
	o.notify()
}

func (o *Orchestrator) updateStep(ref stepRef, fn func(*Step)) {
	o.mu.Lock()
	if step := o.stepLocked(ref); step != nil {
		fn(step)
	}
	if ref.story >= 0 {
		o.focusLocked(ref.story)
	}
	o.mu.Unlock()
	o.notify()
}

// stepLocked finds the step ref addresses; story -1 holds the run-level
// discovery and analysis steps.
func (o *Orchestrator) stepLocked(ref stepRef) *Step {
	switch {
	case ref == analysisRef:
		return o.run.Analysis
	case ref.story < 0:
		return &o.run.Discovery
	}
	steps := o.run.Stories[ref.story].Steps
	if i := stepIndex(steps, ref.id); i >= 0 {
		return &steps[i]
	}
	return nil
}

// focusLocked makes story i the current one, unless another story is still
// running beside it.
func (o *Orchestrator) focusLocked(i int) {
	c := o.run.Current
	if c >= 0 && c < len(o.run.Stories) && c != i && o.run.Stories[c].Status == StepRunning {
		return
	}
	o.run.Current = i
}

func (o *Orchestrator) updateStory(i int, fn func(*StoryRun)) {
	o.mu.Lock()
	o.focusLocked(i)
	fn(&o.run.Stories[i])
	o.mu.Unlock()
	o.notify()
}

func (o *Orchestrator) logf(level, format string, args ...any) {
	o.mu.Lock()
	o.run.Logs = append(o.run.Logs, LogEntry{Time: time.Now(), Level: level, Message: fmt.Sprintf(format, args...)})
	if over := len(o.run.Logs) - maxLogs; over > 0 {
		o.run.Logs = slices.Delete(o.run.Logs, 0, over)
	}
	o.mu.Unlock()
	o.notify()
}

// providerLog adapts router events to UI log levels.
func (o *Orchestrator) providerLog(level slog.Level, format string, args ...any) {
	switch {
	case level >= slog.LevelError:
		o.logf(LevelError, format, args...)
	case level >= slog.LevelWarn:
		o.logf(LevelWarn, format, args...)
	default:
		o.logf(LevelInfo, format, args...)
	}
}

func (o *Orchestrator) freshSteps() []Step {
	policy := o.ws.Policy()
	steps := make([]Step, len(o.stages))
	for i, s := range o.stages {
		steps[i] = newStep(s)
		if policy.StageDisabled(s.ID) {
			steps[i].Status, steps[i].Note = StepOmitted, flowOffNote
		}
	}
	return steps
}

func newStep(s Stage) Step {
	return Step{ID: s.ID, Title: s.Title, Role: s.Role, Tier: s.Tier, Status: StepPending}
}

func (o *Orchestrator) notify() {
	o.requestJournalSave()
	if o.onChange != nil {
		o.onChange()
	}
}
