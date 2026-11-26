//go:build !unix

package provider

import "os/exec"

// killsProcessGroup is false: only the CLI itself is killed on cancel.
const killsProcessGroup = false

func isolate(*exec.Cmd) {}
