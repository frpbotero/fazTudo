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

// Path is the directory of the nested repository n.
func (r *Repo) Path(n string) string { return filepath.Join(r.dir, n) }

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

// BranchPrefix starts the name of every branch FazTudo creates.
const BranchPrefix = "faztudo/"

// SaveInterrupted commits the pending changes of the dirty repositories
// (paths as returned by DirtyPaths) that sit on a FazTudo branch: they are
// work of an interrupted run. Repositories on the user's own branches are
// returned in left, untouched.
func (r *Repo) SaveInterrupted(ctx context.Context, dirty []string, msg string) (saved, left []string, err error) {
	parent := false
	for _, p := range dirty {
		if p == "." {
			parent = true
			continue
		}
		sub := r.sub(p)
		if !strings.HasPrefix(sub.Branch(ctx), BranchPrefix) {
			left = append(left, p)
			continue
		}
		if _, err := sub.commit(ctx, msg, false); err != nil {
			return saved, left, fmt.Errorf("%s: %w", p, err)
		}
		saved = append(saved, p)
	}
	if parent {
		if !strings.HasPrefix(r.Branch(ctx), BranchPrefix) {
			return saved, append(left, "."), nil
		}
		if _, err := r.commit(ctx, msg, false); err != nil {
			return saved, left, err
		}
		saved = append(saved, ".")
	}
	return saved, left, nil
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
// nested repositories too. The new branch then records the nested
// repositories' current commits, so the user's branch is never committed to.
func (r *Repo) CreateBranch(ctx context.Context, name string) error {
	for _, n := range r.nested {
		if _, err := r.sub(n).git(ctx, "switch", "-q", "-c", name); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
	}
	if _, err := r.git(ctx, "switch", "-q", "-c", name); err != nil {
		return err
	}
	_, err := r.SyncNested(ctx)
	return err
}

// RepoBranches lists a repository's branches; Path is "." for the directory
// itself or a nested repository's path.
type RepoBranches struct {
	Path     string   `json:"path"`
	Current  string   `json:"current"`
	Branches []string `json:"branches"`
}

// AllBranches lists the branches of the directory's repository and of each
// nested repository.
func (r *Repo) AllBranches(ctx context.Context) ([]RepoBranches, error) {
	var out []RepoBranches
	for _, p := range append([]string{"."}, r.nested...) {
		repo := r
		if p != "." {
			repo = r.sub(p)
		}
		branches, err := repo.Branches(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, RepoBranches{Path: p, Current: repo.Branch(ctx), Branches: branches})
	}
	return out, nil
}

// Branches lists the local branches and the remote ones git switch can
// create from, by name, leaving FazTudo's own branches out.
func (r *Repo) Branches(ctx context.Context) ([]string, error) {
	out, err := r.git(ctx, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, ref := range strings.Fields(out) {
		name, local := strings.CutPrefix(ref, "refs/heads/")
		if !local {
			// refs/remotes/<remote>/<branch>
			parts := strings.SplitN(strings.TrimPrefix(ref, "refs/remotes/"), "/", 2)
			if len(parts) < 2 || parts[1] == "HEAD" {
				continue
			}
			name = parts[1]
		}
		if !strings.HasPrefix(name, BranchPrefix) && !slices.Contains(branches, name) {
			branches = append(branches, name)
		}
	}
	slices.Sort(branches)
	return branches, nil
}

// SwitchBranches checks out each repository's base branch, keyed as in
// AllBranches; repositories without one stay where they are. A remote branch
// without a local copy is created tracking it.
func (r *Repo) SwitchBranches(ctx context.Context, bases map[string]string) error {
	if err := r.switchTo(ctx, bases["."]); err != nil {
		return err
	}
	// The parent's branch decides which nested repositories exist.
	r.nested = r.findNested(ctx)
	for _, n := range r.nested {
		if err := r.sub(n).switchTo(ctx, bases[n]); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
	}
	return nil
}

func (r *Repo) switchTo(ctx context.Context, branch string) error {
	if branch == "" || r.Branch(ctx) == branch {
		return nil
	}
	if _, err := r.git(ctx, "check-ref-format", "--branch", branch); err != nil || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("branch inválida: %q", branch)
	}
	_, err := r.git(ctx, "switch", "-q", branch)
	return err
}

// CurrentBranches returns the branch checked out in the repository ("." key)
// and in each nested repository, keyed as in AllBranches; detached HEADs are
// left out.
func (r *Repo) CurrentBranches(ctx context.Context) map[string]string {
	bases := map[string]string{}
	if b := r.Branch(ctx); b != "" {
		bases["."] = b
	}
	for _, n := range r.nested {
		if b := r.sub(n).Branch(ctx); b != "" {
			bases[n] = b
		}
	}
	return bases
}

// MergeIntoBases checks out each repository's base branch and merges rev
// (typically the integration branch, whose HEAD is the current one) into it,
// fast-forwarding when possible. On conflict the merge is aborted, the base
// is left as it was and the conflicted paths are returned.
func (r *Repo) MergeIntoBases(ctx context.Context, rev, msg string, bases map[string]string) ([]string, error) {
	if err := r.SwitchBranches(ctx, bases); err != nil {
		return nil, err
	}
	conflicts, err := r.StartMerge(ctx, rev, msg)
	if len(conflicts) > 0 {
		r.AbortMerge(ctx)
		return conflicts, fmt.Errorf("git merge: conflito em %s", strings.Join(conflicts, ", "))
	}
	return nil, err
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
	c, err := r.commit(ctx, msg, false, r.parentExcludes(exclude)...)
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

// parentExcludes drops the excludes inside nested repositories: git rejects
// a pathspec that reaches into one.
func (r *Repo) parentExcludes(exclude []string) []string {
	return slices.DeleteFunc(slices.Clone(exclude), func(e string) bool {
		return slices.ContainsFunc(r.nested, func(n string) bool { return strings.HasPrefix(e, n+"/") })
	})
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
	for _, e := range r.unignored(ctx, exclude) {
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

// unignored drops the paths git already ignores: naming an ignored path in a
// pathspec, even as an exclusion, makes git add fail.
func (r *Repo) unignored(ctx context.Context, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	// Exit status 1 means none is ignored.
	out, _ := r.git(ctx, append([]string{"check-ignore", "--"}, paths...)...)
	ignored := strings.Split(strings.TrimSpace(out), "\n")
	return slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return slices.Contains(ignored, p) })
}

// At returns a Repo for a directory already known to be inside a repository,
// such as a worktree created by AddWorktree.
func At(dir string) *Repo {
	r := &Repo{dir: dir}
	r.nested = r.findNested(context.Background())
	return r
}

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
	if _, err := r.git(ctx, "worktree", "add", "-q", "--detach", path, "HEAD"); err != nil {
		return err
	}
	return r.addNestedWorktrees(ctx, path, func(n *Repo, dir string) error {
		_, err := n.git(ctx, "worktree", "add", "-q", "--detach", dir, "HEAD")
		return err
	})
}

// addNestedWorktrees gives each nested repository its own worktree at its
// place inside the worktree at path, created by add. On failure the whole
// worktree is removed.
func (r *Repo) addNestedWorktrees(ctx context.Context, path string, add func(n *Repo, dir string) error) error {
	if len(r.nested) == 0 {
		return nil
	}
	prefix, err := r.Prefix(ctx)
	if err == nil {
		for _, n := range r.nested {
			if err = add(r.sub(n), filepath.Join(path, prefix, n)); err != nil {
				err = fmt.Errorf("%s: %w", n, err)
				break
			}
		}
	}
	if err != nil {
		_ = r.RemoveWorktree(context.WithoutCancel(ctx), path)
	}
	return err
}

// RemoveWorktree deletes a worktree created by AddWorktree, with the worktrees
// of its nested repositories.
func (r *Repo) RemoveWorktree(ctx context.Context, path string) error {
	if prefix, err := r.Prefix(ctx); err == nil {
		for _, n := range r.nested {
			_, _ = r.sub(n).git(ctx, "worktree", "remove", "--force", filepath.Join(path, prefix, n))
		}
	}
	_, err := r.git(ctx, "worktree", "remove", "--force", path)
	return err
}

// FastForward moves the current branch to hash, which must descend from HEAD;
// nested repositories move to the commits hash records for them.
func (r *Repo) FastForward(ctx context.Context, hash string) error {
	for _, n := range r.nested {
		if target := r.gitlinkAt(ctx, hash, n); target != "" {
			if _, err := r.sub(n).git(ctx, "merge", "-q", "--ff-only", target); err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
		}
	}
	_, err := r.git(ctx, "merge", "-q", "--ff-only", hash)
	return err
}

// gitlinkAt is the commit rev records for the nested repository n, or "".
func (r *Repo) gitlinkAt(ctx context.Context, rev, n string) string {
	out, err := r.git(ctx, "rev-parse", "-q", "--verify", rev+":./"+n)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Merge merges hash into the current branch, fast-forwarding when possible.
// A conflicting merge is aborted, leaving the branch as it was.
func (r *Repo) Merge(ctx context.Context, hash, msg string) error {
	conflicts, err := r.StartMerge(ctx, hash, msg)
	if len(conflicts) > 0 {
		r.AbortMerge(ctx)
		return fmt.Errorf("git merge: conflito em %s", strings.Join(conflicts, ", "))
	}
	return err
}

// StartMerge merges rev into the current branch. On conflict the merge is
// left in progress and the conflicted paths (relative to the repository
// root) are returned; any other failure is aborted. Nested repositories merge
// the commits rev records for them first.
func (r *Repo) StartMerge(ctx context.Context, rev, msg string) ([]string, error) {
	if len(r.nested) > 0 {
		return r.startNestedMerge(ctx, rev, msg)
	}
	conflicts, err := r.startMerge(ctx, rev, msg)
	switch {
	case len(conflicts) > 0:
		return conflicts, nil
	case err != nil:
		r.AbortMerge(ctx)
	}
	return nil, err
}

func (r *Repo) startMerge(ctx context.Context, rev, msg string, flags ...string) ([]string, error) {
	args := append([]string{"merge", "-q", "--no-edit", "-m", msg}, flags...)
	_, err := r.git(ctx, r.withIdentity(ctx, append(args, rev)...)...)
	if err == nil {
		return nil, nil
	}
	return r.conflicts(ctx), err
}

func (r *Repo) conflicts(ctx context.Context) []string {
	out, _ := r.git(ctx, "diff", "--name-only", "--diff-filter=U")
	return strings.Fields(out)
}

// startNestedMerge merges each nested repository, then the parent without
// committing, so the parent records the nested repositories' merged commits.
func (r *Repo) startNestedMerge(ctx context.Context, rev, msg string) ([]string, error) {
	prefix, err := r.Prefix(ctx)
	if err != nil {
		return nil, err
	}
	var conflicts []string
	for _, n := range r.nested {
		target := r.gitlinkAt(ctx, rev, n)
		if target == "" {
			continue
		}
		c, err := r.sub(n).startMerge(ctx, target, msg)
		if len(c) == 0 && err != nil {
			r.AbortMerge(ctx)
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		for _, f := range c {
			conflicts = append(conflicts, prefix+n+"/"+f)
		}
	}
	_, err = r.startMerge(ctx, rev, msg, "--no-ff", "--no-commit")
	// Conflicts on the nested repositories' pointers resolve to their merged commits.
	if _, addErr := r.git(ctx, append([]string{"add", "--"}, r.nested...)...); addErr != nil {
		r.AbortMerge(ctx)
		return nil, addErr
	}
	parent := r.conflicts(ctx)
	if err != nil && len(parent) == 0 && !r.merging(ctx) {
		r.AbortMerge(ctx)
		return nil, err
	}
	if conflicts = append(conflicts, parent...); len(conflicts) > 0 {
		return conflicts, nil
	}
	if _, err := r.FinishMerge(ctx); err != nil {
		r.AbortMerge(ctx)
		return nil, err
	}
	return nil, nil
}

// merging reports whether a merge is in progress.
func (r *Repo) merging(ctx context.Context) bool {
	_, err := r.git(ctx, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	return err == nil
}

// AbortMerge cancels a merge in progress. Nested repositories go back to the
// commits the parent's HEAD records, undoing the merges they concluded.
func (r *Repo) AbortMerge(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	for _, n := range r.nested {
		sub := r.sub(n)
		_, _ = sub.git(ctx, "merge", "--abort")
		if want := r.gitlinkAt(ctx, "HEAD", n); want != "" {
			if head, _ := sub.Head(ctx); head != want {
				_, _ = sub.git(ctx, "reset", "-q", "--hard", want)
			}
		}
	}
	_, _ = r.git(ctx, "merge", "--abort")
}

// FinishMerge stages the resolved files and concludes the merge in progress,
// in the nested repositories first.
func (r *Repo) FinishMerge(ctx context.Context) (*Commit, error) {
	for _, n := range r.nested {
		if sub := r.sub(n); sub.merging(ctx) {
			if _, err := sub.FinishMerge(ctx); err != nil {
				return nil, fmt.Errorf("%s: %w", n, err)
			}
		}
	}
	if _, err := r.git(ctx, "add", "-A", "--", "."); err != nil {
		return nil, err
	}
	if !r.merging(ctx) {
		// Only the nested repositories had something to merge.
		c, err := r.commitPaths(ctx, "FazTudo: sincroniza repositórios aninhados", r.nested)
		if c == nil && err == nil {
			c, err = r.headCommit(ctx)
		}
		return c, err
	}
	if _, err := r.git(ctx, r.withIdentity(ctx, "commit", "-q", "--no-verify", "--no-edit")...); err != nil {
		return nil, err
	}
	return r.headCommit(ctx)
}

func (r *Repo) headCommit(ctx context.Context) (*Commit, error) {
	hash, err := r.git(ctx, "rev-parse", "--short", "HEAD")
	return &Commit{Hash: strings.TrimSpace(hash)}, err
}

// withIdentity prefixes args with FazTudo's identity when git has none.
func (r *Repo) withIdentity(ctx context.Context, args ...string) []string {
	if out, _ := r.git(ctx, "config", "user.email"); strings.TrimSpace(out) == "" {
		return append([]string{"-c", "user.name=" + commitName, "-c", "user.email=" + commitEmail}, args...)
	}
	return args
}

// AddBranchWorktree creates branch from HEAD and checks it out in a new
// worktree at path, in the nested repositories too.
func (r *Repo) AddBranchWorktree(ctx context.Context, path, branch string) error {
	if _, err := r.git(ctx, "worktree", "add", "-q", "-b", branch, path, "HEAD"); err != nil {
		return err
	}
	return r.addNestedWorktrees(ctx, path, func(n *Repo, dir string) error {
		_, err := n.git(ctx, "worktree", "add", "-q", "-b", branch, dir, "HEAD")
		return err
	})
}

// AddExistingWorktree checks out an existing branch in a new worktree at path.
// A nested repository without the branch gets it from its HEAD.
func (r *Repo) AddExistingWorktree(ctx context.Context, path, branch string) error {
	if _, err := r.git(ctx, "worktree", "add", "-q", path, branch); err != nil {
		return err
	}
	return r.addNestedWorktrees(ctx, path, func(n *Repo, dir string) error {
		args := []string{"worktree", "add", "-q", dir, branch}
		if _, err := n.git(ctx, "rev-parse", "-q", "--verify", "refs/heads/"+branch); err != nil {
			args = []string{"worktree", "add", "-q", "-b", branch, dir, "HEAD"}
		}
		_, err := n.git(ctx, args...)
		return err
	})
}

// Diff returns the patch between two commits, capped in size.
func (r *Repo) Diff(ctx context.Context, from, to string, max int) (string, error) {
	out, err := r.git(ctx, "diff", "--no-color", "--no-ext-diff", "--submodule=diff", from, to, "--")
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
	return fmt.Sprintf("%s%s-%s", BranchPrefix, slug, at.Format("20060102-150405"))
}
