//go:build unix

package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStackSlotWaitsForTheHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slot.lock")
	never := func() { t.Error("a free slot must not wait") }
	release, err := waitForLock(context.Background(), path, 10*time.Millisecond, never)
	if err != nil {
		t.Fatal(err)
	}

	waiting := make(chan struct{}, 8)
	acquired := make(chan error, 1)
	go func() {
		unlock, err := waitForLock(context.Background(), path, 10*time.Millisecond, func() { waiting <- struct{}{} })
		if err == nil {
			unlock()
		}
		acquired <- err
	}()

	<-waiting
	select {
	case err := <-acquired:
		t.Fatalf("second holder got in while the slot was taken: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	if err := <-acquired; err != nil {
		t.Fatal(err)
	}
}

func TestStackSlotWaitEndsWithContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slot.lock")
	release, err := waitForLock(context.Background(), path, time.Millisecond, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())

	_, err = waitForLock(ctx, path, time.Millisecond, cancel)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestLockRepositoryRefusesASecondHolder(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	unlock, err := LockRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockRepository(repo); !errors.Is(err, ErrRepositoryBusy) {
		t.Fatalf("second lock err = %v, want ErrRepositoryBusy", err)
	}
	other, err := LockRepository(t.TempDir())
	if err != nil {
		t.Fatalf("another repository must lock freely: %v", err)
	}
	other()
	unlock()
	again, err := LockRepository(repo)
	if err != nil {
		t.Fatalf("lock must be free after unlock: %v", err)
	}
	again()
}
