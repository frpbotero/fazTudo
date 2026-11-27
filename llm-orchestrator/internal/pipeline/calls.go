// Agent calls: running a stage, retrying it, and recording how it ended,
// up to the run's own end.

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/vcs"
	"time"
)

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
	o.recordCall(ref, req, resp)
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
		resp, err := o.callAgent(ctx, ref, req, attempt)
		if errors.Is(err, provider.ErrTurnLimit) {
			return o.turnLimited(ref, req, resp, err)
		}
		if err == nil || ctx.Err() != nil || attempt >= o.attempts || !retryable(err) {
			return resp, err
		}
		wait, ok := o.mayRetry(o.ws.Policy(), attempt)
		if !ok {
			o.logf(LevelWarn, "%s falhou e o orçamento de tentativas acabou.", o.stepTitle(ref))
			return resp, err
		}
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
		s.FailureClass = classifyFailure(err)
		if elapsed > 0 {
			s.DurationMS = elapsed.Milliseconds()
		}
		title = s.Title
	})
	o.abortRemaining(ref.story, StepFailed)
	msg := fmt.Sprintf("Falha na etapa \"%s\": %v", title, err)
	if o.isLane(ref.story) && !isGlobalFailure(err) {
		// Only this story stops; the others of its wave go on.
		o.logf(LevelError, "%s. As demais US da onda continuam.", msg)
		return
	}
	o.finish(ResultFailed, LevelError, msg)
}

// isGlobalFailure tells a failure no story can get past, such as no
// provider able to run agents, from one that concerns a single story.
func isGlobalFailure(err error) bool {
	return errors.Is(err, provider.ErrNoCapableProvider) || errors.Is(err, provider.ErrNoProvider)
}

func (o *Orchestrator) isLane(si int) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.lanes[si]
}

func (o *Orchestrator) setLanes(stories []int, on bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lanes == nil {
		o.lanes = map[int]bool{}
	}
	for _, si := range stories {
		if on {
			o.lanes[si] = true
		} else {
			delete(o.lanes, si)
		}
	}
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
	o.captureHeads()
	o.writePendencies(o.Snapshot())
	o.writeDelivery(o.Snapshot())
	o.logf(level, "%s", msg)
	o.logger.Info("pipeline finished", "result", result)
}

// captureHeads records where each story's branch stands when the run ends,
// so a resume can tell commits made outside the run.
func (o *Orchestrator) captureHeads() {
	o.mu.RLock()
	repo, dry := o.run.Repository, o.run.DryRun
	branches := make([]string, len(o.run.Stories))
	for i, st := range o.run.Stories {
		branches[i] = st.Branch
	}
	o.mu.RUnlock()
	if dry || repo == "" {
		return
	}
	ctx := context.Background()
	g, _, err := vcs.Open(ctx, repo)
	if err != nil || g == nil {
		return
	}
	for i, b := range branches {
		if b == "" {
			continue
		}
		if head, err := g.Rev(ctx, b); err == nil {
			o.updateStory(i, func(r *StoryRun) { r.Head = head })
		}
	}
}
