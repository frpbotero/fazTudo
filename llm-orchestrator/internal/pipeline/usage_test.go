package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

func TestStagesReadOnlyTheirInputs(t *testing.T) {
	previous := []Step{
		{ID: "context", Title: "Passo 0", Output: "achados"},
		{ID: "arch", Title: "Arquiteto", Output: "plano"},
		{ID: "stream-api", Kind: "stream", Agent: "backend", Title: "API", Output: "api pronta"},
		{ID: "checks", Title: "Verificação", Output: "saída enorme dos testes"},
	}
	review := Stage{ID: "review", Task: "revise", Inputs: reviewInputs}
	prompt := buildPrompt(review, storyContext{}, nil, previous)
	if !strings.Contains(prompt, "plano") || !strings.Contains(prompt, "api pronta") {
		t.Errorf("review must see the plan and the implementation:\n%s", prompt)
	}
	if strings.Contains(prompt, "achados") || strings.Contains(prompt, "saída enorme") {
		t.Errorf("review must not get Passo 0 nor check reports:\n%s", prompt)
	}
	if all := buildPrompt(Stage{Task: "x"}, storyContext{}, nil, previous); !strings.Contains(all, "saída enorme") {
		t.Error("a stage without Inputs keeps every step")
	}
}

func TestPromptsShareTheirOpening(t *testing.T) {
	sc := storyContext{Story: stories.Story{ID: "US-1", Content: "Como cliente, quero pagar."}, Rules: rules{Checks: []quality.Command{{Name: "test", Run: "go test ./..."}}}}
	report := &workspace.Report{Path: "/repo", Summary: "mapa"}
	spec := buildPrompt(DefaultStages[1], sc, report, nil)
	arch := buildPrompt(DefaultStages[2], sc, report, []Step{{ID: "spec", Output: "edd"}})
	fix := buildFixPrompt(fixTask, DefaultStages[5], sc, report, "", "", "", nil, 1)
	head := spec[:strings.Index(spec, "## User Story")]
	for name, p := range map[string]string{"arch": arch, "fix": fix} {
		if !strings.HasPrefix(p, head) {
			t.Errorf("%s prompt does not start with the shared context", name)
		}
	}
	if !strings.Contains(head, "go test ./...") {
		t.Error("delivery rules belong to the shared opening")
	}
}

func TestLimitedRequests(t *testing.T) {
	o := &Orchestrator{}
	policy := workspace.DefaultPolicy()
	o.useLimits(policy, []quality.Command{{Name: "test", Run: "npm test"}})
	write := o.limited(provider.Request{})
	if write.MaxTurns != defaultWriteTurns || !slices.Contains(write.Commands, "npm test") ||
		!slices.Contains(write.Commands, "npx jest") || !slices.Contains(write.Commands, workspace.DefaultAgentCommands[0]) {
		t.Errorf("write request = %+v", write)
	}
	if read := o.limited(provider.Request{ReadOnly: true}); read.MaxTurns != defaultReadTurns || read.Commands != nil {
		t.Errorf("read-only request = %+v", read)
	}
	policy.MaxTurns = 12
	o.useLimits(policy, nil)
	if got := o.limited(provider.Request{ReadOnly: true}).MaxTurns; got != minReadTurns {
		t.Errorf("read turns = %d, want %d", got, minReadTurns)
	}
}

func TestUsageLogAndSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "tokens.jsonl")
	started := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	o := &Orchestrator{run: Run{Repository: "/x/loja", StartedAt: &started, Stories: []StoryRun{{ID: "US-1"}}}}
	o.SetUsageLog(path)
	usage := &provider.Usage{InputTokens: 1000, CachedTokens: 800, OutputTokens: 100, CostUSD: 0.5}
	o.recordUsage(stepRef{0, "backend"}, provider.Request{Role: "Backend"}, &provider.Response{Provider: provider.Claude, Model: "claude-sonnet-5-5", Usage: usage}, 1)
	o.recordUsage(stepRef{0, "review"}, provider.Request{Role: "Review"}, &provider.Response{Provider: provider.Codex, Model: "gpt"}, 2)
	o.recordUsage(stepRef{0, "x"}, provider.Request{}, &provider.Response{Simulated: true, Usage: usage}, 1)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "\n"); n != 2 {
		t.Fatalf("want 2 records (simulation skipped), got %d:\n%s", n, data)
	}
	for _, want := range []string{`"story":"US-1"`, `"task":"backend"`, `"agent":"claude"`, `"input_tokens":1000`, `"attempt":2`, `"usage_reported":false`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("log misses %s:\n%s", want, data)
		}
	}

	var out bytes.Buffer
	if err := WriteUsageSummary(bytes.NewReader(data), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2026-10-28T09:00:00Z", "loja", "TOTAL", "1000", "0.5000", "1 chamada(s) sem contagem"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary misses %q:\n%s", want, out.String())
		}
	}
}
