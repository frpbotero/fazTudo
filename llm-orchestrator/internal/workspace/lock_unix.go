//go:build unix

package workspace

import (
	"errors"
	"os"
	"syscall"
)

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
