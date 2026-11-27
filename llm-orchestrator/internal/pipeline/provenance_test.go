package pipeline

import (
	"slices"
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/skills"
)

func TestEveryCallRecordsItsPlaybooksAndParameters(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	o.baseStages = []Stage{
		{ID: "frontend", Title: "Front", Role: "fe", Implementer: true, Skills: []string{"test-driven-development", "test-quality"}},
		{ID: "checks", Title: "Checks", Role: "gate", Verifier: true},
	}
	o.stages = o.baseStages
	policy := o.ws.Policy()
	policy.Profiles = []string{"go"}
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, ".faztudo/skills/test-quality.md", "Testes de tabela, sempre.\n")
	_ = o.Start(realStory("Carrinho"))
	run := waitDone(t, o)

	fe := step(run.Stories[0].Steps, "frontend")
	if len(fe.Calls) != 1 {
		t.Fatalf("calls = %+v, logs %+v", fe.Calls, run.Logs)
	}
	c := fe.Calls[0]
	if c.Bundle != skills.Version() || c.PromptSHA == "" || c.Role != "fe" || c.Provider != "Fake" {
		t.Errorf("record = %+v", c)
	}
	names := make([]string, len(c.Skills))
	for i, s := range c.Skills {
		names[i] = s.Name
		if s.Hash != skills.Hash(s.Name) {
			t.Errorf("%s hash = %s", s.Name, s.Hash)
		}
	}
	if !slices.Equal(names, []string{"test-driven-development", "test-quality"}) {
		t.Errorf("skills = %v", names)
	}
	if q := c.Skills[1]; q.Override != ".faztudo/skills/test-quality.md" || q.OverrideHash == "" {
		t.Errorf("the project's complement and its version are not recorded: %+v", q)
	}
	if len(c.Profiles) != 1 || c.Profiles[0].Name != "go" || c.Profiles[0].Hash == "" {
		t.Errorf("profiles = %+v", c.Profiles)
	}
	prompt := lastPromptOf(exec, "fe")
	for _, want := range []string{"perfil de stack `go`", "complemento do projeto: `.faztudo/skills/test-quality.md`", "não reduz"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if !slices.Contains(run.Protected, ".faztudo/skills/test-quality.md") {
		t.Errorf("an agent could rewrite the playbook it follows: protected %v", run.Protected)
	}
}

func TestCallRecordWithoutSkills(t *testing.T) {
	r := callRecord(provider.Request{Role: "x", Prompt: "faça", ReadOnly: true, MaxTurns: 30}, nil, nil)
	if len(r.Skills) != 0 || !r.ReadOnly || r.MaxTurns != 30 || r.PromptSHA != sha("faça") {
		t.Errorf("record = %+v", r)
	}
}
