package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llm-orchestrator/internal/stories"
)

// defaultPromptDump lists every text sent to a model when nothing is customized.
func defaultPromptDump(stages []Stage, specials ...Stage) string {
	var b strings.Builder
	section := func(name, text string) { fmt.Fprintf(&b, "=== %s\n%s\n", name, text) }
	for _, s := range append(append([]Stage{}, stages...), specials...) {
		section(s.ID+".system", s.System)
		section(s.ID+".task", s.Task)
		for _, l := range s.Lenses {
			section(s.ID+".lens."+l.ID, l.Focus)
		}
	}
	section("stories.detect", stories.DetectSystem)
	section("audit.notice", auditNotice)
	return b.String()
}

func TestDefaultPromptsUnchanged(t *testing.T) {
	golden := filepath.Join("testdata", "prompts_default.golden")
	texts := resolveTexts(nil)
	got := defaultPromptDump(resolveStages(DefaultStages, nil), texts.discovery, texts.analysis, texts.integration, texts.selector, texts.investigator, texts.cleanup)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatal("default prompts changed; only a deliberate edit may update the golden file")
	}
}
