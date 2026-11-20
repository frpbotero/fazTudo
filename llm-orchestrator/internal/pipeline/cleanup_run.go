package pipeline

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/vcs"
	"llm-orchestrator/internal/workspace"
)

var (
	cleanupBaselineRef = stepRef{story: 0, id: CleanupBaselineStage.ID}
	cleanupAgentRef    = stepRef{story: 0, id: CleanupStage.ID}
	cleanupVerifyRef   = stepRef{story: 0, id: CleanupVerifyStage.ID}
)

// StartCleanup launches a code health run on one area in the background. It
// never merges: the result is a branch for a person to review.
func (o *Orchestrator) StartCleanup(opts CleanupOptions) error {
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
	opts, err := validateCleanupOptions(repo, opts)
	if err != nil {
		o.mu.Unlock()
		return err
	}
	if !opts.DryRun && !o.ws.Policy().Git {
		o.mu.Unlock()
		return ErrCleanupNeedsGit
	}
	unlock, err := workspace.LockRepository(repo)
	if err != nil {
		o.mu.Unlock()
		return err
	}
	customized := o.usePrompts()
	o.exec.SetDryRun(opts.DryRun)
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	o.done = make(chan struct{})
	o.run = Run{
		Kind:       RunCleanup,
		Running:    true,
		Source:     "saude",
		Repository: repo,
		DryRun:     opts.DryRun,
		StartedAt:  &now,
		Discovery:  newStep(DiscoveryStage),
		Stories: []StoryRun{{
			ID: "saude", Title: "Saúde do código: " + areaLabel(opts.Area), Status: StepPending,
			Steps: []Step{newStep(CleanupBaselineStage), newStep(o.texts.cleanup), newStep(CleanupVerifyStage)},
		}},
		Current: -1,
		Cleanup: &CleanupInfo{Area: opts.Area, Comments: opts.Comments, Tests: opts.Tests, Rounds: opts.Rounds},
	}
	o.last = StartOptions{DryRun: opts.DryRun, Source: "saude"}
	o.isJournalOwner = true
	done := o.done
	o.mu.Unlock()

	o.logf(LevelInfo, "Saúde do código iniciada em %s: área %s, até %d rodada(s) (%s).", repo, areaLabel(opts.Area), opts.Rounds, modeLabel(opts.DryRun))
	o.logger.Info("cleanup started", "repository", repo, "area", opts.Area, "dry_run", opts.DryRun)
	o.logCustomPrompts(customized)
	go func() {
		defer close(done)
		defer unlock()
		defer cancel()
		defer o.sweepRun(repo, opts.DryRun)
		o.sweepRun(repo, opts.DryRun)
		o.runCleanup(ctx, repo, opts)
	}()
	return nil
}

func areaLabel(area string) string {
	if area == "." {
		return "repositório inteiro"
	}
	return area
}

func modeLabel(dry bool) string {
	if dry {
		return "simulação"
	}
	return "execução real"
}

// runCleanup takes the baseline, lets the agent clean the area, checks again
// and decides the verdict from the commands, the guard, the area and the
// test IDs, never from what the agent says.
func (o *Orchestrator) runCleanup(ctx context.Context, repo string, opts CleanupOptions) {
	ss, ok := o.prepare(ctx, repo, StartOptions{DryRun: opts.DryRun, Source: "saude"})
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
	started := time.Now()
	o.updateStory(0, func(r *StoryRun) { r.Status, r.StartedAt = StepRunning, &started })
	if ss.git != nil {
		if !o.startCleanupBranch(ctx, ss, opts.Area, started) {
			return
		}
		defer o.leaveCleanupBranch(ss)
	}

	idsBefore := quality.TestIDs(repo)
	baseline, ok := o.cleanupChecks(ctx, ss, cleanupBaselineRef)
	if !ok {
		return
	}
	o.updateCleanup(func(c *CleanupInfo) { c.Baseline = baseline })

	agentStarted := quality.FileClock(repo)
	logRel := path.Join(cleanupOutDir, "saude-"+started.Format("20060102-150405")+".md")
	stage := o.texts.cleanup
	prompt := buildCleanupPrompt(stage.Task, report, cleanupPrompt{
		opts: opts, logPath: logRel, instructions: ss.instructions,
		protected: ss.guard.Files(), commands: quality.RepairCommands(ss.checks),
	})
	if _, ok := o.runStage(ctx, cleanupAgentRef, provider.Request{
		Role: stage.Role, System: stage.System, Prompt: prompt, Tier: stage.Tier, WorkDir: repo,
	}); !ok {
		return
	}
	guarded := o.guardInputs(ss, cleanupAgentRef)
	changed := o.commitCleanup(ctx, ss, opts.Area, agentStarted)
	o.keepDecisionLog(repo, logRel)
	if !guarded {
		o.updateCleanup(func(c *CleanupInfo) {
			c.Verdict, c.Changed = VerdictRejected, changed
			c.Reasons = []string{"Arquivo de entrada ou protegido alterado; o conteúdo original foi restaurado."}
		})
		return
	}

	final, ok := o.cleanupChecks(ctx, ss, cleanupVerifyRef)
	if !ok {
		return
	}
	reasons := cleanupReasons(opts.Area, changed, idsBefore, quality.TestIDs(repo), baseline, final)
	o.closeCleanup(final, changed, reasons, started)
}

// startCleanupBranch creates the run's own branch from the base and keeps
// the decision logs out of git.
func (o *Orchestrator) startCleanupBranch(ctx context.Context, ss *session, area string, at time.Time) bool {
	base := ss.git.Branch(ctx)
	branch := vcs.BranchName("saude-"+area, at)
	if area == "." {
		branch = vcs.BranchName("saude", at)
	}
	if err := ss.git.CreateBranch(ctx, branch); err != nil {
		o.failStep(cleanupBaselineRef, 0, fmt.Errorf("não foi possível criar a branch %s: %w", branch, err))
		return false
	}
	workspace.ExcludeFromGit(ss.repo, cleanupOutDir+"/")
	o.updateStory(0, func(r *StoryRun) { r.Branch = branch })
	o.updateCleanup(func(c *CleanupInfo) { c.BaseBranch = base })
	o.logf(LevelInfo, "Branch %s criada a partir de %s.", branch, base)
	return true
}

// leaveCleanupBranch saves what an interrupted agent left and returns to the
// base, so the next run starts where the user left the repository.
func (o *Orchestrator) leaveCleanupBranch(ss *session) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := ss.git.CommitAll(ctx, "faztudo(saude): trabalho interrompido", dependencyPaths(ss.git.Nested())...); err != nil {
		o.logf(LevelWarn, "Alterações da limpeza não salvas: %v.", err)
	}
	if err := ss.git.SwitchBranches(ctx, ss.bases); err != nil {
		o.logf(LevelWarn, "Não foi possível voltar para a branch base: %v.", err)
		return
	}
	o.logf(LevelInfo, "Repositório de volta à branch base; a limpeza ficou na sua branch.")
}

// cleanupChecks runs one deterministic step. In simulation the commands do
// not run, as in story runs. It returns false when ctx was canceled.
func (o *Orchestrator) cleanupChecks(ctx context.Context, ss *session, ref stepRef) ([]CheckSnapshot, bool) {
	started := time.Now()
	o.markRunning(ref, started)
	if ss.dry || len(ss.checks) == 0 {
		note := "Nenhum comando configurado."
		if ss.dry {
			note = "Simulação: comandos não executados."
		}
		o.updateStep(ref, func(s *Step) { s.Status, s.Note = StepDone, note })
		return nil, true
	}
	snaps, results, ok := o.takeSnapshot(ctx, ss)
	elapsed := time.Since(started)
	if !ok {
		o.cancelStep(ref, elapsed)
		return nil, false
	}
	var failed []string
	for _, s := range snaps {
		if !s.Passed {
			failed = append(failed, s.Name)
		}
	}
	summary := fmt.Sprintf("%d comando(s), %d com falha", len(snaps), len(failed))
	if len(failed) > 0 {
		summary += ": " + strings.Join(failed, ", ")
	}
	o.updateStep(ref, func(s *Step) {
		s.Status, s.Checks, s.DurationMS = StepDone, results, elapsed.Milliseconds()
		s.Output = summary + "."
	})
	o.logf(LevelInfo, "%s: %s (%s).", o.stepTitle(ref), summary, fmtSeconds(elapsed))
	return snaps, true
}

// commitCleanup records the agent's work on the branch and lists the files
// it changed; without git, the files modified since the run started.
func (o *Orchestrator) commitCleanup(ctx context.Context, ss *session, area string, started time.Time) []string {
	var changed []string
	if ss.git == nil {
		changed = quality.ChangedSince(ss.repo, started)
	} else {
		c, err := ss.git.CommitAll(ctx, "faztudo(saude): "+areaLabel(area), dependencyPaths(ss.git.Nested())...)
		if err != nil {
			o.logf(LevelWarn, "Não foi possível versionar a limpeza: %v.", err)
		}
		if c != nil {
			o.updateStep(cleanupAgentRef, func(s *Step) { s.Commit = c })
			for _, f := range c.Files {
				changed = append(changed, f.Path)
			}
		}
	}
	var out []string
	for _, f := range changed {
		if !strings.HasPrefix(f, cleanupOutDir+"/") {
			out = append(out, f)
		}
	}
	return out
}

// keepDecisionLog reads the agent's decision log into the run and copies it
// beside the saved runs, where it outlives the repository's ignored files.
func (o *Orchestrator) keepDecisionLog(repo, rel string) {
	data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		o.logf(LevelWarn, "O agente não deixou o registro de decisões em %s.", rel)
		return
	}
	o.updateCleanup(func(c *CleanupInfo) { c.DecisionLog = clip(string(data), maxDecisionLog) })
	o.mu.RLock()
	dir := o.journalDir
	o.mu.RUnlock()
	if dir == "" {
		return
	}
	sum := sha1.Sum([]byte(repo))
	file := filepath.Join(filepath.Dir(dir), "saude", filepath.Base(repo)+"-"+hex.EncodeToString(sum[:4])+"-"+path.Base(rel))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err == nil {
		if err = os.WriteFile(file, data, 0o644); err == nil {
			o.logf(LevelInfo, "Registro de decisões copiado para %s.", file)
		}
	}
}

// closeCleanup records the verdict and ends the run: approved is a success,
// rejected a failure whose branch stays for inspection.
func (o *Orchestrator) closeCleanup(final []CheckSnapshot, changed, reasons []string, started time.Time) {
	verdict := VerdictApproved
	if len(reasons) > 0 {
		verdict = VerdictRejected
	}
	o.updateCleanup(func(c *CleanupInfo) {
		c.Final, c.Changed, c.Verdict, c.Reasons = final, changed, verdict, reasons
	})
	elapsed := time.Since(started).Milliseconds()
	snap := o.Snapshot()
	branch := snap.Stories[0].Branch
	if verdict == VerdictRejected {
		o.updateStep(cleanupVerifyRef, func(s *Step) { s.Status, s.Error = StepFailed, strings.Join(reasons, " ") })
		o.updateStory(0, func(r *StoryRun) { r.Status, r.DurationMS = StepFailed, elapsed })
		msg := "Limpeza reprovada: " + strings.Join(reasons, " ")
		if branch != "" {
			msg += " Branch " + branch + " mantida para inspeção."
		}
		o.finish(ResultFailed, LevelError, msg)
		return
	}
	o.updateStory(0, func(r *StoryRun) { r.Status, r.DurationMS = StepDone, elapsed })
	msg := fmt.Sprintf("Limpeza aprovada: %d arquivo(s) alterado(s), sem falha nova.", len(changed))
	if branch != "" {
		msg += " Revise a branch " + branch + " e faça o merge se quiser."
	}
	o.finish(ResultSucceeded, LevelSuccess, msg)
}

func fmtSeconds(d time.Duration) string {
	return fmt.Sprintf("%.0f s", d.Seconds())
}

func (o *Orchestrator) updateCleanup(fn func(*CleanupInfo)) {
	o.mu.Lock()
	if o.run.Cleanup != nil {
		fn(o.run.Cleanup)
	}
	o.mu.Unlock()
	o.notify()
}
