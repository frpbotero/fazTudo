//go:build !unix

package provider

import "os/exec"

func isolate(*exec.Cmd) {}
