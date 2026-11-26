package scm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// GitHub serves github.com and GitHub Enterprise through the gh CLI, which
// holds the user's authentication: the orchestrator never sees a token.
type GitHub struct {
	Dir string
	Run Runner
}

// Host names the provider.
func (g *GitHub) Host() string { return HostGitHub }

// Target reads the repository and its default branch.
func (g *GitHub) Target(ctx context.Context) (Target, error) {
	out, err := g.Run(ctx, g.Dir, "gh", "repo", "view", "--json", "nameWithOwner,defaultBranchRef")
	if err != nil {
		return Target{}, fmt.Errorf("gh repo view: %w", err)
	}
	var v struct {
		NameWithOwner    string `json:"nameWithOwner"`
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return Target{}, fmt.Errorf("gh repo view: resposta ilegível: %w", err)
	}
	return Target{Host: HostGitHub, Repository: v.NameWithOwner, DefaultBranch: v.DefaultBranchRef.Name}, nil
}

// Publish pushes branch to origin.
func (g *GitHub) Publish(ctx context.Context, branch string) error {
	if err := checkBranch(branch); err != nil {
		return err
	}
	if _, err := g.Run(ctx, g.Dir, "git", "push", "-u", "origin", "refs/heads/"+branch+":refs/heads/"+branch); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	return nil
}

type ghPR struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	HeadRefName string `json:"headRefName"`
	BaseRefName string `json:"baseRefName"`
	HeadRefOid  string `json:"headRefOid"`
	State       string `json:"state"`
}

func (p ghPR) pr() *PullRequest {
	return &PullRequest{Number: p.Number, URL: p.URL, Head: p.HeadRefName, Base: p.BaseRefName, HeadSHA: p.HeadRefOid, State: p.State}
}

const prFields = "number,url,headRefName,baseRefName,headRefOid,state"

// Find returns the open pull request from head, or nil.
func (g *GitHub) Find(ctx context.Context, head string) (*PullRequest, error) {
	if err := checkBranch(head); err != nil {
		return nil, err
	}
	out, err := g.Run(ctx, g.Dir, "gh", "pr", "list", "--head", head, "--state", "open", "--json", prFields)
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var list []ghPR
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("gh pr list: resposta ilegível: %w", err)
	}
	if len(list) == 0 {
		return nil, nil
	}
	return list[0].pr(), nil
}

// Create opens a pull request; the body goes through a file, never through
// an argument a shell could read.
func (g *GitHub) Create(ctx context.Context, req Request) (*PullRequest, error) {
	for _, b := range []string{req.Head, req.Base} {
		if err := checkBranch(b); err != nil {
			return nil, err
		}
	}
	f, err := os.CreateTemp("", "faztudo-pr-*.md")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	_, werr := f.WriteString(req.Body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, werr
	}
	args := []string{"pr", "create", "--head", req.Head, "--base", req.Base, "--title", oneLine(req.Title), "--body-file", f.Name()}
	if req.Draft {
		args = append(args, "--draft")
	}
	if _, err := g.Run(ctx, g.Dir, "gh", args...); err != nil {
		return nil, fmt.Errorf("gh pr create: %w", err)
	}
	pr, err := g.Find(ctx, req.Head)
	if err == nil && pr == nil {
		err = fmt.Errorf("gh pr create: PR de %s não encontrado depois de criado", req.Head)
	}
	return pr, err
}

// Checks reads the host's checks on the pull request.
func (g *GitHub) Checks(ctx context.Context, number int) ([]Check, error) {
	out, err := g.Run(ctx, g.Dir, "gh", "pr", "checks", strconv.Itoa(number), "--json", "name,bucket")
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("gh pr checks: %w", err)
	}
	var list []struct {
		Name   string `json:"name"`
		Bucket string `json:"bucket"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("gh pr checks: resposta ilegível: %w", err)
	}
	checks := make([]Check, len(list))
	for i, c := range list {
		checks[i] = Check{Name: c.Name, State: c.Bucket}
	}
	return checks, nil
}

// Review reads the review decision and whether the host would merge it.
func (g *GitHub) Review(ctx context.Context, number int) (Review, error) {
	out, err := g.Run(ctx, g.Dir, "gh", "pr", "view", strconv.Itoa(number), "--json", "reviewDecision,mergeStateStatus,headRefOid")
	if err != nil {
		return Review{}, fmt.Errorf("gh pr view: %w", err)
	}
	var v struct {
		ReviewDecision   string `json:"reviewDecision"`
		MergeStateStatus string `json:"mergeStateStatus"`
		HeadRefOid       string `json:"headRefOid"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return Review{}, fmt.Errorf("gh pr view: resposta ilegível: %w", err)
	}
	return Review{Decision: v.ReviewDecision, MergeState: v.MergeStateStatus, HeadSHA: v.HeadRefOid}, nil
}

// checkBranch refuses a name that could pass for an option.
func checkBranch(b string) error {
	if b == "" || strings.HasPrefix(b, "-") || strings.ContainsAny(b, " \t\n\r~^:?*[\\") {
		return fmt.Errorf("branch inválida: %q", b)
	}
	return nil
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}
