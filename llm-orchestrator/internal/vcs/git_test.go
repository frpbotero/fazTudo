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
