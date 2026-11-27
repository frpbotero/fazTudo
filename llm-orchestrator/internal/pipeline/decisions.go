package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"llm-orchestrator/internal/workspace"
)

// ErrNotWaiting is returned when answering a story that waits on no decision.
var ErrNotWaiting = errors.New("a US não aguarda decisão de produto")

// maxAnswerLen bounds each answer.
const maxAnswerLen = 2000

// AnswerDecisions records the user's answers to the product decisions story
// si waits on and develops it again from Passo 0, on a new branch: the
// artifacts that depended on the open question are all made again. The story
// text is kept as written; the answers travel beside it. Stories skipped only
// because they depend on it run again too.
func (o *Orchestrator) AnswerDecisions(si int, answers []string) error {
	o.mu.Lock()
	if o.busyLocked() {
		o.mu.Unlock()
		return ErrAlreadyRunning
	}
	if o.run.Kind == RunCleanup {
		o.mu.Unlock()
		return ErrCleanupNotResumable
	}
	if si < 0 || si >= len(o.run.Stories) || len(o.last.Stories) != len(o.run.Stories) ||
		o.run.Stories[si].Status != StepWaiting || o.run.Stories[si].Readiness == nil {
		o.mu.Unlock()
		return ErrNotWaiting
	}
	open := o.run.Stories[si].Readiness.Decisions
	if len(answers) != len(open) {
		o.mu.Unlock()
		return fmt.Errorf("responda as %d decisão(ões) em aberto", len(open))
	}
	for i, a := range answers {
		if a = strings.TrimSpace(a); a == "" || len(a) > maxAnswerLen {
			o.mu.Unlock()
			return fmt.Errorf("resposta %d: informe de 1 a %d caracteres", i+1, maxAnswerLen)
		}
	}
	unlock, err := workspace.LockRepository(o.ws.Path())
	if err != nil {
		o.mu.Unlock()
		return err
	}

	now := time.Now()
	story := &o.run.Stories[si]
	for i, d := range open {
		d.Answer, d.AnsweredAt = strings.TrimSpace(answers[i]), &now
		story.Answers = append(story.Answers, d)
	}
	o.resetStoryLocked(si)
	for i := range o.run.Stories {
		if i != si && skippedByDependency(o.run.Stories[i]) {
			o.resetStoryLocked(i)
		}
	}
	o.relaunchLocked(si, unlock, fmt.Sprintf("Decisões de %s respondidas; a US recomeça do Passo 0.", story.Title))
	return nil
}

// relaunchLocked runs the last run again from story si's wave, the stories
// already finished kept as they are. Caller holds o.mu and the repository
// lock, whose unlock runs when the run ends; o.mu is released here.
func (o *Orchestrator) relaunchLocked(si int, unlock func(), msg string) {
	o.run.Running = true
	o.run.Result = ""
	o.run.Validation = ""
	o.run.FinishedAt = nil
	o.run.Resume = nil
	o.run.Current = si
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
	o.logf(LevelInfo, "%s", msg)

	started := time.Now()
	go func() {
		defer close(done)
		defer unlock()
		defer cancel()
		defer o.sweepRun(o.ws.Path(), opts.DryRun)
		o.sweepRun(o.ws.Path(), opts.DryRun)
		o.retryExecute(ctx, opts, si, -1, started)
	}()
}

// resetStoryLocked puts a story back to its start, keeping only the user's
// answers. Caller holds o.mu.
func (o *Orchestrator) resetStoryLocked(i int) {
	r := &o.run.Stories[i]
	*r = StoryRun{
		ID: r.ID, Title: r.Title, Status: StepPending, Steps: o.freshSteps(),
		Wave: r.Wave, DependsOn: r.DependsOn, Answers: r.Answers,
	}
}
