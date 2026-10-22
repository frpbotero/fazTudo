// Package pipeline runs the multi-agent engineering flow and keeps an
// observable snapshot of its progress.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

const (
	maxLogs = 500
	// maxFixRounds is how many fix-and-verify rounds a gate may trigger
	// before the story fails.
	maxFixRounds = 2
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
	Kind     string        `json:"kind,omitempty"`  // "" (stage), "fix" or "verify"
	Round    int           `json:"round,omitempty"` // fix/verification round
	Gate     string        `json:"gate,omitempty"`  // gate that requested a fix
	Issues   []Issue       `json:"issues,omitempty"`
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
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Status     StepStatus `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	Plan       *Plan      `json:"plan,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	// Trace links the tests the specification requires to the test files.
	Trace []quality.Trace `json:"trace,omitempty"`
	Steps []Step          `json:"steps"`
}

// Run describes the current or last pipeline execution: a shared discovery
// followed by one or more stories developed in sequence.
type Run struct {
	Running    bool       `json:"running"`
	Source     string     `json:"source,omitempty"`
	Repository string     `json:"repository,omitempty"`
	DryRun     bool       `json:"dry_run"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Result     Result     `json:"result,omitempty"`
	BaseBranch string     `json:"base_branch,omitempty"`
	Protected  []string   `json:"protected,omitempty"` // input files guarded during the run
	Discovery  Step       `json:"discovery"`
	Stories    []StoryRun `json:"stories"`
	Current    int        `json:"current"` // index into Stories; -1 while discovering
	Logs       []LogEntry `json:"logs"`
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

// Orchestrator runs one pipeline at a time and notifies observers on change.
type Orchestrator struct {
	exec     Executor
	ws       *workspace.Manager
	stages   []Stage
	onChange func()
	logger   *slog.Logger
	runCheck func(ctx context.Context, dir string, c quality.Command, timeout time.Duration) quality.Result

	mu     sync.RWMutex
	run    Run
	last   StartOptions
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates an orchestrator. The discovery stage always runs first (or is
// reused from the session cache), followed by stages. onChange is called
// (without locks held) after every state mutation and must not block.
func New(exec Executor, ws *workspace.Manager, stages []Stage, onChange func(), logger *slog.Logger) *Orchestrator {
	o := &Orchestrator{exec: exec, ws: ws, stages: stages, onChange: onChange, logger: logger, runCheck: quality.Run}
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
	return r
}

// SetRepository changes the target repository. It is refused during a run.
func (o *Orchestrator) SetRepository(path string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.run.Running {
		return "", ErrAlreadyRunning
	}
	return o.ws.SetPath(path)
}

// Start launches a new run in the background. The simulation mode is applied
// only once the run is accepted, so a rejected start never alters a live run.
func (o *Orchestrator) Start(opts StartOptions) error {
	if len(opts.Stories) == 0 {
		return ErrNoStories
	}
	o.mu.Lock()
	if o.run.Running {
		o.mu.Unlock()
		return ErrAlreadyRunning
	}
	repo := o.ws.Path()
	if repo == "" {
		o.mu.Unlock()
		return workspace.ErrNotConfigured
	}
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
	o.last = opts
	done := o.done
	o.mu.Unlock()

	mode := "execução real"
	if opts.DryRun {
		mode = "simulação"
	}
	o.logf(LevelInfo, "Execução iniciada em %s: %d US (origem: %s, modo: %s).", repo, len(opts.Stories), opts.Source, mode)
	o.logger.Info("pipeline started", "repository", repo, "stories", len(opts.Stories), "source", opts.Source, "dry_run", opts.DryRun)

	go func() {
		defer close(done)
		defer cancel()
		o.execute(ctx, repo, opts)
	}()
	return nil
}

// RetryFailed resumes the failed story at the stage that owns the failure.
// Completed stages before it are kept as context; generated fix/verify steps
// from that stage onward are discarded and will be rebuilt by the gate.
func (o *Orchestrator) RetryFailed(story int, stepID string) error {
	o.mu.Lock()
	if o.run.Running {
		o.mu.Unlock()
		return ErrAlreadyRunning
	}
	if story == -1 && stepID == DiscoveryStage.ID && o.run.Result == ResultFailed && o.run.Discovery.Status == StepFailed && len(o.last.Stories) > 0 {
		opts := o.last
		o.mu.Unlock()
		return o.Start(opts)
	}
	if o.run.Result != ResultFailed || story < 0 || story >= len(o.run.Stories) || len(o.last.Stories) != len(o.run.Stories) {
		o.mu.Unlock()
		return ErrNoFailedStep
	}

	failed := stepIndex(o.run.Stories[story].Steps, stepID)
	if failed < 0 || o.run.Stories[story].Steps[failed].Status != StepFailed {
		o.mu.Unlock()
		return ErrNoFailedStep
	}
	baseID := retryStageID(o.run.Stories[story].Steps[failed])
	stageAt := slices.IndexFunc(o.stages, func(s Stage) bool { return s.ID == baseID })
	if stageAt < 0 {
		o.mu.Unlock()
		return ErrNoFailedStep
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
	started := time.Now()
	o.run.Stories[story].StartedAt = &started
	o.run.Stories[story].DurationMS = 0
	for i := story + 1; i < len(o.run.Stories); i++ {
		o.run.Stories[i].Status = StepPending
		o.run.Stories[i].StartedAt = nil
		o.run.Stories[i].DurationMS = 0
		o.run.Stories[i].Plan = nil
		o.run.Stories[i].Trace = nil
		o.run.Stories[i].Steps = o.freshSteps()
	}
	o.run.Running = true
	o.run.Result = ""
	o.run.FinishedAt = nil
	o.run.Current = story
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	o.done = make(chan struct{})
	done := o.done
	opts := o.last
	o.mu.Unlock()
	o.notify()
	o.logf(LevelInfo, "Refazendo %q a partir de %s.", previous.Title, o.stages[stageAt].Title)

	go func() {
		defer close(done)
		defer cancel()
		o.retryExecute(ctx, opts, story, stageAt, started)
	}()
	return nil
}

func retryStageID(step Step) string {
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
	checks []quality.Command
	guard  *quality.Guard
	git    *vcs.Repo
	report *workspace.Report
	// instructions are the repository's agent instruction files.
	instructions string
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
}

func (o *Orchestrator) execute(ctx context.Context, repo string, opts StartOptions) {
	ss, ok := o.prepare(ctx, repo, opts)
	if !ok {
		return
	}
	report, ok := o.discover(ctx, repo)
	if !ok || !o.guardInputs(ss, discoveryRef) {
		return
	}
	ss.report = report
	if ss.policy.Candidates > 1 && !ss.dry && ss.git == nil {
		o.logf(LevelWarn, "Best-of-N precisa de git; cada implementador fará uma única implementação.")
	}

	total := len(opts.Stories)
	for si, story := range opts.Stories {
		started := time.Now()
		o.updateStory(si, func(r *StoryRun) { r.Status = StepRunning; r.StartedAt = &started })
		if total > 1 {
			o.logf(LevelInfo, "Iniciando %s (%d de %d).", story.Title, si+1, total)
		}

		st := &storyState{
			si:      si,
			started: started,
			sc: storyContext{
				Preamble: opts.Preamble, Story: story, Delivered: opts.Stories[:si],
				Position: si + 1, Total: total, Rules: ss.rules(), Instructions: ss.instructions,
			},
		}
		if !o.developStory(ctx, ss, st) {
			return
		}

		o.updateStory(si, func(r *StoryRun) { r.Status = StepDone; r.DurationMS = time.Since(started).Milliseconds() })
		if total > 1 {
			o.logf(LevelSuccess, "%s entregue (%d de %d).", story.Title, si+1, total)
		}
	}
	o.finish(ResultSucceeded, LevelSuccess, "Pipeline concluído. Todos os agentes entregaram seus artefatos.")
}

// retryExecute rebuilds the transient session around the repository as it was
// left by the failed attempt, then continues the failed story and the queue.
func (o *Orchestrator) retryExecute(ctx context.Context, opts StartOptions, story, stageAt int, started time.Time) {
	repo := o.ws.Path()
	ss := o.retrySession(ctx, repo, opts)
	if ss == nil {
		return
	}
	report := o.ws.Discovery()
	if report == nil || report.Path != repo {
		o.failStep(stepRef{story, o.stages[stageAt].ID}, 0, errors.New("discovery indisponível; inicie uma nova execução"))
		return
	}
	ss.report = report

	total := len(opts.Stories)
	for si := story; si < total; si++ {
		storyStarted := started
		startAt := stageAt
		if si > story {
			storyStarted = time.Now()
			startAt = 0
			o.updateStory(si, func(r *StoryRun) { r.Status = StepRunning; r.StartedAt = &storyStarted })
		}
		st := o.restoreStoryState(si, storyStarted, opts, ss)
		if !o.developStoryFrom(ctx, ss, st, startAt, si > story) {
			return
		}
		o.updateStory(si, func(r *StoryRun) { r.Status = StepDone; r.DurationMS = time.Since(storyStarted).Milliseconds() })
	}
	o.finish(ResultSucceeded, LevelSuccess, "Pipeline concluído após refazer a etapa com falha.")
}

func (o *Orchestrator) retrySession(ctx context.Context, repo string, opts StartOptions) *session {
	o.exec.SetDryRun(opts.DryRun)
	policy := o.ws.Policy()
	ss := &session{repo: repo, dry: opts.DryRun, policy: policy, checks: o.ws.Checks(), instructions: workspace.ReadInstructions(repo)}
	patterns := policy.Protected
	if name := path.Base(filepath.ToSlash(strings.TrimSpace(opts.DocumentName))); name != "." && name != "/" {
		patterns = append(slices.Clone(patterns), name)
	}
	ss.guard = quality.NewGuard(repo, patterns)
	if !opts.DryRun && policy.Git {
		g, _, err := vcs.Open(ctx, repo)
		if err != nil {
			o.logf(LevelWarn, "Versionamento indisponível ao refazer a etapa: %v.", err)
		} else {
			ss.git = g
		}
	}
	return ss
}

func (o *Orchestrator) restoreStoryState(si int, started time.Time, opts StartOptions, ss *session) *storyState {
	st := &storyState{
		si: si, started: started,
		sc:   storyContext{Preamble: opts.Preamble, Story: opts.Stories[si], Delivered: opts.Stories[:si], Position: si + 1, Total: len(opts.Stories), Rules: ss.rules(), Instructions: ss.instructions},
		plan: fullPlan(o.stages, "default"),
		reqs: quality.ExtractRequirements(opts.Preamble + "\n\n" + opts.Stories[si].Content),
	}
	o.mu.RLock()
	runStory := o.run.Stories[si]
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
	if ss.instructions != "" {
		o.logf(LevelInfo, "Instruções do projeto (AGENTS.md/CLAUDE.md) repassadas a todos os agentes.")
	}

	patterns := policy.Protected
	if name := path.Base(filepath.ToSlash(strings.TrimSpace(opts.DocumentName))); name != "." && name != "/" {
		patterns = append(slices.Clone(patterns), name)
	}
	ss.guard = quality.NewGuard(repo, patterns)
	if files := ss.guard.Files(); len(files) > 0 {
		o.mu.Lock()
		o.run.Protected = files
		o.mu.Unlock()
		o.logf(LevelInfo, "Arquivos de entrada protegidos: %s.", strings.Join(files, ", "))
	}
	if len(ss.checks) == 0 {
		o.logf(LevelWarn, "Nenhum comando de verificação configurado ou detectado; o portão executável fará só as checagens estáticas.")
	}
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
	if dirty, err := g.Dirty(ctx); err != nil || dirty {
		if err == nil {
			err = vcs.ErrDirty
		}
		o.abortRemaining(StepCanceled)
		o.finish(ResultFailed, LevelError, err.Error())
		return nil, false
	}
	ss.git = g
	o.mu.Lock()
	o.run.BaseBranch = g.Branch(ctx)
	o.mu.Unlock()
	return ss, true
}

// developStory runs the stages for one story: planner first, then only the
// selected agents, with fix rounds whenever a gate requests changes.
func (o *Orchestrator) developStory(ctx context.Context, ss *session, st *storyState) bool {
	st.plan = fullPlan(o.stages, "default")
	st.reqs = quality.ExtractRequirements(st.sc.Preamble + "\n\n" + st.sc.Story.Content)
	return o.developStoryFrom(ctx, ss, st, 0, true)
}

func (o *Orchestrator) developStoryFrom(ctx context.Context, ss *session, st *storyState, startAt int, createBranch bool) bool {
	if createBranch {
		o.startBranch(ctx, ss, st)
	}

	for stageIndex, stage := range o.stages {
		if stageIndex < startAt {
			continue
		}
		if stage.Selectable && !st.plan.Includes(stage.ID) {
			continue // already marked as omitted when the plan was applied
		}
		ref := stepRef{st.si, stage.ID}
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
		case len(lenses) > 1:
			resp, ok = o.runLenses(ctx, ss, st, stage, lenses, ref)
		case stage.Implementer && ss.policy.Candidates > 1 && ss.git != nil && !ss.dry:
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
		case stage.Specifier:
			o.applySpec(st, resp)
		case stage.Planner:
			st.plan, st.planText = o.applyPlan(st.si, st.sc, resp), resp.Output
		case stage.Gate:
			recheck := func(ctx context.Context, ref stepRef, issues []Issue, fixes []Step, round int) (evaluation, bool) {
				resp, ok := o.runStage(ctx, ref, provider.Request{
					Role:     stage.Role,
					System:   stage.System,
					Prompt:   buildVerifyPrompt(stage, st.sc, ss.report, issues, fixes, round),
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

// startBranch creates the story's branch from the current HEAD, so stories of
// a sequence build on each other.
func (o *Orchestrator) startBranch(ctx context.Context, ss *session, st *storyState) {
	if ss.git == nil {
		return
	}
	branch := vcs.BranchName(storyLabel(st.sc.Story), time.Now())
	if err := ss.git.CreateBranch(ctx, branch); err != nil {
		o.logf(LevelWarn, "Não foi possível criar a branch %s: %v.", branch, err)
		return
	}
	o.updateStory(st.si, func(r *StoryRun) { r.Branch = branch })
	o.logf(LevelInfo, "Branch %s criada para %s.", branch, st.sc.Story.Title)
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
	c, err := ss.git.CommitAll(ctx, fmt.Sprintf("faztudo(%s): %s", storyLabel(st.sc.Story), title))
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
		if i := stepIndex(st.Steps, stage.ID); i >= 0 {
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
}

// recheckFunc re-evaluates a gate in the verification step ref after fixes.
type recheckFunc func(ctx context.Context, ref stepRef, issues []Issue, fixes []Step, round int) (evaluation, bool)

func (o *Orchestrator) llmEvaluation(resp *provider.Response, plan Plan) evaluation {
	v, ok := parseVerdict(resp.Output, o.stages, plan)
	return evaluation{verdict: v, parsed: ok, simulated: resp.Simulated, report: resp.Output}
}

// enforceGate reads a gate's verdict and, while it requests changes, sends the
// issues to the responsible implementers and re-evaluates the gate.
func (o *Orchestrator) enforceGate(ctx context.Context, ss *session, st *storyState, gate Stage, ev evaluation, recheck recheckFunc) bool {
	stepID := gate.ID
	rounds := maxFixRounds
	if gate.Verifier {
		rounds = maxVerifierRounds
	}
	for round := 1; ; round++ {
		if !ev.parsed {
			if !ev.simulated {
				o.logf(LevelWarn, "%s não retornou um veredito em JSON; considerado aprovado.", gate.Title)
			}
			return true
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
			if round > 1 {
				o.logf(LevelSuccess, "%s aprovou as correções.", gate.Title)
			}
			return true
		}

		if round > rounds {
			return o.failGate(st.si, stepID, gate, fmt.Errorf("%d problema(s) persistem após %d rodadas de correção", len(verdict.Issues), rounds))
		}
		fixes := o.scheduleFixes(st.si, gate, stepID, verdict.Issues, round)
		if len(fixes) == 0 {
			return o.failGate(st.si, stepID, gate, fmt.Errorf("%d problema(s) sem implementador para corrigi-los", len(verdict.Issues)))
		}
		verifyID := fmt.Sprintf("%s-verify-%d", gate.ID, round+1)
		o.insertSteps(st.si, fixes[len(fixes)-1].step.ID, Step{
			ID: verifyID, Title: fmt.Sprintf("%s — rodada %d", gate.Title, round+1), Role: gate.Role, Tier: gate.Tier,
			Status: StepPending, Kind: "verify", Round: round + 1,
		})

		var done []Step
		for _, f := range fixes {
			ref := stepRef{st.si, f.step.ID}
			fixResp, ok := o.runStage(ctx, ref, provider.Request{
				Role:    f.stage.Role,
				System:  f.stage.System,
				Prompt:  buildFixPrompt(f.stage, gate, st.sc, ss.report, st.planText, ev.report, f.step.Issues, round),
				Tier:    f.stage.Tier,
				WorkDir: ss.repo,
			})
			if !ok || !o.afterAgent(ctx, ss, st, ref) {
				return false
			}
			f.step.Output = fixResp.Output
			done = append(done, f.step)
			o.logf(LevelSuccess, "%s concluída via %s.", f.step.Title, fixResp.Name)
		}

		var alive bool
		if ev, alive = recheck(ctx, stepRef{st.si, verifyID}, verdict.Issues, done, round+1); !alive {
			return false
		}
		stepID = verifyID
	}
}

func (o *Orchestrator) failGate(si int, stepID string, gate Stage, err error) bool {
	o.updateStep(stepRef{si, stepID}, func(s *Step) { s.Status = StepFailed; s.Error = err.Error() })
	o.abortRemaining(StepFailed)
	o.finish(ResultFailed, LevelError, fmt.Sprintf("%s: %v", gate.Title, err))
	return false
}

type scheduledFix struct {
	stage Stage
	step  Step
}

// scheduleFixes groups issues by implementer and inserts one pending fix
// step per implementer right after the gate step.
func (o *Orchestrator) scheduleFixes(si int, gate Stage, afterID string, issues []Issue, round int) []scheduledFix {
	var fixes []scheduledFix
	var names []string
	for _, impl := range o.stages {
		if !impl.Implementer {
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
func (o *Orchestrator) discover(ctx context.Context, repo string) (*workspace.Report, bool) {
	if cached := o.ws.Discovery(); cached != nil && cached.Path == repo {
		o.updateStep(discoveryRef, func(s *Step) {
			s.Status = StepSkipped
			s.Provider, s.Model = cached.Provider, cached.Model
			s.Output = cached.Summary
			s.Note = fmt.Sprintf("Reutilizado da sessão (%s)", cached.CreatedAt.Format("15:04:05"))
		})
		o.logf(LevelInfo, "Discovery reutilizado da sessão (realizado às %s).", cached.CreatedAt.Format("15:04:05"))
		return cached, true
	}

	stacks, err := workspace.Scan(repo)
	if err != nil {
		o.failStep(discoveryRef, 0, fmt.Errorf("varredura do repositório: %w", err))
		return nil, false
	}
	o.logf(LevelInfo, "Varredura inicial: %d stack(s) identificada(s).", len(stacks))

	resp, ok := o.runStage(ctx, discoveryRef, provider.Request{
		Role:     DiscoveryStage.Role,
		System:   DiscoveryStage.System,
		Prompt:   buildDiscoveryPrompt(repo, stacks),
		Tier:     DiscoveryStage.Tier,
		WorkDir:  repo,
		ReadOnly: true,
	})
	if !ok {
		return nil, false
	}

	report := workspace.Report{
		Path:      repo,
		Stacks:    stacks,
		Summary:   resp.Output,
		Provider:  resp.Name,
		Model:     resp.Model,
		Simulated: resp.Simulated,
		CreatedAt: time.Now(),
	}
	o.ws.Store(report)
	o.logf(LevelSuccess, "Discovery concluído e salvo para esta sessão.")
	return &report, true
}

// runStage executes a step and records its outcome. On failure or
// cancellation it finishes the run and returns false.
func (o *Orchestrator) runStage(ctx context.Context, ref stepRef, req provider.Request) (*provider.Response, bool) {
	started := time.Now()
	o.updateStep(ref, func(s *Step) {
		s.Status = StepRunning
		s.StartedAt = &started
	})

	resp, err := o.exec.Execute(ctx, req, o.providerLog)
	elapsed := time.Since(started)
	if err != nil {
		if ctx.Err() != nil {
			o.cancelStep(ref, elapsed)
			return nil, false
		}
		o.failStep(ref, elapsed, err)
		return nil, false
	}

	o.updateStep(ref, func(s *Step) {
		s.Status = StepDone
		s.Provider = resp.Name
		s.Model = resp.Model
		s.Effort = resp.Effort
		s.Output = resp.Output
		s.DurationMS = elapsed.Milliseconds()
	})
	resp.Duration = elapsed
	return resp, true
}

func (o *Orchestrator) cancelStep(ref stepRef, elapsed time.Duration) {
	o.updateStep(ref, func(s *Step) { s.Status = StepCanceled; s.DurationMS = elapsed.Milliseconds() })
	o.abortRemaining(StepCanceled)
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
	o.abortRemaining(StepFailed)
	o.finish(ResultFailed, LevelError, fmt.Sprintf("Falha na etapa \"%s\": %v", title, err))
}

func (o *Orchestrator) finish(result Result, level, msg string) {
	now := time.Now()
	o.mu.Lock()
	o.run.Running = false
	o.run.Result = result
	o.run.FinishedAt = &now
	o.mu.Unlock()
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
	if ref.story < 0 {
		return o.run.Discovery.Title
	}
	steps := o.run.Stories[ref.story].Steps
	if i := stepIndex(steps, ref.id); i >= 0 {
		return steps[i].Title
	}
	return ref.id
}

func stepIndex(steps []Step, id string) int {
	return slices.IndexFunc(steps, func(s Step) bool { return s.ID == id })
}

// abortRemaining marks every pending step as canceled and closes the story
// in progress with the given status; later stories are canceled.
func (o *Orchestrator) abortRemaining(current StepStatus) {
	o.mu.Lock()
	if o.run.Discovery.Status == StepPending {
		o.run.Discovery.Status = StepCanceled
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
			st.Status = current
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
	if ref.story < 0 {
		fn(&o.run.Discovery)
	} else {
		o.run.Current = ref.story
		if i := stepIndex(o.run.Stories[ref.story].Steps, ref.id); i >= 0 {
			fn(&o.run.Stories[ref.story].Steps[i])
		}
	}
	o.mu.Unlock()
	o.notify()
}

func (o *Orchestrator) updateStory(i int, fn func(*StoryRun)) {
	o.mu.Lock()
	o.run.Current = i
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
	steps := make([]Step, len(o.stages))
	for i, s := range o.stages {
		steps[i] = newStep(s)
	}
	return steps
}

func newStep(s Stage) Step {
	return Step{ID: s.ID, Title: s.Title, Role: s.Role, Tier: s.Tier, Status: StepPending}
}

func (o *Orchestrator) notify() {
	if o.onChange != nil {
		o.onChange()
	}
}
