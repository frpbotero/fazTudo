//go:build unix

package provider

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerKillsStuckAgent(t *testing.T) {
	// The shell keeps a child holding stdout, like an agent that left a dev
	// server running in the foreground.
	cli := CLI{Binary: "sh", Args: func(Invocation) []string { return []string{"-c", "sleep 30 & wait"} }}
	start := time.Now()
	_, err := ExecRunner{Timeout: 200 * time.Millisecond}.Run(context.Background(), Invocation{CLI: cli, WorkDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "tempo limite") {
		t.Fatalf("err = %v, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("stuck agent took %s to be killed", elapsed)
	}
}
