// Step bookkeeping: the locked updates every part of the run goes through.

package pipeline

import (
	"fmt"
	"log/slog"
	"slices"
	"time"
)

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
	limit := maxLogs
	if o.ws != nil {
		if l := o.ws.Policy().LogLimit; l > 0 {
			limit = l
		}
	}
	o.mu.Lock()
	o.run.Logs = append(o.run.Logs, LogEntry{Time: time.Now(), Level: level, Message: redactSecrets(fmt.Sprintf(format, args...))})
	if over := len(o.run.Logs) - limit; over > 0 {
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
