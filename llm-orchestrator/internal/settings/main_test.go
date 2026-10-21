package settings

import (
	"os"
	"testing"
)

// TestMain isolates tests from the developer's real Codex installation, so
// the built-in model catalog is used instead of ~/.codex/models_cache.json.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "codex-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("CODEX_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
