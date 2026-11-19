//go:build !unix

package quality

import (
	"os/exec"
	"time"
)

func isolate(*exec.Cmd, time.Duration) (finish func()) { return func() {} }
