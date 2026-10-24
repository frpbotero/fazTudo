package pipeline

import (
	"context"
	"os"
	"testing"

	"llm-orchestrator/internal/workspace"
)

// TestMain keeps the suite off the graphify installed on the machine: test
// repositories get no code graph.
func TestMain(m *testing.M) {
	updateCodeGraph = func(context.Context, string) (string, error) { return "", workspace.ErrGraphUnavailable }
	os.Exit(m.Run())
}
