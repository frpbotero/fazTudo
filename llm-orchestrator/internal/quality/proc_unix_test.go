//go:build unix

package quality

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunTimeoutLetsCommandCleanUp(t *testing.T) {
	dir := t.TempDir()
	teardown := Command{Run: "trap 'sleep 1; echo limpo > cleaned; exit 0' INT; sleep 30"}

	r := run(context.Background(), dir, teardown, 200*time.Millisecond, 10*time.Second)

	if !r.TimedOut || r.Passed {
		t.Fatalf("expected a timeout, got %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "cleaned")); err != nil {
		t.Fatalf("teardown did not finish before the kill: %v", err)
	}
}

func TestRunCancellationLetsCommandCleanUp(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	r := run(ctx, dir, Command{Run: "trap 'echo limpo > cleaned; exit 0' INT; sleep 30"}, time.Minute, 10*time.Second)

	if r.Passed || r.TimedOut {
		t.Fatalf("a canceled command neither passes nor times out: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "cleaned")); err != nil {
		t.Fatalf("teardown did not run on cancellation: %v", err)
	}
}

func TestRunKillsCommandIgnoringInterrupt(t *testing.T) {
	start := time.Now()

	r := run(context.Background(), t.TempDir(), Command{Run: "trap '' INT; sleep 30"}, 200*time.Millisecond, 300*time.Millisecond)

	if !r.TimedOut {
		t.Fatalf("expected a timeout, got %+v", r)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("command survived the grace period for %s", elapsed)
	}
}
