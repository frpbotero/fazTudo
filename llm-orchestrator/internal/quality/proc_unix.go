//go:build unix

package quality

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// isolate starts the command in its own process group. Cancellation first
// asks the group to stop with SIGINT: test runners trap it to run their
// teardown, and Playwright's is the only thing that removes the Docker stacks
// it started, since containers are not children of the command. Whatever is
// alive after grace is killed. The returned function, called once the command
// ended, drops that kill when the group is gone, so its id cannot be reused.
func isolate(cmd *exec.Cmd, grace time.Duration) (finish func()) {
	var mu sync.Mutex
	var kill *time.Timer
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		group := -cmd.Process.Pid
		mu.Lock()
		defer mu.Unlock()
		kill = time.AfterFunc(grace, func() { _ = syscall.Kill(group, syscall.SIGKILL) })
		return syscall.Kill(group, syscall.SIGINT)
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if kill != nil && errors.Is(syscall.Kill(-cmd.Process.Pid, 0), syscall.ESRCH) {
			kill.Stop()
		}
	}
}
