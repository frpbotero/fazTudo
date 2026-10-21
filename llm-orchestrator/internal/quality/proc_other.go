//go:build !unix

package quality

import "os/exec"

func isolate(*exec.Cmd) {}
