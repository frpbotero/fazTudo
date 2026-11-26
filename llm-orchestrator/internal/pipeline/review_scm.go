package pipeline

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"llm-orchestrator/internal/scm"
	"llm-orchestrator/internal/workspace"
)

// IntegrationAwaitingReview means the candidate went to a pull request: it
// enters the base when the reviewers and the host's checks let it, not
// before. Stories depending on it wait.
const IntegrationAwaitingReview Integration = "awaiting_review"

// maxPRBody stays under the host's limit for a pull request's body.
const maxPRBody = 60_000

// needsReview tells whether the stories go through a pull request instead of
// a local merge: only when the policy publishes, and from its risk level up.
func (o *Orchestrator) needsReview(ss *session, stories []int) bool {
	p := ss.policy.SCM
	if p.Mode != workspace.SCMPullRequest || ss.dry {
		return false
	}
	if p.ReviewFrom == "" {
		return true
	}
	return slices.ContainsFunc(stories, func(si int) bool {
		r := o.storyRun(si).Risk
		return r != nil && workspace.RiskRank(r.Level) >= workspace.RiskRank(p.ReviewFrom)
	})
}

// openReview publishes the candidate branch and opens its pull request with
// the evidence, once: a resumed run finds the operation done, or the host's
// open pull request from the branch, and does not open another. The base is
// left as it was and the repository goes back to it.
func (o *Orchestrator) openReview(ctx context.Context, ss *session, label string, p *Promotion, stories []int) bool {
	branch := ss.git.Branch(ctx)
	sha, _ := ss.git.Rev(ctx, branch)
	p.Branch, p.Commit = branch, sha
	if len(ss.git.Nested()) > 0 {
		return o.blockPromotion(ctx, ss, stories, *p, "pull request em repositórios aninhados ainda não é suportado; a branch fica para revisão")
	}
	key := operationKey("pr", branch, sha)
	var pr *scm.PullRequest
	if op, done := o.operationDone(key); done {
		pr = &scm.PullRequest{URL: op.Result, Head: branch}
	} else {
		var err error
		if pr, err = o.preparePR(ctx, ss, label, branch, sha, stories); err != nil {
			return o.blockPromotion(ctx, ss, stories, *p, "pull request não aberto: "+err.Error())
		}
		o.recordOperation(Operation{Key: key, Kind: "pr", Target: ss.bases["."], Result: pr.URL})
	}
	p.PR = pr
	p.Status, p.Reason = IntegrationAwaitingReview, "aguarda revisão no pull request "+pr.URL
	o.recordPromotion(*p, stories)
	o.logf(LevelSuccess, "%s publicada para revisão: %s (commit avaliado %s).", label, pr.URL, short(sha))
	if err := ss.git.SwitchBranches(ctx, ss.bases); err != nil {
		o.failRun(fmt.Errorf("voltar para a branch base: %w", err))
		return false
	}
	return true
}

// preparePR pushes the branch and opens its pull request, or reuses the one
// the host already has open from it.
func (o *Orchestrator) preparePR(ctx context.Context, ss *session, label, branch, sha string, stories []int) (*scm.PullRequest, error) {
	prov, err := o.scmFor(ctx, ss.repo)
	if err != nil {
		return nil, err
	}
	existing, err := prov.Find(ctx, branch)
	if err != nil {
		return nil, err
	}
	if err := prov.Publish(ctx, branch); err != nil {
		return nil, err
	}
	if existing != nil {
		o.logf(LevelInfo, "Pull request de %s já aberto (%s); não foi criado outro.", branch, existing.URL)
		return existing, nil
	}
	return prov.Create(ctx, scm.Request{
		Head: branch, Base: ss.bases["."], Title: "faztudo: " + label,
		Body: o.reviewBody(sha, stories), Draft: ss.policy.SCM.Draft,
	})
}

// reviewBody is the pull request's description: the evaluated commit and
// each story's evidence, redacted, as in the delivery package.
func (o *Orchestrator) reviewBody(sha string, stories []int) string {
	run := o.Snapshot()
	var b strings.Builder
	fmt.Fprintf(&b, "Preparado pelo FazTudo. **Commit avaliado:** `%s`.\n\n", sha)
	b.WriteString("A evidência abaixo complementa os checks do CI e a revisão humana; não é garantia por si só. " +
		"O merge segue as regras do repositório e o estado atual da base.\n\n")
	for _, si := range stories {
		b.WriteString(renderStoryDelivery(run, run.Stories[si]))
		b.WriteString("\n---\n\n")
	}
	body := redactSecrets(b.String())
	if len(body) > maxPRBody {
		body = body[:maxPRBody] + "\n\n[… evidência truncada; o pacote de entrega tem o restante]"
	}
	return body
}

// ReviewStatus is a pull request the run opened, with what the host says now.
type ReviewStatus struct {
	Stories []string        `json:"stories"`
	PR      scm.PullRequest `json:"pr"`
	Checks  []scm.Check     `json:"checks,omitempty"`
	Review  scm.Review      `json:"review"`
	// Stale says the pull request's head is not the commit the run evaluated.
	Stale bool   `json:"stale,omitempty"`
	Error string `json:"error,omitempty"`
}

// ReviewStatuses reads, from the host, the checks and the review of every
// pull request the last run opened.
func (o *Orchestrator) ReviewStatuses(ctx context.Context) ([]ReviewStatus, error) {
	run := o.Snapshot()
	var out []ReviewStatus
	var prov scm.Provider
	for _, p := range run.Promotions {
		if p.PR == nil {
			continue
		}
		if prov == nil {
			var err error
			if prov, err = o.scmFor(ctx, run.Repository); err != nil {
				return nil, err
			}
		}
		st := ReviewStatus{Stories: p.Stories, PR: *p.PR}
		number := p.PR.Number
		if number == 0 {
			if pr, err := prov.Find(ctx, p.Branch); err == nil && pr != nil {
				st.PR, number = *pr, pr.Number
			}
		}
		var errs []string
		if checks, err := prov.Checks(ctx, number); err == nil {
			st.Checks = checks
		} else {
			errs = append(errs, err.Error())
		}
		if review, err := prov.Review(ctx, number); err == nil {
			st.Review = review
			st.Stale = review.HeadSHA != "" && p.Commit != "" && review.HeadSHA != p.Commit
		} else {
			errs = append(errs, err.Error())
		}
		st.Error = cmp.Or(strings.Join(errs, "; "))
		out = append(out, st)
	}
	return out, nil
}
