package pipeline

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/scm"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/vcs"
	"llm-orchestrator/internal/workspace"
)

// Integration is what happened to a story's branch and the base, apart from
// whether the story finished (Status) and was approved (Validation).
type Integration string

const (
	// IntegrationIntegrated means the story is in the base branch.
	IntegrationIntegrated Integration = "integrated"
	// IntegrationBlocked means the story was kept out of the base: its branch
	// stays for review and the stories depending on it do not run.
	IntegrationBlocked Integration = "blocked"
	// IntegrationPartial means some repositories took the story and others
	// did not: nothing was reset, the run stops and Reconcile completes it.
	IntegrationPartial Integration = "partial"
)

// RepoPromotion is one repository's side of a promotion: the candidate's
// commit there, where its base stood before and after, and what happened.
type RepoPromotion struct {
	Path      string      `json:"path"`
	Base      string      `json:"base"`
	Candidate string      `json:"candidate,omitempty"`
	Before    string      `json:"before,omitempty"`
	After     string      `json:"after,omitempty"`
	Status    Integration `json:"status,omitempty"`
}

// Promotion records one decision on merging a candidate (a story's branch or
// a wave's integration branch) into the base.
type Promotion struct {
	Stories []string    `json:"stories"`
	Branch  string      `json:"branch"`
	Commit  string      `json:"commit,omitempty"`
	Status  Integration `json:"status"`
	Reason  string      `json:"reason,omitempty"`
	// Checks are the project commands run on the candidate itself; empty
	// when the story's own verification still covered the same code.
	Checks []quality.Result `json:"checks,omitempty"`
	// Repos are the promotion per repository (the parent and each nested one).
	Repos []RepoPromotion `json:"repos,omitempty"`
	// PR is the pull request the candidate went to, in review mode.
	PR *scm.PullRequest `json:"pr,omitempty"`
	At time.Time        `json:"at"`
}

// promotable is the single rule every route to the base goes through,
// sequential or parallel, first attempt or retry: only a story whose delivery
// was approved may enter it. It returns why the story may not, or "".
// A story not evaluated (no command ran, nothing to judge) is promoted only
// when the policy says so explicitly.
func promotable(st StoryRun, policy workspace.Policy) string {
	switch st.Status {
	case StepSkipped:
		return "US não desenvolvida"
	case StepWaiting:
		return "US aguarda decisão de produto"
	case StepFailed, StepCanceled, StepRunning, StepPending:
		return "US não concluída"
	}
	switch cmpValidation(st.Validation) {
	case ValidationPassed:
		return ""
	case ValidationFailed:
		return "validação reprovada"
	case ValidationInconclusive:
		return "validação inconclusiva"
	}
	if policy.PromoteUnverified {
		return ""
	}
	return "sem validação executada (configure os comandos do projeto ou permita promover sem validação)"
}

// verifyCandidate runs the project commands on the candidate checked out in
// ss.repo, from the cheapest layer, stopping at the first that fails. It
// returns the results and why the candidate may not be promoted ("" when it
// may); false means ctx was canceled.
func (o *Orchestrator) verifyCandidate(ctx context.Context, ss *session, label string) ([]quality.Result, string, bool) {
	if len(ss.checks) == 0 {
		if ss.policy.PromoteUnverified {
			return nil, "", true
		}
		return nil, "nenhum comando de verificação para o candidato", true
	}
	o.logf(LevelInfo, "Verificando o candidato à base (%s).", label)
	timeout := time.Duration(ss.policy.CheckTimeoutS) * time.Second
	var results []quality.Result
	var failed []string
	failedLayer := -1
	for _, c := range quality.InLayers(ss.checks) {
		if failedLayer >= 0 && quality.Layer(c) > failedLayer {
			continue
		}
		r := o.runGated(ctx, ss, ss.repo, c, timeout)
		if ctx.Err() != nil {
			return results, "", false
		}
		results = append(results, r)
		if !r.Passed {
			failedLayer = quality.Layer(c)
			failed = append(failed, c.Name)
		}
	}
	if len(failed) > 0 {
		return results, fmt.Sprintf("%s falhou no candidato", strings.Join(failed, ", ")), true
	}
	return results, "", true
}

// baseMoved tells why the base cannot take rev without a new verification:
// a base that got commits rev lacks would end up with code no one checked.
func (o *Orchestrator) baseMoved(ctx context.Context, ss *session, rev string) string {
	moved, err := ss.git.MovedBases(ctx, rev, ss.bases)
	switch {
	case err != nil:
		return fmt.Sprintf("não foi possível comparar com a base: %v", err)
	case len(moved) > 0:
		return fmt.Sprintf("a base mudou depois da verificação (%s); integre de novo", strings.Join(moved, ", "))
	}
	return ""
}

// promoteStory decides whether a story developed in place enters the base.
// The candidate is verified again when anything changed after the story's
// last approved verification (documentation, for one). When the story is
// kept out, the repository goes back to the base, so the next story does not
// build on it.
func (o *Orchestrator) promoteStory(ctx context.Context, ss *session, st *storyState) bool {
	if ss.git == nil || ss.bases["."] == "" {
		return true
	}
	from := ss.git.Branch(ctx)
	if from == "" || from == ss.bases["."] {
		return true
	}
	if st.waiting > 0 {
		// Nothing was built: back to the base, the story waits for its answers.
		if err := ss.git.SwitchBranches(ctx, ss.bases); err != nil {
			o.failRun(fmt.Errorf("voltar para a branch base: %w", err))
			return false
		}
		return true
	}
	label := storyLabel(st.sc.Story)
	p := Promotion{Stories: []string{label}, Branch: from}
	reason := promotable(o.storyRun(st.si), ss.policy)
	if reason == "" && (st.verifiedAt.IsZero() || len(o.changedFiles(ss, st.verifiedAt)) > 0) {
		var alive bool
		if p.Checks, reason, alive = o.verifyCandidate(ctx, ss, label); !alive {
			return false
		}
	}
	if reason == "" {
		reason = o.baseMoved(ctx, ss, from)
	}
	p.Commit, _ = ss.git.Head(ctx)
	if reason != "" {
		return o.blockPromotion(ctx, ss, []int{st.si}, p, reason)
	}
	if o.needsReview(ss, []int{st.si}) {
		return o.openReview(ctx, ss, st.sc.Story.Title, &p, []int{st.si})
	}
	if !o.mergeIntoBase(ctx, ss, st.sc.Story.Title, &p, []int{st.si}) {
		return false
	}
	p.Status = IntegrationIntegrated
	o.recordPromotion(p, []int{st.si})
	return true
}

// blockPromotion keeps a candidate out of the base and puts the repository
// back on it; the candidate's branches stay for review.
func (o *Orchestrator) blockPromotion(ctx context.Context, ss *session, stories []int, p Promotion, reason string) bool {
	p.Status, p.Reason = IntegrationBlocked, reason
	o.recordPromotion(p, stories)
	o.logf(LevelWarn, "%s fora da branch base: %s. A branch %s fica para revisão.", strings.Join(p.Stories, ", "), reason, p.Branch)
	if err := ss.git.SwitchBranches(ctx, ss.bases); err != nil {
		o.failRun(fmt.Errorf("voltar para a branch base depois de bloquear %s: %w", strings.Join(p.Stories, ", "), err))
		return false
	}
	return true
}

// recordPromotion stores a decision and marks its stories. A story kept out
// of the base was not delivered, whatever its validation: it is pending.
func (o *Orchestrator) recordPromotion(p Promotion, stories []int) {
	p.At = time.Now()
	o.mu.Lock()
	o.run.Promotions = append(o.run.Promotions, p)
	for _, si := range stories {
		o.markIntegrationLocked(si, p.Status, p.Reason)
	}
	o.mu.Unlock()
	o.notify()
}

func (o *Orchestrator) markIntegration(si int, status Integration, note string) {
	o.mu.Lock()
	o.markIntegrationLocked(si, status, note)
	o.mu.Unlock()
	o.notify()
}

func (o *Orchestrator) markIntegrationLocked(si int, status Integration, note string) {
	r := &o.run.Stories[si]
	r.Integration, r.IntegrationNote = status, note
	if status == IntegrationBlocked && r.Status == StepDone {
		r.Status = StepWarning
	}
}

func (o *Orchestrator) storyRun(si int) StoryRun {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.run.Stories[si]
}

// unmetDependency tells why story si cannot start: a story it depends on was
// kept out of the base or never developed, so building on the base would
// miss it. Only the dependencies the analysis found count (an unreadable
// analysis already chains every story to the one before); without an
// analysis the stories are independent, each starting from the base, never
// from a branch that was kept out of it.
func (o *Orchestrator) unmetDependency(si int, list []stories.Story) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	deps := o.dependenciesLocked(si, list)
	for _, d := range deps {
		dep := o.run.Stories[d]
		name := cmp.Or(dep.ID, dep.Title)
		switch {
		case dep.Status == StepWaiting:
			return fmt.Sprintf("%s %s, que aguarda decisão de produto", dependsPrefix, name)
		case dep.Integration == IntegrationAwaitingReview:
			return fmt.Sprintf("%s %s, que aguarda revisão no pull request", dependsPrefix, name)
		case dep.Integration == IntegrationPartial:
			return fmt.Sprintf("%s %s, cuja integração ficou parcial", dependsPrefix, name)
		case dep.Integration == IntegrationBlocked || dep.Status == StepSkipped:
			return fmt.Sprintf("%s %s, que não entrou na base", dependsPrefix, name)
		case o.run.Bases["."] != "" && dep.Integration != IntegrationIntegrated:
			// The dependent builds on the base: what it needs must be there,
			// in an approved and identified snapshot.
			return fmt.Sprintf("%s %s, que ainda não está na base", dependsPrefix, name)
		}
	}
	return ""
}

func (o *Orchestrator) dependenciesLocked(si int, list []stories.Story) []int {
	index := map[string]int{}
	for i, k := range storyKeys(list) {
		index[strings.ToLower(k)] = i
	}
	var deps []int
	for _, id := range o.run.Stories[si].DependsOn {
		if i, ok := index[strings.ToLower(id)]; ok && i != si {
			deps = append(deps, i)
		}
	}
	return deps
}

// dependencySnapshots names the base commit each dependency entered with,
// for the record of what a story was built on.
func (o *Orchestrator) dependencySnapshots(si int, list []stories.Story) map[string]string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := map[string]string{}
	for _, d := range o.dependenciesLocked(si, list) {
		label := storyLabel(list[d])
		for i := len(o.run.Promotions) - 1; i >= 0; i-- {
			p := o.run.Promotions[i]
			if p.Status == IntegrationIntegrated && slices.Contains(p.Stories, label) {
				out[label] = p.Commit
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// dependsPrefix starts the note of a story skipped for an unmet dependency.
const dependsPrefix = "depende de"

// skippedByDependency reports whether a story was left out only because of a
// dependency, so it runs again when the dependency is resumed.
func skippedByDependency(r StoryRun) bool {
	return r.Status == StepSkipped && strings.HasPrefix(r.IntegrationNote, dependsPrefix)
}

// skipStory leaves a story out of the run because of an unmet dependency.
func (o *Orchestrator) skipStory(si int, reason string) {
	o.updateStory(si, func(r *StoryRun) {
		r.Status = StepSkipped
		r.Validation = ValidationNotEvaluated
		r.Integration, r.IntegrationNote = IntegrationBlocked, reason
		for i := range r.Steps {
			if r.Steps[i].Status == StepPending {
				r.Steps[i].Status, r.Steps[i].Note = StepSkipped, "US bloqueada: "+reason
			}
		}
	})
	o.logf(LevelWarn, "%s não iniciada: %s.", o.storyTitle(si), reason)
}

// mergeIntoBase merges the current branch into the bases and fills p.Repos
// with where each repository stood. A merge that reached only some of the
// repositories is recorded as partial: nothing is reset, the run stops, and
// Reconcile completes it.
func (o *Orchestrator) mergeIntoBase(ctx context.Context, ss *session, label string, p *Promotion, stories []int) bool {
	if ss.git == nil || ss.bases["."] == "" {
		return true
	}
	from := ss.git.Branch(ctx)
	if from == "" || from == ss.bases["."] {
		return true
	}
	sha, _ := ss.git.Rev(ctx, from)
	before, cands := ss.git.BaseHeads(ctx, ss.bases), ss.git.CandidateHeads(ctx, from)
	key := operationKey("merge-base", sha, ss.bases["."])
	if op, done := o.operationDone(key); done || (sha != "" && ss.git.IsAncestor(ctx, sha, ss.bases["."])) {
		// A resumed run finds the merge made before the interruption.
		if err := ss.git.SwitchBranches(ctx, ss.bases); err != nil {
			o.failRun(fmt.Errorf("voltar para a branch base: %w", err))
			return false
		}
		if !done {
			o.recordOperation(Operation{Key: key, Kind: "merge-base", Target: ss.bases["."], Result: sha})
		}
		p.Repos = repoPromotions(ss.bases, cands, before, ss.git.BaseHeads(ctx, ss.bases))
		o.logf(LevelInfo, "%s já estava na branch base (%s); merge não repetido.", label, cmpOrString(op.Result, short(sha)))
		return true
	}
	_, err := ss.git.MergeIntoBasesKeeping(ctx, from, fmt.Sprintf("faztudo: integra %s na base", label), ss.bases)
	p.Repos = repoPromotions(ss.bases, cands, before, ss.git.BaseHeads(ctx, ss.bases))
	var partial *vcs.PartialMergeError
	switch {
	case errors.As(err, &partial):
		p.Status, p.Reason = IntegrationPartial, partial.Error()
		p.Commit = sha
		o.recordPromotion(*p, stories)
		o.failRun(fmt.Errorf("%s: %w. Nada foi desfeito; use \"Reconciliar integração\" (POST /api/pipeline/reconcile) "+
			"para completar, ou desfaça manualmente nos repositórios integrados", label, partial))
		return false
	case err != nil:
		o.failRun(fmt.Errorf("merge de %s na branch base falhou (branch %s mantida): %w", label, from, err))
		return false
	}
	o.recordOperation(Operation{Key: key, Kind: "merge-base", Target: ss.bases["."], Result: sha})
	o.logf(LevelSuccess, "%s integrada na branch base (%s).", label, baseLabels(ctx, ss.git))
	return true
}

// repoPromotions describes each repository's side of a promotion.
func repoPromotions(bases, cands, before, after map[string]string) []RepoPromotion {
	paths := make([]string, 0, len(bases))
	for path := range bases {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var out []RepoPromotion
	for _, path := range paths {
		r := RepoPromotion{Path: path, Base: bases[path], Candidate: cands[path], Before: before[path], After: after[path]}
		switch {
		case r.Candidate == "":
			continue // the candidate does not touch this repository
		case r.After != "" && r.After != r.Before:
			r.Status = IntegrationIntegrated
		case r.Before == r.Candidate:
			r.Status = IntegrationIntegrated // already there
		default:
			r.Status = IntegrationBlocked
		}
		out = append(out, r)
	}
	return out
}
