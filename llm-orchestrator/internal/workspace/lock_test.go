//go:build unix

package workspace

import (
	"errors"
	"testing"
)

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
