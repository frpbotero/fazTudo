package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"llm-orchestrator/internal/quality"
)

func TestCleanupRunsAreNotResumable(t *testing.T) {
	o, _ := newVerifierTest(t, &fakeExecutor{}, func(int) quality.Result { return quality.Result{Passed: true} })
	o.mu.Lock()
	o.run.Kind, o.run.Result = RunCleanup, ResultFailed
	o.isJournalOwner = true
	o.mu.Unlock()
	if err := o.Resume(); !errors.Is(err, ErrCleanupNotResumable) {
		t.Errorf("Resume = %v, want ErrCleanupNotResumable", err)
	}
	if err := o.RetryFailed(0, CleanupStage.ID); !errors.Is(err, ErrCleanupNotResumable) {
		t.Errorf("RetryFailed = %v, want ErrCleanupNotResumable", err)
	}
}

func TestValidateCleanupOptions(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "internal", "pay"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "file.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}

	got, err := validateCleanupOptions(repo, CleanupOptions{Area: " internal/pay/ "})
	if err != nil || got.Area != "internal/pay" || got.Rounds != defaultCleanupRounds {
		t.Errorf("valid area: %+v, %v", got, err)
	}
	if got, err := validateCleanupOptions(repo, CleanupOptions{Area: ".", Rounds: 5}); err != nil || got.Area != "." || got.Rounds != 5 {
		t.Errorf("whole repository chosen explicitly: %+v, %v", got, err)
	}
	for _, area := range []string{"", "missing", "/etc", "../x", "internal/../..", "file.go", "escape"} {
		if _, err := validateCleanupOptions(repo, CleanupOptions{Area: area}); !errors.Is(err, ErrInvalidArea) {
			t.Errorf("area %q: err = %v, want ErrInvalidArea", area, err)
		}
	}
	for _, rounds := range []int{-1, 6} {
		if _, err := validateCleanupOptions(repo, CleanupOptions{Area: ".", Rounds: rounds}); !errors.Is(err, ErrInvalidRounds) {
			t.Errorf("rounds %d: err = %v, want ErrInvalidRounds", rounds, err)
		}
	}
}
