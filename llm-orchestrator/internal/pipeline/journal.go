package pipeline

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// ResumePoint is where a stopped run continues: the failed step, or the step
// that was running when the run was canceled or the app closed.
type ResumePoint struct {
	Story int    `json:"story"` // -1 for the discovery or the dependency analysis
	Step  string `json:"step"`
	Title string `json:"title"`
	// StoryTitle names the story; empty for run-level steps.
	StoryTitle string `json:"story_title,omitempty"`
}

// journal is the last run of a repository as saved on disk: its steps and
// outputs (the architect's plan included), branches, waves, logs and the
// options it started with, enough to continue it after the app restarts.
type journal struct {
	Run     Run          `json:"run"`
	Options StartOptions `json:"options"`
	SavedAt time.Time    `json:"saved_at"`
}

// journalSaveInterval spaces the saves of a busy run; logs change it often.
const journalSaveInterval = time.Second

// SetJournalDir keeps the last run of each repository under dir and loads
// the current repository's one. An empty dir disables it.
func (o *Orchestrator) SetJournalDir(dir string) {
	o.mu.Lock()
	o.journalDir = dir
	if dir != "" && o.journalDirty == nil {
		o.journalDirty = make(chan struct{}, 1)
		go o.saveJournalLoop(o.journalDirty)
	}
	if !o.busyLocked() {
		o.loadJournalLocked(o.ws.Path())
	}
	o.mu.Unlock()
	o.notify()
}

// FlushJournal writes the run now; the app calls it on shutdown, after the
// run stopped, so the interruption is saved.
func (o *Orchestrator) FlushJournal() {
	o.writeJournal()
}

// Resume continues the stopped run from where it stopped. Another window may
// have advanced the repository's run, so the saved one is reloaded first.
func (o *Orchestrator) Resume() error {
	o.mu.Lock()
	if o.busyLocked() {
		o.mu.Unlock()
		return ErrAlreadyRunning
	}
	if !o.isJournalOwner {
		o.loadJournalLocked(o.ws.Path())
	}
	point := o.resumePointLocked()
	o.mu.Unlock()
	if point == nil {
		return ErrNoFailedStep
	}
	return o.RetryFailed(point.Story, point.Step)
}

// resumePointLocked finds where the stopped run continues: a failed step
// first, else the first interrupted one, in wave order.
func (o *Orchestrator) resumePointLocked() *ResumePoint {
	if !o.isResumableLocked() || len(o.last.Stories) == 0 || len(o.last.Stories) != len(o.run.Stories) {
		return nil
	}
	for _, s := range []*Step{&o.run.Discovery, o.run.Analysis} {
		if s != nil && o.isStoppedLocked(s.Status) {
			return &ResumePoint{Story: -1, Step: s.ID, Title: s.Title}
		}
	}
	order := slices.Concat(o.run.Waves...)
	if len(order) != len(o.run.Stories) {
		order = slices.Concat(sequentialWaves(len(o.run.Stories))...)
	}
	for _, wanted := range []StepStatus{StepFailed, StepCanceled} {
		for _, si := range order {
			st := o.run.Stories[si]
			if isFinished(st.Status) {
				continue
			}
			for _, step := range st.Steps {
				if step.Status == wanted && o.isStoppedLocked(step.Status) {
					return &ResumePoint{Story: si, Step: step.ID, Title: step.Title, StoryTitle: st.Title}
				}
			}
		}
	}
	return nil
}

// requestJournalSave asks the saver to write the run; it never blocks.
func (o *Orchestrator) requestJournalSave() {
	o.mu.RLock()
	dirty, isOwner := o.journalDirty, o.isJournalOwner
	o.mu.RUnlock()
	if dirty == nil || !isOwner {
		return
	}
	select {
	case dirty <- struct{}{}:
	default:
	}
}

func (o *Orchestrator) saveJournalLoop(dirty <-chan struct{}) {
	for range dirty {
		o.writeJournal()
		time.Sleep(journalSaveInterval)
	}
}

// writeJournal saves the run atomically. Only the window that ran it writes:
// an idle window on the same repository must not overwrite a newer run.
func (o *Orchestrator) writeJournal() {
	o.mu.RLock()
	dir, isOwner, repo, opts := o.journalDir, o.isJournalOwner, o.run.Repository, o.last
	o.mu.RUnlock()
	if dir == "" || !isOwner || repo == "" {
		return
	}
	data, err := json.Marshal(journal{Run: o.Snapshot(), Options: opts, SavedAt: time.Now()})
	if err != nil {
		return
	}
	o.journalMu.Lock()
	defer o.journalMu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		o.logger.Warn("journal da execução não salvo", "err", err)
		return
	}
	file := journalFile(dir, repo)
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		o.logger.Warn("journal da execução não salvo", "err", err)
		return
	}
	if err := os.Rename(tmp, file); err != nil {
		o.logger.Warn("journal da execução não salvo", "err", err)
	}
}

// loadJournalLocked replaces the idle run with the repository's saved one.
// A run saved while still running was cut by the app closing or crashing:
// it is marked interrupted, ready to resume.
func (o *Orchestrator) loadJournalLocked(repo string) {
	if o.journalDir == "" || repo == "" {
		return
	}
	data, err := os.ReadFile(journalFile(o.journalDir, repo))
	if err != nil {
		return
	}
	var j journal
	if json.Unmarshal(data, &j) != nil || j.Run.Repository != repo {
		return
	}
	if j.Run.Running {
		markInterrupted(&j.Run, j.SavedAt)
	}
	j.Run.Logs = append(j.Run.Logs, LogEntry{Time: time.Now(), Level: LevelInfo,
		Message: "Última execução deste repositório carregada (" + j.SavedAt.Format("02/01 15:04") + ")."})
	o.run, o.last = j.Run, j.Options
	o.isJournalOwner = false
}

// markInterrupted closes a run the app never finished: running and pending
// steps and stories become canceled, so the run can resume from them.
func markInterrupted(run *Run, at time.Time) {
	cancelOpen := func(s *Step) {
		if s.Status == StepRunning || s.Status == StepPending {
			s.Status = StepCanceled
		}
	}
	cancelOpen(&run.Discovery)
	if run.Analysis != nil {
		cancelOpen(run.Analysis)
	}
	for i := range run.Stories {
		st := &run.Stories[i]
		for j := range st.Steps {
			cancelOpen(&st.Steps[j])
		}
		if st.Status == StepRunning || st.Status == StepPending {
			st.Status = StepCanceled
		}
	}
	run.Running = false
	run.Result = ResultCanceled
	run.FinishedAt = &at
	run.Logs = append(run.Logs, LogEntry{Time: at, Level: LevelWarn,
		Message: "Execução interrompida: o app fechou no meio. Use \"Continuar de onde parou\"."})
}

func journalFile(dir, repo string) string {
	sum := sha1.Sum([]byte(repo))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
}
