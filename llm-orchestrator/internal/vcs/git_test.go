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
}
