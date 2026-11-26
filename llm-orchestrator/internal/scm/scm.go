// Package scm prepares external review on the project's source host: it
// reads the target repository, publishes a branch, opens a pull request with
// the evidence and reads its checks and review. It never merges: the merge
// stays with the reviewers and the host's own rules.
package scm

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Target is the repository a pull request goes to.
type Target struct {
	Host          string `json:"host"` // HostGitHub, HostBitbucket
	Repository    string `json:"repository"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

// Hosts.
const (
	HostGitHub    = "github"
	HostBitbucket = "bitbucket"
)

// PullRequest is a pull request as the host reports it.
type PullRequest struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	Head    string `json:"head"`
	Base    string `json:"base"`
	HeadSHA string `json:"head_sha,omitempty"`
	State   string `json:"state,omitempty"`
}

// Request is a pull request to open.
type Request struct {
	Head, Base, Title, Body string
	Draft                   bool
}

// Check is one of the host's checks on a pull request.
type Check struct {
	Name  string `json:"name"`
	State string `json:"state"` // as the host says it: pass, fail, pending…
}

// Review is the reviewers' and the host's verdict on a pull request.
type Review struct {
	Decision   string `json:"decision,omitempty"`    // e.g. APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED
	MergeState string `json:"merge_state,omitempty"` // e.g. CLEAN, BLOCKED, BEHIND
	HeadSHA    string `json:"head_sha,omitempty"`
}

// Provider is a source host.
type Provider interface {
	Host() string
	Target(ctx context.Context) (Target, error)
	Publish(ctx context.Context, branch string) error
	Find(ctx context.Context, head string) (*PullRequest, error)
	Create(ctx context.Context, req Request) (*PullRequest, error)
	Checks(ctx context.Context, number int) ([]Check, error)
	Review(ctx context.Context, number int) (Review, error)
}

// ErrUnsupportedHost is returned for a remote no provider serves yet.
var ErrUnsupportedHost = errors.New("provedor de SCM não suportado para este remoto")

// ErrBitbucketPending marks the Bitbucket provider as the tracked follow-up
// of FT-020: its Cloud and Server APIs differ and the real environment
// decides which one comes first.
var ErrBitbucketPending = errors.New("Bitbucket ainda não implementado (subentrega FT-020b); use o modo local")

// Runner runs a command in dir and returns its standard output.
type Runner func(ctx context.Context, dir, name string, args ...string) (string, error)

// ExecRunner runs real commands, with no shell: arguments are passed as
// they are, so no text becomes a command.
func ExecRunner(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return string(out), errors.New(strings.TrimSpace(string(exitErr.Stderr)))
		}
	}
	return string(out), err
}

// Detect picks the provider for the repository's origin remote.
func Detect(ctx context.Context, dir string, run Runner) (Provider, error) {
	url, err := run(ctx, dir, "git", "remote", "get-url", "origin")
	if err != nil {
		return nil, errors.New("o repositório não tem remoto origin")
	}
	switch host := hostOf(strings.TrimSpace(url)); host {
	case HostGitHub:
		return &GitHub{Dir: dir, Run: run}, nil
	case HostBitbucket:
		return nil, ErrBitbucketPending
	}
	return nil, ErrUnsupportedHost
}

func hostOf(url string) string {
	u := strings.ToLower(url)
	switch {
	case strings.Contains(u, "github.com") || strings.Contains(u, "github."):
		return HostGitHub
	case strings.Contains(u, "bitbucket"):
		return HostBitbucket
	}
	return ""
}
