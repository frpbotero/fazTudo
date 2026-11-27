package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/vcs"
	"llm-orchestrator/internal/workspace"
)

func TestReconcileCompletesAPartialPromotionWithoutResetting(t *testing.T) {
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		gitOut(t, dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	}
	front := filepath.Join(root, "front")
	writeFile(t, front, "app.js", "v1\n")
	git(front, "init", "-q")
	git(front, "add", "-A")
	git(front, "commit", "-q", "-m", "front v1")
	writeFile(t, root, "README.md", "pai\n")
	git(root, "init", "-q")
	git(root, "add", "-A")
	git(root, "commit", "-q", "-m", "pai")

	ctx := context.Background()
	r, _, err := vcs.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	bases := r.CurrentBranches(ctx)
	if err := r.CreateBranch(ctx, "faztudo/us-1"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, front, "app.js", "v2\n")
	writeFile(t, root, "README.md", "pai da US\n")
	if _, err := r.CommitAll(ctx, "faztudo(US-1): muda os dois"); err != nil {
		t.Fatal(err)
	}
	candidate := strings.TrimSpace(gitOut(t, root, "rev-parse", "faztudo/us-1"))
	if err := r.SwitchBranches(ctx, bases); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "README.md", "outra\n")
	git(root, "commit", "-q", "-am", "base mudou")
	var partial *vcs.PartialMergeError
	if _, err := r.MergeIntoBasesKeeping(ctx, "faztudo/us-1", "integra", bases); !errors.As(err, &partial) {
		t.Fatalf("setup: %v", err)
	}

	ws, err := workspace.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	o := New(&fakeExecutor{}, ws, gatedStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	story := stories.Story{ID: "US-1", Title: "US-1 X"}
	o.last = StartOptions{Stories: []stories.Story{story}}
	o.run = Run{Repository: root, Bases: bases, Result: ResultFailed,
		Stories:    []StoryRun{{ID: "US-1", Status: StepDone, Integration: IntegrationPartial}},
		Promotions: []Promotion{{Stories: []string{storyLabel(story)}, Branch: "faztudo/us-1", Commit: candidate, Status: IntegrationPartial}},
	}

	// Still conflicting: reconciling fails and resets nothing.
	frontHead := strings.TrimSpace(gitOut(t, front, "rev-parse", "main"))
	if _, err := o.Reconcile(); err == nil {
		t.Fatal("a reconcile that cannot merge must fail")
	}
	if got := strings.TrimSpace(gitOut(t, front, "rev-parse", "main")); got != frontHead {
		t.Error("the nested repository was reset")
	}
	// The user settles the conflict on the base; reconciling completes it.
	writeFile(t, root, "README.md", "pai\n")
	git(root, "commit", "-q", "-am", "volta")
	p, err := o.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != IntegrationIntegrated || len(p.Repos) == 0 {
		t.Errorf("promotion = %+v", p)
	}
	if got := o.Snapshot().Stories[0].Integration; got != IntegrationIntegrated {
		t.Errorf("story integration = %s", got)
	}
	if _, err := o.Reconcile(); !errors.Is(err, ErrNothingToReconcile) {
		t.Errorf("a second reconcile = %v", err)
	}
}
