package pipeline

import (
	"context"
	"os"
	"testing"

	"llm-orchestrator/internal/workspace"
)

// TestMain keeps the suite off the graphify installed on the machine, so test
// repositories get no code graph, and off Docker and the real stack slot, which
// a FazTudo running on the machine may be holding.
func TestMain(m *testing.M) {
	updateCodeGraph = func(context.Context, string) (string, error) { return "", workspace.ErrGraphUnavailable }
	defaultSweepStacks = func(context.Context, []string, []string) ([]string, error) { return nil, nil }
	defaultLockStackSlot = func(context.Context, func()) (func(), error) { return func() {}, nil }
	os.Exit(m.Run())
}
