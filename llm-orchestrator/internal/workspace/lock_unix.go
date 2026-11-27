//go:build unix

package workspace

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// stackSlotPoll is how often a waiting check retries the stack slot.
const stackSlotPoll = 2 * time.Second

// LockStackSlot waits for the machine's single test stack slot, across FazTudo
// processes, until ctx ends: one stack plus its image build already take most
// of the memory Docker has. onWait is called once if the slot is taken. The
// kernel releases the slot when the process dies.
func LockStackSlot(ctx context.Context, onWait func()) (unlock func(), err error) {
	path, err := stackSlotFile()
	if err != nil {
		return nil, err
	}
	return waitForLock(ctx, path, stackSlotPoll, onWait)
}

// resourcePoll is how often a waiting command retries a resource lock.
const resourcePoll = time.Second

// LockResource waits for the named resource (a database, a port range, a
// service) across FazTudo processes and the stories of a run, until ctx
// ends. Commands that declare the same resource never run at once.
func LockResource(ctx context.Context, name string, onWait func()) (unlock func(), err error) {
	path, err := resourceFile(name)
	if err != nil {
		return nil, err
	}
	return waitForLock(ctx, path, resourcePoll, onWait)
}

func waitForLock(ctx context.Context, path string, poll time.Duration, onWait func()) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for waited := false; ; waited = true {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		if !waited {
			onWait()
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// LockRepository takes the repository's run lock across FazTudo processes.
// The kernel releases it when the process dies, so a crash never leaves the
// repository locked.
func LockRepository(repo string) (unlock func(), err error) {
	path, err := lockFile(repo)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrRepositoryBusy
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
