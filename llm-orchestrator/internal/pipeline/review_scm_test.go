package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"llm-orchestrator/internal/scm"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

// fakeSCM is a host that keeps its pull requests in memory.
type fakeSCM struct {
	mu        sync.Mutex
	published []string
	created   []scm.Request
	open      map[string]*scm.PullRequest
	failPush  error
}

func (f *fakeSCM) Host() string { return "fake" }
func (f *fakeSCM) Target(context.Context) (scm.Target, error) {
	return scm.Target{Host: "fake", Repository: "acme/loja"}, nil
}
func (f *fakeSCM) Publish(_ context.Context, b string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPush != nil {
		return f.failPush
	}
	f.published = append(f.published, b)
	return nil
}
func (f *fakeSCM) Find(_ context.Context, head string) (*scm.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open[head], nil
}
func (f *fakeSCM) Create(_ context.Context, r scm.Request) (*scm.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, r)
	pr := &scm.PullRequest{Number: len(f.created), URL: "https://host/pr/" + r.Head, Head: r.Head, Base: r.Base}
	if f.open == nil {
		f.open = map[string]*scm.PullRequest{}
	}
	f.open[r.Head] = pr
	return pr, nil
}
func (f *fakeSCM) Checks(context.Context, int) ([]scm.Check, error) {
	return []scm.Check{{Name: "ci", State: "pass"}}, nil
}
func (f *fakeSCM) Review(context.Context, int) (scm.Review, error) {
	return scm.Review{Decision: "REVIEW_REQUIRED", HeadSHA: "outro"}, nil
}

func newReviewTest(t *testing.T, mutate func(*workspace.SCMPolicy)) (*Orchestrator, string, *fakeSCM) {
	t.Helper()
	o, repo, _ := newPromotionTest(t, true, func(string) bool { return true })
	host := &fakeSCM{}
	o.scmFor = func(context.Context, string) (scm.Provider, error) { return host, nil }
	policy := o.ws.Policy()
	policy.SCM = workspace.SCMPolicy{Mode: workspace.SCMPullRequest, Draft: true}
	mutate(&policy.SCM)
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	return o, repo, host
}

func TestReviewModeOpensAPullRequestInsteadOfMerging(t *testing.T) {
	o, repo, host := newReviewTest(t, func(*workspace.SCMPolicy) {})
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run := waitDone(t, o)
	st := run.Stories[0]
	if st.Integration != IntegrationAwaitingReview || st.Status != StepDone {
		t.Fatalf("story = %s %s, logs %+v", st.Status, st.Integration, run.Logs)
	}
	if exists(repo, "um.txt") {
		t.Error("the base took the story before the review")
	}
	if len(host.created) != 1 || !host.created[0].Draft || host.created[0].Head != st.Branch {
		t.Fatalf("pull requests = %+v", host.created)
	}
	p := run.Promotions[len(run.Promotions)-1]
	if p.PR == nil || p.Commit == "" || !strings.Contains(host.created[0].Body, p.Commit) {
		t.Errorf("the pull request must carry the evaluated commit: %+v", p)
	}
	if !strings.Contains(host.created[0].Body, "complementa os checks do CI") {
		t.Error("the evidence must not present itself as a guarantee")
	}
	statuses, err := o.ReviewStatuses(context.Background())
	if err != nil || len(statuses) != 1 || !statuses[0].Stale || statuses[0].Checks[0].State != "pass" {
		t.Errorf("statuses = %+v, %v", statuses, err)
	}
}

func TestPullRequestIsNotDuplicated(t *testing.T) {
	o, _, host := newReviewTest(t, func(*workspace.SCMPolicy) {})
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run := waitDone(t, o)
	branch := run.Stories[0].Branch
	// The host already has it open: a second attempt reuses it.
	ss := &session{repo: run.Repository, policy: o.ws.Policy()}
	pr, err := o.preparePR(context.Background(), ss, "US-1", branch, "abc", []int{0})
	if err != nil || pr.URL != host.open[branch].URL || len(host.created) != 1 {
		t.Errorf("second preparation = %+v, %v; created %d", pr, err, len(host.created))
	}
}

func TestOnlyRiskyStoriesGoToReview(t *testing.T) {
	o, repo, host := newReviewTest(t, func(p *workspace.SCMPolicy) { p.ReviewFrom = workspace.RiskHigh })
	policy := o.ws.Policy()
	policy.RiskRules = []workspace.RiskRule{{Name: "Auth", Keywords: []string{"login"}, Level: workspace.RiskHigh, Require: []string{"checks"}}}
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	list := []stories.Story{
		{ID: "US-1", Title: "US-1 Catálogo", Content: "<<um>>"},
		{ID: "US-2", Title: "US-2 Login", Content: "<<dois>> login com senha"},
	}
	_ = o.Start(StartOptions{Stories: list, Source: "Markdown"})
	run := waitDone(t, o)
	if st := run.Stories[0]; st.Integration != IntegrationIntegrated || !exists(repo, "um.txt") {
		t.Errorf("a low-risk story merges locally: %s", st.Integration)
	}
	if st := run.Stories[1]; st.Integration != IntegrationAwaitingReview {
		t.Errorf("a high-risk story goes to review: %s (%s)", st.Integration, st.IntegrationNote)
	}
	if len(host.created) != 1 {
		t.Errorf("pull requests = %d", len(host.created))
	}
}

func TestFailedPublicationKeepsTheBranch(t *testing.T) {
	o, repo, host := newReviewTest(t, func(*workspace.SCMPolicy) {})
	host.failPush = errors.New("permission denied")
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run := waitDone(t, o)
	st := run.Stories[0]
	if st.Integration != IntegrationBlocked || !strings.Contains(st.IntegrationNote, "permission denied") {
		t.Fatalf("story = %s %q", st.Integration, st.IntegrationNote)
	}
	if exists(repo, "um.txt") || gitOut(t, repo, "branch", "--list", st.Branch) == "" {
		t.Error("the base must stay as it was and the branch must stay for review")
	}
}

func TestDependentWaitsForTheReview(t *testing.T) {
	o, _, _ := newReviewTest(t, func(*workspace.SCMPolicy) {})
	// Wave 1 (US-1, US-2) goes to review; US-3 depends on both.
	o.baseStages, o.stages = gatedStages, gatedStages
	policy := o.ws.Policy()
	policy.Sequential = false
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	_ = o.Start(StartOptions{Stories: threeStories, Source: "Markdown"})
	run := waitDone(t, o)
	if st := run.Stories[2]; st.Status != StepSkipped || !strings.Contains(st.IntegrationNote, "revisão") {
		t.Errorf("US-3 = %s %q", st.Status, st.IntegrationNote)
	}
}
