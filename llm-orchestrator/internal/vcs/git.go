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
	"slices"
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
	// Dependency folders have no trailing slash so symlinks to them match too.
	"node_modules", "vendor/", "dist/", "build/", "coverage/", ".venv", "venv", "__pycache__/",
	"test-results/", "playwright-report/", ".DS_Store", "*.log",
}

const (
	commitName  = "FazTudo"
	commitEmail = "faztudo@localhost"
	maxPatch    = 400 << 10
)

// Repo runs git inside a directory. The directory may be a subfolder of a
// larger repository; commits are then limited to it. Repositories nested in
// it (gitlinks, e.g. a front and an api cloned inside a parent repository)
// are branched and committed together with it.
type Repo struct {
	dir    string
	nested []string // relative paths of nested repositories
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
		r.nested = r.findNested(ctx)
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
	r.nested = r.findNested(ctx)
	return r, nil
}

// findNested lists the gitlinks under the directory whose repository is
// checked out (submodules or repositories added without .gitmodules).
func (r *Repo) findNested(ctx context.Context) []string {
	out, err := r.git(ctx, "ls-files", "--stage", "--", ".")
	if err != nil {
		return nil
	}
	var nested []string
	for _, line := range strings.Split(out, "\n") {
		meta, path, ok := strings.Cut(line, "\t")
		if !ok || !strings.HasPrefix(meta, "160000 ") {
			continue
		}
		if _, err := os.Stat(filepath.Join(r.dir, path, ".git")); err == nil {
			nested = append(nested, path)
		}
	}
	return nested
}

// Nested returns the relative paths of the nested repositories.
func (r *Repo) Nested() []string { return r.nested }

func (r *Repo) sub(path string) *Repo { return &Repo{dir: filepath.Join(r.dir, path)} }

// Dirty reports whether tracked files under the directory have changes.
func (r *Repo) Dirty(ctx context.Context) (bool, error) {
	paths, err := r.DirtyPaths(ctx)
	return len(paths) > 0, err
}

// DirtyPaths lists the repositories ("." for the directory itself, or a
// nested repository's path) whose tracked files have changes. A nested
// repository that only moved to another commit is not a change: SyncNested
// records it.
func (r *Repo) DirtyPaths(ctx context.Context) ([]string, error) {
	var dirty []string
	out, err := r.git(ctx, "status", "--porcelain", "--untracked-files=no", "--ignore-submodules=all", "--", ".")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) != "" {
		dirty = append(dirty, ".")
	}
	for _, n := range r.nested {
		out, err := r.sub(n).git(ctx, "status", "--porcelain", "--untracked-files=no")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if strings.TrimSpace(out) != "" {
			dirty = append(dirty, n)
		}
	}
	return dirty, nil
}

// SyncNested commits nested repositories that moved to another commit
// (e.g. work committed inside them by hand), so the pipeline's own commits
// only hold the agents' changes. It returns nil when nothing moved.
func (r *Repo) SyncNested(ctx context.Context) (*Commit, error) {
	if len(r.nested) == 0 {
		return nil, nil
	}
	return r.commitPaths(ctx, "FazTudo: sincroniza repositórios aninhados", r.nested)
}

// Branch returns the current branch name ("" when detached).
func (r *Repo) Branch(ctx context.Context) string {
	out, err := r.git(ctx, "branch", "--show-current")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// CreateBranch creates and switches to a new branch from HEAD, in the
// nested repositories too.
func (r *Repo) CreateBranch(ctx context.Context, name string) error {
	for _, n := range r.nested {
		if _, err := r.sub(n).git(ctx, "switch", "-q", "-c", name); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
	}
	_, err := r.git(ctx, "switch", "-q", "-c", name)
	return err
}

// CommitAll stages every change under the directory and commits it. It
// returns nil when there was nothing to commit. Paths in exclude (relative
// to the directory) are left out. Nested repositories commit their own
// changes first; the parent commit then records their new commits, and the
// returned files list both, nested paths prefixed.
func (r *Repo) CommitAll(ctx context.Context, msg string, exclude ...string) (*Commit, error) {
	var nestedFiles []FileChange
	for _, n := range r.nested {
		c, err := r.sub(n).commit(ctx, msg, false, nestedExcludes(n, exclude)...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if c != nil {
			for _, f := range c.Files {
				f.Path = n + "/" + f.Path
				nestedFiles = append(nestedFiles, f)
			}
		}
	}
	c, err := r.commit(ctx, msg, false, exclude...)
	if err != nil || c == nil {
		return c, err
	}
	if len(nestedFiles) > 0 {
		c.Files = append(slices.DeleteFunc(c.Files, func(f FileChange) bool { return slices.Contains(r.nested, f.Path) }), nestedFiles...)
	}
	return c, nil
}

// nestedExcludes keeps the excludes that fall inside nested, relative to it.
func nestedExcludes(nested string, exclude []string) []string {
	var out []string
	for _, e := range exclude {
		if rest, ok := strings.CutPrefix(e, nested+"/"); ok {
			out = append(out, rest)
		}
	}
	return out
}

// commitPaths commits only the given paths, when they changed.
func (r *Repo) commitPaths(ctx context.Context, msg string, paths []string) (*Commit, error) {
	spec := append([]string{"--"}, paths...)
	if _, err := r.git(ctx, append([]string{"add"}, spec...)...); err != nil {
		return nil, err
	}
	if _, err := r.git(ctx, append([]string{"diff", "--cached", "--quiet"}, spec...)...); err == nil {
		return nil, nil
	}
	args := append([]string{"commit", "-q", "--no-verify", "-m", msg}, spec...)
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
	return &Commit{Hash: strings.TrimSpace(hash)}, nil
}

func (r *Repo) commit(ctx context.Context, msg string, allowEmpty bool, exclude ...string) (*Commit, error) {
	spec := []string{"--", "."}
	for _, e := range exclude {
		spec = append(spec, ":(exclude)"+e)
	}
	if _, err := r.git(ctx, append([]string{"add", "-A"}, spec...)...); err != nil {
		return nil, err
	}
	if !allowEmpty {
		if _, err := r.git(ctx, append([]string{"diff", "--cached", "--quiet"}, spec...)...); err == nil {
			return nil, nil
		}
	}
	args := []string{"commit", "-q", "--no-verify", "-m", msg}
	if allowEmpty {
		args = append(args, "--allow-empty")
	} else {
		args = append(args, spec...) // never sweep in staged files outside the directory
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

// At returns a Repo for a directory already known to be inside a repository,
// such as a worktree created by AddWorktree.
func At(dir string) *Repo { return &Repo{dir: dir} }

// Head returns the full hash of HEAD.
func (r *Repo) Head(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Prefix returns the directory's path relative to the repository root, with
// a trailing slash, or "" at the root.
func (r *Repo) Prefix(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "rev-parse", "--show-prefix")
	return strings.TrimSpace(out), err
}

// AddWorktree checks out HEAD, detached, in a new worktree at path.
func (r *Repo) AddWorktree(ctx context.Context, path string) error {
	_, err := r.git(ctx, "worktree", "add", "-q", "--detach", path, "HEAD")
	return err
}

// RemoveWorktree deletes a worktree created by AddWorktree.
func (r *Repo) RemoveWorktree(ctx context.Context, path string) error {
	_, err := r.git(ctx, "worktree", "remove", "--force", path)
	return err
}

// FastForward moves the current branch to hash, which must descend from HEAD.
func (r *Repo) FastForward(ctx context.Context, hash string) error {
	_, err := r.git(ctx, "merge", "-q", "--ff-only", hash)
	return err
}

// Diff returns the patch between two commits, capped in size.
func (r *Repo) Diff(ctx context.Context, from, to string, max int) (string, error) {
	out, err := r.git(ctx, "diff", "--no-color", "--no-ext-diff", from, to, "--")
	if err != nil {
		return "", err
	}
	if len(out) > max {
		out = strings.ToValidUTF8(out[:max], "") + "\n[… diff truncado]"
	}
	return out, nil
}

var reHash = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// Show returns the commit message, stats and patch, capped in size.
func (r *Repo) Show(ctx context.Context, hash string) (string, error) {
	if !reHash.MatchString(hash) {
		return "", fmt.Errorf("hash de commit inválido: %q", hash)
	}
	out, err := r.git(ctx, "show", "--stat", "--patch", "--no-color", "--no-ext-diff", "--submodule=diff", hash, "--")
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
