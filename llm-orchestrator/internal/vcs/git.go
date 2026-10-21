// Package vcs records the pipeline's work in git: one branch per user story
// and one commit per agent step, so every change can be reviewed or undone.
package vcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrDirty is returned when tracked files have uncommitted changes, which
// would otherwise be mixed into the agents' commits.
var ErrDirty = errors.New("o repositório tem alterações não commitadas em arquivos versionados; faça commit ou stash antes de executar")

// ErrNoGit is returned when the git binary is not installed.
var ErrNoGit = errors.New("git não encontrado no PATH")

// defaultExcludes keep dependencies and generated output out of commits in
// repositories FazTudo initialises itself (which have no .gitignore yet).
var defaultExcludes = []string{
	"node_modules/", "vendor/", "dist/", "build/", "coverage/", ".venv/", "venv/", "__pycache__/",
	"test-results/", "playwright-report/", ".DS_Store", "*.log",
}

const (
	commitName  = "FazTudo"
	commitEmail = "faztudo@localhost"
	maxPatch    = 400 << 10
)

// Repo runs git inside a directory. The directory may be a subfolder of a
// larger repository; commits are then limited to it.
type Repo struct {
	dir string
}

// FileChange is one file touched by a commit.
type FileChange struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary,omitempty"`
}

// Commit summarises a commit made for a step.
type Commit struct {
	Hash  string       `json:"hash"`
	Files []FileChange `json:"files"`
}

// Open returns the repository containing dir. When dir is not inside a git
// repository it is initialised with an initial commit and init is true.
func Open(ctx context.Context, dir string) (r *Repo, initialised bool, err error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, false, ErrNoGit
	}
	r = &Repo{dir: dir}
	if _, err := r.git(ctx, "rev-parse", "--git-dir"); err == nil {
		return r, false, nil
	}
	if _, err := r.git(ctx, "init", "-q"); err != nil {
		return nil, false, err
	}
	exclude := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err == nil {
		f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "\n# Adicionado pelo FazTudo\n%s\n", strings.Join(defaultExcludes, "\n"))
			f.Close()
		}
	}
	if _, err := r.commit(ctx, "FazTudo: estado inicial", true); err != nil {
		return nil, false, err
	}
	return r, true, nil
}

// ErrNotRepo is returned by Find outside a git repository.
var ErrNotRepo = errors.New("o repositório não está versionado com git")

// Find returns the existing repository containing dir, without creating one.
func Find(ctx context.Context, dir string) (*Repo, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, ErrNoGit
	}
	r := &Repo{dir: dir}
	if _, err := r.git(ctx, "rev-parse", "--git-dir"); err != nil {
		return nil, ErrNotRepo
	}
	return r, nil
}

// Dirty reports whether tracked files under the directory have changes.
func (r *Repo) Dirty(ctx context.Context) (bool, error) {
	out, err := r.git(ctx, "status", "--porcelain", "--untracked-files=no", "--", ".")
	return strings.TrimSpace(out) != "", err
}

// Branch returns the current branch name ("" when detached).
func (r *Repo) Branch(ctx context.Context) string {
	out, err := r.git(ctx, "branch", "--show-current")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// CreateBranch creates and switches to a new branch from HEAD.
func (r *Repo) CreateBranch(ctx context.Context, name string) error {
	_, err := r.git(ctx, "switch", "-q", "-c", name)
	return err
}

// CommitAll stages every change under the directory and commits it. It
// returns nil when there was nothing to commit.
func (r *Repo) CommitAll(ctx context.Context, msg string) (*Commit, error) {
	return r.commit(ctx, msg, false)
}

func (r *Repo) commit(ctx context.Context, msg string, allowEmpty bool) (*Commit, error) {
	if _, err := r.git(ctx, "add", "-A", "--", "."); err != nil {
		return nil, err
	}
	if !allowEmpty {
		if _, err := r.git(ctx, "diff", "--cached", "--quiet", "--", "."); err == nil {
			return nil, nil
		}
	}
	args := []string{"commit", "-q", "--no-verify", "-m", msg}
	if allowEmpty {
		args = append(args, "--allow-empty")
	} else {
		args = append(args, "--", ".") // never sweep in staged files outside the directory
	}
	if out, _ := r.git(ctx, "config", "user.email"); strings.TrimSpace(out) == "" {
		args = append([]string{"-c", "user.name=" + commitName, "-c", "user.email=" + commitEmail}, args...)
	}
	if _, err := r.git(ctx, args...); err != nil {
		return nil, err
	}
	hash, err := r.git(ctx, "rev-parse", "--short", "HEAD")
	if err != nil {
		return nil, err
	}
	c := &Commit{Hash: strings.TrimSpace(hash)}
	if stat, err := r.git(ctx, "show", "--numstat", "--format=", "HEAD"); err == nil {
		c.Files = parseNumstat(stat)
	}
	return c, nil
}

var reHash = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// Show returns the commit message, stats and patch, capped in size.
func (r *Repo) Show(ctx context.Context, hash string) (string, error) {
	if !reHash.MatchString(hash) {
		return "", fmt.Errorf("hash de commit inválido: %q", hash)
	}
	out, err := r.git(ctx, "show", "--stat", "--patch", "--no-color", "--no-ext-diff", hash, "--")
	if err != nil {
		return "", err
	}
	if len(out) > maxPatch {
		out = out[:maxPatch] + "\n[… diff truncado]"
	}
	return out, nil
}

func parseNumstat(out string) []FileChange {
	var files []FileChange
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		fc := FileChange{Path: parts[2]}
		if parts[0] == "-" {
			fc.Binary = true
		} else {
			fc.Added, _ = strconv.Atoi(parts[0])
			fc.Deleted, _ = strconv.Atoi(parts[1])
		}
		files = append(files, fc)
	}
	return files
}

func (r *Repo) git(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("git %s: %s", args[0], msg)
	}
	return stdout.String(), nil
}

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

// BranchName builds a branch name for a story, unique per run.
func BranchName(storyID string, at time.Time) string {
	slug := strings.Trim(reSlug.ReplaceAllString(strings.ToLower(storyID), "-"), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug == "" {
		slug = "us"
	}
	return fmt.Sprintf("faztudo/%s-%s", slug, at.Format("20060102-150405"))
}
