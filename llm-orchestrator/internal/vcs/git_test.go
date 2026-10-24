package vcs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// Isolate from the developer's global git configuration.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenInitialisesAndCommitsPerStep(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	write(t, dir, "index.html", "<h1>oi</h1>\n")
	write(t, dir, "node_modules/x/index.js", "ignored")

	if _, err := Find(ctx, dir); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("Find before init: %v", err)
	}
	r, initialised, err := Open(ctx, dir)
	if err != nil || !initialised {
		t.Fatalf("Open: initialised=%v err=%v", initialised, err)
	}
	if dirty, _ := r.Dirty(ctx); dirty {
		t.Fatal("fresh repository must be clean")
	}
	tracked, _ := r.git(ctx, "ls-files")
	if strings.Contains(tracked, "node_modules") || !strings.Contains(tracked, "index.html") {
		t.Fatalf("initial commit tracked:\n%s", tracked)
	}

	branch := BranchName("US-12: Carrinho de compras!", time.Date(2026, 10, 21, 15, 4, 5, 0, time.UTC))
	if branch != "faztudo/us-12-carrinho-de-compras-20261021-150405" {
		t.Fatalf("branch = %q", branch)
	}
	if err := r.CreateBranch(ctx, branch); err != nil {
		t.Fatal(err)
	}
	if got := r.Branch(ctx); got != branch {
		t.Fatalf("current branch = %q", got)
	}

	if c, err := r.CommitAll(ctx, "nada"); err != nil || c != nil {
		t.Fatalf("empty commit: %+v %v", c, err)
	}
	write(t, dir, "index.html", "<h1>olá</h1>\n<p>novo</p>\n")
	write(t, dir, "app.js", "console.log(1)\n")
	c, err := r.CommitAll(ctx, "faztudo(US-12): Frontend")
	if err != nil || c == nil {
		t.Fatalf("commit: %+v %v", c, err)
	}
	if len(c.Files) != 2 || c.Hash == "" {
		t.Fatalf("commit = %+v", c)
	}

	patch, err := r.Show(ctx, c.Hash)
	if err != nil || !strings.Contains(patch, "+<p>novo</p>") || !strings.Contains(patch, "faztudo(US-12): Frontend") {
		t.Fatalf("show: %v\n%s", err, patch)
	}
	if _, err := r.Show(ctx, "HEAD; rm -rf /"); err == nil {
		t.Error("Show must reject anything but a hash")
	}

	write(t, dir, "index.html", "pendente")
	if dirty, _ := r.Dirty(ctx); !dirty {
		t.Error("modified tracked file must make the repository dirty")
	}
	if _, init2, err := Open(ctx, dir); err != nil || init2 {
		t.Errorf("reopening must not re-initialise: %v %v", init2, err)
	}
}

func TestNestedRepositoriesAreBranchedAndCommitted(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Parent repository with front/ as a gitlink (added without .gitmodules).
	front := filepath.Join(root, "front")
	write(t, front, "app.js", "v1\n")
	run(front, "init", "-q")
	run(front, "add", "-A")
	run(front, "commit", "-q", "-m", "front v1")
	write(t, root, "README.md", "pai\n")
	run(root, "init", "-q")
	run(root, "add", "-A")
	run(root, "commit", "-q", "-m", "pai")

	// Work committed by hand inside front moves its HEAD: not a pending change.
	write(t, front, "app.js", "v2\n")
	run(front, "commit", "-q", "-am", "front v2")

	r, _, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Nested(); len(got) != 1 || got[0] != "front" {
		t.Fatalf("nested = %v", got)
	}
	if dirty, err := r.DirtyPaths(ctx); err != nil || len(dirty) != 0 {
		t.Fatalf("moved gitlink must not count as dirty: %v %v", dirty, err)
	}
	if c, err := r.SyncNested(ctx); err != nil || c == nil {
		t.Fatalf("SyncNested = %v, %v", c, err)
	}

	// Uncommitted edits inside the nested repository do block a run.
	write(t, front, "app.js", "v3\n")
	if dirty, _ := r.DirtyPaths(ctx); len(dirty) != 1 || dirty[0] != "front" {
		t.Fatalf("dirty = %v, want [front]", dirty)
	}

	if err := r.CreateBranch(ctx, "faztudo/us-1"); err != nil {
		t.Fatal(err)
	}
	if b := r.sub("front").Branch(ctx); b != "faztudo/us-1" {
		t.Fatalf("nested branch = %q", b)
	}
	write(t, root, "docker-compose.yml", "x\n")
	c, err := r.CommitAll(ctx, "passo")
	if err != nil || c == nil {
		t.Fatalf("CommitAll = %v, %v", c, err)
	}
	var paths []string
	for _, f := range c.Files {
		paths = append(paths, f.Path)
	}
	if got := strings.Join(paths, ","); got != "docker-compose.yml,front/app.js" {
		t.Fatalf("files = %s", got)
	}
	if dirty, _ := r.DirtyPaths(ctx); len(dirty) != 0 {
		t.Fatalf("everything must be committed, dirty = %v", dirty)
	}
	if out, _ := r.Show(ctx, c.Hash); !strings.Contains(out, "+v3") {
		t.Errorf("show must include the nested diff:\n%s", out)
	}

	// An interrupted run leaves edits on its own branch: they are saved.
	// Edits on a user branch are left alone.
	write(t, front, "app.js", "v4\n")
	write(t, root, "README.md", "editado\n")
	run(root, "switch", "-q", "main")
	saved, left, err := r.SaveInterrupted(ctx, []string{".", "front"}, "salva")
	if err != nil || strings.Join(saved, ",") != "front" || strings.Join(left, ",") != "." {
		t.Fatalf("SaveInterrupted = %v, %v, %v; want [front] saved, [.] left", saved, left, err)
	}
	if dirty, _ := r.DirtyPaths(ctx); strings.Join(dirty, ",") != "." {
		t.Fatalf("dirty after save = %v, want only the parent", dirty)
	}
}

func TestMergeCombinesWorktreesAndAbortsConflicts(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	write(t, dir, "README.md", "loja\n")
	r, _, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	commitIn := func(name, file, content string) string {
		wt := filepath.Join(t.TempDir(), name)
		if err := r.AddWorktree(ctx, wt); err != nil {
			t.Fatal(err)
		}
		write(t, wt, file, content)
		c, err := At(wt).CommitAll(ctx, name)
		if err != nil || c == nil {
			t.Fatalf("commit %s: %v", name, err)
		}
		return c.Hash
	}
	ui, api := commitIn("ui", "ui.txt", "ui\n"), commitIn("api", "api.txt", "api\n")
	clash := commitIn("clash", "ui.txt", "outro\n")

	if err := r.Merge(ctx, ui, "merge ui"); err != nil {
		t.Fatal(err)
	}
	if err := r.Merge(ctx, api, "merge api"); err != nil {
		t.Fatal(err)
	}
	if err := r.Merge(ctx, clash, "merge clash"); err == nil {
		t.Fatal("conflicting merge must fail")
	}
	if dirty, _ := r.Dirty(ctx); dirty {
		t.Error("a conflicting merge must be aborted")
	}
	for _, f := range []string{"ui.txt", "api.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s must be merged: %v", f, err)
		}
	}
}

func TestNestedRepositoriesGetWorktreesAndMerge(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	root := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	front := filepath.Join(root, "front")
	write(t, front, "app.js", "v1\n")
	run(front, "init", "-q")
	run(front, "add", "-A")
	run(front, "commit", "-q", "-m", "front v1")
	write(t, root, "README.md", "pai\n")
	run(root, "init", "-q")
	run(root, "add", "-A")
	run(root, "commit", "-q", "-m", "pai")

	r, _, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CreateBranch(ctx, "faztudo/base"); err != nil {
		t.Fatal(err)
	}
	lanes := t.TempDir()
	// lane develops a story on its own branch and worktree, nested repository included.
	lane := func(branch string, files map[string]string) {
		t.Helper()
		wt := filepath.Join(lanes, strings.TrimPrefix(branch, "faztudo/"))
		if err := r.AddBranchWorktree(ctx, wt, branch); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(wt, "front", "app.js")); err != nil {
			t.Fatalf("nested repository missing from the worktree: %v", err)
		}
		w := At(wt)
		if got := w.Nested(); len(got) != 1 || got[0] != "front" {
			t.Fatalf("worktree nested = %v", got)
		}
		for f, content := range files {
			write(t, wt, f, content)
		}
		if c, err := w.CommitAll(ctx, branch); err != nil || c == nil {
			t.Fatalf("commit %s: %v %v", branch, c, err)
		}
		if err := r.RemoveWorktree(ctx, wt); err != nil {
			t.Fatal(err)
		}
	}
	consistent := func() {
		t.Helper()
		if dirty, _ := r.DirtyPaths(ctx); len(dirty) != 0 {
			t.Fatalf("dirty = %v", dirty)
		}
		head, _ := r.sub("front").Head(ctx)
		if link := r.gitlinkAt(ctx, "HEAD", "front"); link != head {
			t.Fatalf("parent records front at %s, front is at %s", link, head)
		}
	}

	lane("faztudo/a", map[string]string{"front/a.js": "a\n", "a.md": "a\n"})
	lane("faztudo/b", map[string]string{"front/b.js": "b\n"})
	if out := run(front, "worktree", "list"); strings.Count(out, "\n") != 0 {
		t.Fatalf("nested worktrees left behind:\n%s", out)
	}
	for _, b := range []string{"faztudo/a", "faztudo/b"} {
		if conflicts, err := r.StartMerge(ctx, b, "integra "+b); err != nil || len(conflicts) > 0 {
			t.Fatalf("merge %s: %v %v", b, conflicts, err)
		}
	}
	for _, f := range []string{"a.md", "front/a.js", "front/b.js"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("%s must be merged: %v", f, err)
		}
	}
	consistent()

	// Both stories change the same nested file: the conflict is reported with
	// its path from the root, resolved, and concluded in both repositories.
	lane("faztudo/c", map[string]string{"front/app.js": "c\n"})
	lane("faztudo/d", map[string]string{"front/app.js": "d\n"})
	lane("faztudo/e", map[string]string{"front/app.js": "e\n", "front/e.js": "e\n"})
	if conflicts, err := r.StartMerge(ctx, "faztudo/c", "integra c"); err != nil || len(conflicts) > 0 {
		t.Fatalf("merge c: %v %v", conflicts, err)
	}
	conflicts, err := r.StartMerge(ctx, "faztudo/d", "integra d")
	if err != nil || strings.Join(conflicts, ",") != "front/app.js" {
		t.Fatalf("merge d: conflicts = %v, err = %v", conflicts, err)
	}
	write(t, root, "front/app.js", "c+d\n")
	if _, err := r.FinishMerge(ctx); err != nil {
		t.Fatal(err)
	}
	consistent()

	// An aborted merge rolls the nested repository back too.
	if conflicts, _ := r.StartMerge(ctx, "faztudo/e", "integra e"); len(conflicts) == 0 {
		t.Fatal("merge e must conflict")
	}
	r.AbortMerge(ctx)
	consistent()
	if _, err := os.Stat(filepath.Join(root, "front", "e.js")); err == nil {
		t.Error("aborted merge left the story's files")
	}

	// Detached worktrees fast-forward the nested repository with the parent.
	wt := filepath.Join(lanes, "candidate")
	if err := r.AddWorktree(ctx, wt); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "front/f.js", "f\n")
	c, err := At(wt).CommitAll(ctx, "candidato")
	if err != nil || c == nil {
		t.Fatalf("commit: %v %v", c, err)
	}
	if err := r.FastForward(ctx, c.Hash); err != nil {
		t.Fatal(err)
	}
	_ = r.RemoveWorktree(ctx, wt)
	if _, err := os.Stat(filepath.Join(root, "front", "f.js")); err != nil {
		t.Errorf("fast-forward must reach the nested repository: %v", err)
	}
	consistent()
}

func TestCommitSkipsIgnoredExcludes(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// The nested repository ignores /node_modules (which matches a symlink);
	// the parent ignores node_modules/ (which does not).
	front := filepath.Join(root, "front")
	write(t, front, "app.js", "v1\n")
	write(t, front, ".gitignore", "/node_modules\n")
	run(front, "init", "-q")
	run(front, "add", "-A")
	run(front, "commit", "-q", "-m", "front")
	write(t, root, ".gitignore", "node_modules/\n")
	write(t, root, "README.md", "pai\n")
	run(root, "init", "-q")
	run(root, "add", "-A")
	run(root, "commit", "-q", "-m", "pai")
	deps := t.TempDir()

	r, _, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := r.AddWorktree(ctx, wt); err != nil {
		t.Fatal(err)
	}
	defer r.RemoveWorktree(ctx, wt)
	for _, link := range []string{"node_modules", "front/node_modules"} {
		if err := os.Symlink(deps, filepath.Join(wt, link)); err != nil {
			t.Fatal(err)
		}
	}
	write(t, wt, "front/app.js", "v2\n")
	c, err := At(wt).CommitAll(ctx, "frente", "node_modules", "front/node_modules")
	if err != nil || c == nil {
		t.Fatalf("CommitAll = %v, %v", c, err)
	}
	var paths []string
	for _, f := range c.Files {
		paths = append(paths, f.Path)
	}
	if got := strings.Join(paths, ","); got != "front/app.js" {
		t.Fatalf("files = %s, want only front/app.js", got)
	}
}

func TestBaseBranchesPerRepository(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	front := filepath.Join(root, "front")
	write(t, front, "app.js", "v1\n")
	run(front, "init", "-q")
	run(front, "add", "-A")
	run(front, "commit", "-q", "-m", "front")
	run(front, "branch", "develop")
	run(front, "branch", "faztudo/us-1-20261023-120000")
	write(t, root, "README.md", "pai\n")
	run(root, "init", "-q")
	run(root, "add", "-A")
	run(root, "commit", "-q", "-m", "pai")
	run(root, "branch", "develop")
	// A remote-only branch can be chosen too.
	remote := t.TempDir()
	run(remote, "clone", "-q", "--bare", root, "origin.git")
	run(root, "remote", "add", "origin", filepath.Join(remote, "origin.git"))
	run(root, "fetch", "-q", "origin")
	run(root, "push", "-q", "origin", "main:release")
	run(root, "fetch", "-q", "origin")

	r, _, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	repos, err := r.AllBranches(ctx)
	if err != nil || len(repos) != 2 {
		t.Fatalf("AllBranches = %+v, %v", repos, err)
	}
	if got := strings.Join(repos[0].Branches, ","); repos[0].Path != "." || got != "develop,main,release" {
		t.Errorf("root branches = %s (%s)", got, repos[0].Path)
	}
	if got := strings.Join(repos[1].Branches, ","); repos[1].Path != "front" || repos[1].Current != "main" || got != "develop,main" {
		t.Errorf("front = %+v, FazTudo branches must be left out", repos[1])
	}

	if err := r.SwitchBranches(ctx, map[string]string{".": "release", "front": "develop"}); err != nil {
		t.Fatal(err)
	}
	if b := r.Branch(ctx); b != "release" {
		t.Errorf("root on %q, want release", b)
	}
	if b := r.sub("front").Branch(ctx); b != "develop" {
		t.Errorf("front on %q, want develop", b)
	}
	if err := r.SwitchBranches(ctx, map[string]string{"front": "nao-existe"}); err == nil {
		t.Error("a missing branch must fail")
	}

	// The user's branch is never committed to: pointers are recorded on the
	// new FazTudo branch.
	write(t, front, "app.js", "v2\n")
	run(front, "commit", "-q", "-am", "front v2")
	before, _ := r.Head(ctx)
	if err := r.CreateBranch(ctx, "faztudo/us-2"); err != nil {
		t.Fatal(err)
	}
	if after, _ := r.Head(ctx); after == before {
		t.Error("the new branch must record the nested repository's commit")
	}
	if head, _ := r.sub("front").Head(ctx); r.gitlinkAt(ctx, "HEAD", "front") != head {
		t.Error("pointer not synced")
	}
	if release := r.gitlinkAt(ctx, "release", "front"); release == r.gitlinkAt(ctx, "HEAD", "front") {
		t.Error("the base branch must stay untouched")
	}
}
