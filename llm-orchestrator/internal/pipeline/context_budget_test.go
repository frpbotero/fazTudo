package pipeline

import (
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
)

func TestGatesSeeTheChangedFilesAndArtifactsFitTheBudget(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{"fe": {strings.Repeat("x", 3000) + "\n" + fence(approved)}}}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	o.baseStages = []Stage{
		{ID: "frontend", Title: "Front", Role: "fe", Implementer: true},
		{ID: "checks", Title: "Checks", Role: "gate", Verifier: true},
		{ID: "review", Title: "Review", Role: "rev", Gate: true, ReadOnly: true},
	}
	o.stages = o.baseStages
	policy := o.ws.Policy()
	policy.ContextBytes = 1000
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	exec.onCall = func(req provider.Request) {
		if req.Role == "fe" {
			writeFile(t, repo, "src/cart.js", "export const total = 1\n")
		}
	}
	_ = o.Start(realStory("Carrinho"))
	run := waitDone(t, o)
	prompt := lastPromptOf(exec, "rev")
	if !strings.Contains(prompt, "## Arquivos alterados nesta US") || !strings.Contains(prompt, "`src/cart.js`") {
		t.Errorf("the reviewer was not pointed at the change:\n%s", prompt)
	}
	if strings.Contains(lastPromptOf(exec, "fe"), "Arquivos alterados nesta US") {
		t.Error("only gates get the changed files")
	}
	if !strings.Contains(prompt, truncationMark) || strings.Count(prompt, "x") > 1500 {
		t.Error("the front-end's long output must be cut to the budget, saying so")
	}
	rev := step(run.Stories[0].Steps, "review")
	if len(rev.Calls) != 1 || rev.Calls[0].Truncated != 1 || rev.Calls[0].PromptBytes == 0 {
		t.Errorf("the call record must count the cut: %+v", rev.Calls)
	}
}

func TestWriteChangedCutsALongList(t *testing.T) {
	var files []string
	for i := range maxChangedListed + 5 {
		files = append(files, strings.Repeat("a", i+1)+".go")
	}
	var b strings.Builder
	writeChanged(&b, files)
	if !strings.Contains(b.String(), "e mais 5") || strings.Count(b.String(), ".go`") != maxChangedListed {
		t.Errorf("list = %s", b.String())
	}
}

func TestSameContentKeepsTheApproval(t *testing.T) {
	exec := &fakeExecutor{}
	calls := 0
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { calls++; return quality.Result{Passed: true} })
	o.baseStages = []Stage{
		{ID: "frontend", Title: "Front", Role: "fe", Implementer: true},
		{ID: "checks", Title: "Checks", Role: "gate", Verifier: true},
		{ID: "docs", Title: "Docs", Role: "doc"},
		{ID: "checks2", Title: "Checks 2", Role: "gate", Verifier: true},
	}
	o.stages = o.baseStages
	exec.onCall = func(req provider.Request) {
		// Both agents write the same content: the second rewrite changes nothing.
		writeFile(t, repo, "src/cart.js", "export const total = 1\n")
	}
	_ = o.Start(realStory("Carrinho"))
	run := waitDone(t, o)
	if c := step(run.Stories[0].Steps, "checks2"); c.Status != StepSkipped || !strings.Contains(c.Note, "mesmo conteúdo") {
		t.Errorf("second verification = %s %q", c.Status, c.Note)
	}
	if calls != 1 {
		t.Errorf("the commands ran %d time(s) for one content", calls)
	}
}
