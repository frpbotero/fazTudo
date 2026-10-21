//go:build unix

package quality

import (
	"os/exec"
	"syscall"
)

// isolate starts the command in its own process group and makes
// cancellation kill the whole group.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
