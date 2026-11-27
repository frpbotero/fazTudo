package scm

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// fakeHost answers gh and git like a host would and records every call.
type fakeHost struct {
	calls  [][]string
	body   string
	prs    string // gh pr list output
	remote string
}

func (f *fakeHost) run(_ context.Context, _ string, name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	switch {
	case name == "git" && args[0] == "remote":
		if f.remote == "" {
			return "", errors.New("no remote")
		}
		return f.remote + "\n", nil
	case name == "gh" && args[0] == "repo":
		return `{"nameWithOwner":"acme/loja","defaultBranchRef":{"name":"main"}}`, nil
	case name == "gh" && args[0] == "pr" && args[1] == "list":
		return cmpOr(f.prs, "[]"), nil
	case name == "gh" && args[0] == "pr" && args[1] == "create":
		i := slices.Index(args, "--body-file")
		data, _ := os.ReadFile(args[i+1])
		f.body = string(data)
		f.prs = `[{"number":7,"url":"https://github.com/acme/loja/pull/7","headRefName":"faztudo/us-1","baseRefName":"main","headRefOid":"abc","state":"OPEN"}]`
		return "https://github.com/acme/loja/pull/7\n", nil
	case name == "gh" && args[0] == "pr" && args[1] == "checks":
		return `[{"name":"verify (ubuntu)","bucket":"pass"},{"name":"verify (macos)","bucket":"pending"}]`, errors.New("exit 8")
	case name == "gh" && args[0] == "pr" && args[1] == "view":
		return `{"reviewDecision":"REVIEW_REQUIRED","mergeStateStatus":"BLOCKED","headRefOid":"abc"}`, nil
	}
	return "", nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func TestGitHubPreparesAPullRequestWithoutAShell(t *testing.T) {
	h := &fakeHost{}
	g := &GitHub{Dir: "/repo", Run: h.run}
	ctx := context.Background()
	target, err := g.Target(ctx)
	if err != nil || target.Repository != "acme/loja" || target.DefaultBranch != "main" {
		t.Fatalf("target = %+v, %v", target, err)
	}
	if pr, err := g.Find(ctx, "faztudo/us-1"); err != nil || pr != nil {
		t.Fatalf("find before create = %+v, %v", pr, err)
	}
	if err := g.Publish(ctx, "faztudo/us-1"); err != nil {
		t.Fatal(err)
	}
	body := "## Evidência\n$(rm -rf /) `x` ; echo oi"
	pr, err := g.Create(ctx, Request{Head: "faztudo/us-1", Base: "main", Title: "feat: US-1\nlinha", Body: body, Draft: true})
	if err != nil || pr.Number != 7 || pr.HeadSHA != "abc" {
		t.Fatalf("create = %+v, %v", pr, err)
	}
	if h.body != body {
		t.Errorf("the body must reach gh as a file, unchanged: %q", h.body)
	}
	for _, c := range h.calls {
		if slices.Contains(c, body) || slices.Contains(c, "sh") || slices.Contains(c, "-c") {
			t.Errorf("a call passes the body or a shell: %v", c)
		}
	}
	create := h.calls[len(h.calls)-2]
	if !slices.Contains(create, "--draft") || !slices.Contains(create, "feat: US-1 linha") {
		t.Errorf("create args = %v", create)
	}
	checks, err := g.Checks(ctx, 7)
	if err != nil || len(checks) != 2 || checks[1].State != "pending" {
		t.Errorf("checks (gh exits non-zero while pending) = %+v, %v", checks, err)
	}
	review, err := g.Review(ctx, 7)
	if err != nil || review.Decision != "REVIEW_REQUIRED" || review.MergeState != "BLOCKED" {
		t.Errorf("review = %+v, %v", review, err)
	}
}

func TestBranchNamesCannotPassForOptions(t *testing.T) {
	g := &GitHub{Run: (&fakeHost{}).run}
	ctx := context.Background()
	for _, b := range []string{"", "--force", "a b", "x:y"} {
		if err := g.Publish(ctx, b); err == nil {
			t.Errorf("branch %q accepted", b)
		}
		if _, err := g.Create(ctx, Request{Head: b, Base: "main"}); err == nil {
			t.Errorf("head %q accepted", b)
		}
	}
}

func TestDetectPicksTheHost(t *testing.T) {
	ctx := context.Background()
	cases := map[string]error{
		"git@github.com:acme/loja.git":          nil,
		"https://bitbucket.org/acme/loja.git":   ErrBitbucketPending,
		"https://git.example.com/acme/loja.git": ErrUnsupportedHost,
	}
	for remote, want := range cases {
		p, err := Detect(ctx, "/repo", (&fakeHost{remote: remote}).run)
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", remote, err, want)
		}
		if want == nil && (p == nil || p.Host() != HostGitHub) {
			t.Errorf("%s: provider = %v", remote, p)
		}
	}
	if _, err := Detect(ctx, "/repo", (&fakeHost{}).run); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Errorf("no remote = %v", err)
	}
}

func TestNoChecksIsAnEmptyList(t *testing.T) {
	g := &GitHub{Dir: "/repo", Run: func(context.Context, string, string, ...string) (string, error) {
		return "", errors.New("no checks reported on the 'faztudo/us-4' branch")
	}}
	if checks, err := g.Checks(context.Background(), 1); err != nil || len(checks) != 0 {
		t.Errorf("a repository without CI has no checks, not an error: %+v, %v", checks, err)
	}
	g.Run = func(context.Context, string, string, ...string) (string, error) { return "", errors.New("HTTP 401") }
	if _, err := g.Checks(context.Background(), 1); err == nil {
		t.Error("a real failure must still be an error")
	}
}

func TestReviewReadsTheState(t *testing.T) {
	g := &GitHub{Dir: "/repo", Run: func(context.Context, string, string, ...string) (string, error) {
		return `{"reviewDecision":"","mergeStateStatus":"UNKNOWN","headRefOid":"abc","state":"MERGED","mergeCommit":{"oid":"m1"}}`, nil
	}}
	r, err := g.Review(context.Background(), 1)
	if err != nil || r.State != "MERGED" || r.MergeCommit != "m1" {
		t.Errorf("review = %+v, %v", r, err)
	}
}
