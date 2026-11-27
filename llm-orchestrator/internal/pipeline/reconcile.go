package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/scm"
	"llm-orchestrator/internal/vcs"
	"llm-orchestrator/internal/workspace"
)

// ErrNothingToReconcile is returned when no promotion was left partial and no
// pull request the run opened was merged or closed since.
var ErrNothingToReconcile = errors.New("nada a reconciliar: nenhuma integração parcial nem pull request concluído")

// reconcileTimeout bounds completing a partial merge.
const reconcileTimeout = 2 * time.Minute

// Reconcile completes the last promotion that reached only some of the
// repositories: the ones that already took the candidate are left as they
// are (a merge of what they have is a no-op), the others get it. Nothing is
// reset. On success its stories count as integrated and the run can resume.
// With no partial promotion, it reads the pull requests still awaiting review
// (see reconcileReviews).
func (o *Orchestrator) Reconcile() (Promotion, error) {
	o.mu.Lock()
	if o.busyLocked() {
		o.mu.Unlock()
		return Promotion{}, ErrAlreadyRunning
	}
	i := len(o.run.Promotions) - 1
	for i >= 0 && o.run.Promotions[i].Status != IntegrationPartial {
		i--
	}
	if i < 0 || o.run.Promotions[i].Commit == "" || o.run.Bases["."] == "" || slices.ContainsFunc(o.run.Promotions[i+1:], func(p Promotion) bool {
		return p.Status == IntegrationIntegrated && p.Commit == o.run.Promotions[i].Commit
	}) {
		o.mu.Unlock()
		return o.reconcileReviews()
	}
	unlock, err := workspace.LockRepository(o.ws.Path())
	if err != nil {
		o.mu.Unlock()
		return Promotion{}, err
	}
	defer unlock()
	partial, bases, repo := o.run.Promotions[i], o.run.Bases, o.ws.Path()
	labels := map[string]int{}
	for si, s := range o.last.Stories {
		labels[storyLabel(s)] = si
	}
	o.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()
	g, _, err := vcs.Open(ctx, repo)
	if err != nil || g == nil {
		return Promotion{}, fmt.Errorf("abrir o repositório: %w", err)
	}
	before, cands := g.BaseHeads(ctx, bases), g.CandidateHeads(ctx, partial.Commit)
	p := Promotion{Stories: partial.Stories, Branch: partial.Branch, Commit: partial.Commit}
	_, err = g.MergeIntoBasesKeeping(ctx, partial.Commit, "faztudo: reconcilia "+strings.Join(partial.Stories, ", "), bases)
	p.Repos = repoPromotions(bases, cands, before, g.BaseHeads(ctx, bases))
	if err != nil {
		p.Status, p.Reason = IntegrationPartial, "reconciliação falhou: "+err.Error()
		o.recordPromotion(p, nil)
		o.logf(LevelError, "Reconciliação de %s falhou: %v. Nada foi desfeito.", strings.Join(p.Stories, ", "), err)
		return p, err
	}
	var stories []int
	for _, l := range p.Stories {
		if si, ok := labels[l]; ok {
			stories = append(stories, si)
		}
	}
	p.Status, p.Reason = IntegrationIntegrated, "integração parcial reconciliada"
	o.recordPromotion(p, stories)
	o.recordOperation(Operation{Key: operationKey("merge-base", partial.Commit, bases["."]), Kind: "merge-base", Target: bases["."], Result: partial.Commit})
	o.logf(LevelSuccess, "%s reconciliada: todos os repositórios têm a integração. Continue de onde parou.", strings.Join(p.Stories, ", "))
	o.writeJournal()
	return p, nil
}

// reconcileReviews asks the host about every pull request still awaiting
// review: merged, its stories count as integrated; closed without merge, as
// blocked, with the branch kept. The local base is not touched: the merge
// happened on the host, and the user brings it in with their own git.
func (o *Orchestrator) reconcileReviews() (Promotion, error) {
	o.mu.Lock()
	var waiting []Promotion
	for _, p := range latestReviews(o.run.Promotions) {
		if p.Status == IntegrationAwaitingReview {
			waiting = append(waiting, p)
		}
	}
	repo := o.ws.Path()
	labels := map[string]int{}
	for si, s := range o.last.Stories {
		labels[storyLabel(s)] = si
	}
	o.mu.Unlock()
	if len(waiting) == 0 {
		return Promotion{}, ErrNothingToReconcile
	}

	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()
	prov, err := o.scmFor(ctx, repo)
	if err != nil {
		return Promotion{}, err
	}
	var last *Promotion
	for _, w := range waiting {
		review, err := prov.Review(ctx, w.PR.Number)
		if err != nil {
			return Promotion{}, err
		}
		p := Promotion{Stories: w.Stories, Branch: w.Branch, Commit: w.Commit, PR: w.PR}
		switch review.State {
		case scm.StateMerged:
			p.Status = IntegrationIntegrated
			p.Reason = "merge feito no pull request " + w.PR.URL
			if review.MergeCommit != "" {
				p.Reason += " (commit " + short(review.MergeCommit) + ")"
			}
			p.Reason += "; a base local não muda: traga-a com git pull"
		case scm.StateClosed:
			p.Status = IntegrationBlocked
			p.Reason = "pull request " + w.PR.URL + " fechado sem merge; a branch fica para revisão"
		default:
			continue
		}
		pr := *w.PR
		pr.State = review.State
		p.PR = &pr
		var stories []int
		for _, l := range p.Stories {
			if si, ok := labels[l]; ok {
				stories = append(stories, si)
			}
		}
		o.recordPromotion(p, stories)
		o.logf(LevelInfo, "%s: %s.", strings.Join(p.Stories, ", "), p.Reason)
		last = &p
	}
	if last == nil {
		return Promotion{}, ErrNothingToReconcile
	}
	o.writeJournal()
	return *last, nil
}
