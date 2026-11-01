package pipeline

import (
	"errors"
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
)

func promptOrchestrator(t *testing.T, exec *fakeExecutor) *Orchestrator {
	t.Helper()
	o, _ := newVerifierTest(t, exec, alwaysPasses)
	o.baseStages = DefaultStages
	o.stages = resolveStages(DefaultStages, nil)
	return o
}

func TestEveryCatalogKeyCanBeEdited(t *testing.T) {
	o := promptOrchestrator(t, &fakeExecutor{})
	seen := map[string]bool{}
	for _, item := range o.Prompts() {
		if seen[item.Key] {
			t.Errorf("duplicate key %s", item.Key)
		}
		seen[item.Key] = true
		if strings.TrimSpace(item.Value) == "" || item.Group == "" || item.Title == "" || len(item.Parts) == 0 {
			t.Errorf("incomplete item: %+v", item)
		}
		if err := o.SetPrompt(item.Key, "texto novo"); err != nil {
			t.Errorf("%s: %v", item.Key, err)
		}
		if o.Prompt(item.Key) != "texto novo" {
			t.Errorf("%s did not take the new text", item.Key)
		}
	}
	for _, key := range []string{"stage.review.task", "stage.investigator.system", "lens.ux", "rules.engineering", "fix.task", "stories.detect"} {
		if !seen[key] {
			t.Errorf("catalog misses %s", key)
		}
	}
	if seen["stage.checks.task"] {
		t.Error("the automatic gate calls no model")
	}
}

func TestCustomizationReachesEveryKindOfCall(t *testing.T) {
	overrides := map[string]string{
		"stage.discovery.system": "D-SYS", "stage.frontend.task": "F-TASK", "rules.engineering": "MINHAS REGRAS",
		"lens.spec": "L-FOCO", "fix.task": "FIX-T", "verify.task": "V-T", "stage.investigator.task": "I-T",
		"audit.notice": "A-N", "stage.selector.system": "S-SYS",
	}
	stages := resolveStages(DefaultStages, overrides)
	texts := resolveTexts(overrides)
	byID := map[string]Stage{}
	for _, s := range stages {
		byID[s.ID] = s
	}

	if texts.discovery.System != "D-SYS" || texts.selector.System != "S-SYS" || texts.fixTask != "FIX-T" ||
		texts.verifyTask != "V-T" || texts.investigator.Task != "I-T" || texts.auditNotice != "A-N\n\n" {
		t.Fatalf("texts = %+v", texts)
	}
	if byID["frontend"].Task != "F-TASK" || !strings.HasSuffix(byID["frontend"].System, "MINHAS REGRAS") {
		t.Fatalf("frontend = %+v", byID["frontend"])
	}
	if byID["review"].Lenses[0].Focus != "L-FOCO" {
		t.Fatalf("lens = %+v", byID["review"].Lenses[0])
	}
	if strings.Contains(byID["frontend"].System, engineeringRules) {
		t.Fatal("the shared rules were replaced")
	}
	if fix := buildFixPrompt(texts.fixTask, byID["review"], storyContext{}, nil, "", "", "", nil, 1); !strings.Contains(fix, "FIX-T") {
		t.Fatalf("fix prompt:\n%s", fix)
	}
}

func TestEditingATaskKeepsTheContractAndThePermissions(t *testing.T) {
	stages := resolveStages(DefaultStages, map[string]string{"stage.review.task": "Só olhe nomes."})

	for _, s := range stages {
		if s.ID != "review" {
			continue
		}
		prompt := buildPrompt(s, storyContext{}, nil, nil)
		if !strings.Contains(prompt, "Só olhe nomes.") || !strings.Contains(prompt, verdictInstructions) {
			t.Fatalf("the contract must follow the edited task:\n%s", prompt)
		}
		if !s.ReadOnly {
			t.Fatal("a prompt must not change what a stage may do")
		}
	}
}

func TestResolvingNeverChangesTheDefaults(t *testing.T) {
	before := DefaultStages[1].System
	lens := reviewLenses[0].Focus

	resolveStages(DefaultStages, map[string]string{"stage.spec.system": "x", "lens.spec": "y"})

	if DefaultStages[1].System != before || reviewLenses[0].Focus != lens {
		t.Fatal("defaults were mutated")
	}
}

func TestDefaultTextIsNotACustomization(t *testing.T) {
	o := promptOrchestrator(t, &fakeExecutor{})
	if err := o.SetPrompt("fix.task", "outro"); err != nil {
		t.Fatal(err)
	}

	if err := o.SetPrompt("fix.task", fixTask+"\n"); err != nil {
		t.Fatal(err)
	}

	if len(o.PromptOverrides()) != 0 {
		t.Fatalf("overrides = %v", o.PromptOverrides())
	}
}

func TestInvalidPromptsAreRefused(t *testing.T) {
	o := promptOrchestrator(t, &fakeExecutor{})
	if err := o.SetPrompt("nao.existe", "x"); !errors.Is(err, ErrUnknownPrompt) {
		t.Errorf("unknown key: %v", err)
	}
	for name, value := range map[string]string{"blank": " \n", "nul": "a\x00b", "huge": strings.Repeat("x", maxPromptBytes+1), "utf8": "a\xffb"} {
		if err := o.SetPrompt("fix.task", value); !errors.Is(err, ErrInvalidPrompt) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := o.ResetPrompt("nao.existe"); !errors.Is(err, ErrUnknownPrompt) {
		t.Errorf("reset: %v", err)
	}
}

func TestStaleSavedPromptIsSkipped(t *testing.T) {
	o := promptOrchestrator(t, &fakeExecutor{})

	errs := o.LoadPrompts(map[string]string{"stage.antiga.task": "x", "fix.task": "novo"})

	if len(errs) != 1 || o.Prompt("fix.task") != "novo" {
		t.Fatalf("errs=%v value=%q", errs, o.Prompt("fix.task"))
	}
}

func TestEditDuringARunOnlyAppliesToTheNext(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newVerifierTest(t, exec, alwaysPasses)
	o.baseStages = []Stage{{ID: "spec", Title: "Spec", Role: "spec", System: "original", Task: "t"}}
	o.stages = resolveStages(o.baseStages, nil)
	edited := false
	exec.onCall = func(req provider.Request) {
		if req.Role == "spec" && !edited {
			edited = true
			if err := o.SetPrompt("stage.spec.system", "editado"); err != nil {
				t.Error(err)
			}
		}
	}

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, o)
	if got := o.stages[0].System; got != "original" {
		t.Fatalf("the running run changed: %q", got)
	}

	if err := o.Start(realStory("Carrinho")); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if got := o.stages[0].System; got != "editado" {
		t.Fatalf("the next run must use the edit: %q", got)
	}
	logged := false
	for _, l := range run.Logs {
		logged = logged || strings.Contains(l.Message, "stage.spec.system")
	}
	if !logged {
		t.Error("a run must say which prompts it customizes")
	}
}
