package pipeline

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

const frontOnly = `{"agents": ["frontend"]}`

func step(steps []Step, id string) *Step {
	if i := stepIndex(steps, id); i >= 0 {
		return &steps[i]
	}
	return nil
}

func TestGateWithoutVerdictIsAskedOnlyForTheVerdict(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"architect": {fence(frontOnly)},
		"rev":       {"Revisei tudo, parece bom.", fence(approved)},
	}}
	run := runFlow(t, exec)

	st := run.Stories[0]
	if got := stepIDs(st.Steps); !strings.Contains(got, "review:done review-formato-1:done") {
		t.Fatalf("steps = %s", got)
	}
	if exec.count("rev") != 2 {
		t.Errorf("rev calls = %d, want the review plus one format recovery", exec.count("rev"))
	}
	last := exec.reqs[len(exec.reqs)-1]
	if last.Role != "rev" || !last.ReadOnly || !strings.Contains(last.Prompt, "nenhum bloco JSON") {
		t.Errorf("format recovery must be read-only and say what was wrong: %+v", last)
	}
	if v := step(st.Steps, "review").Validation; v != ValidationPassed {
		t.Errorf("review validation = %q", v)
	}
	if st.Validation != ValidationPassed || run.Validation != ValidationPassed {
		t.Errorf("story %s/%s, run %s", st.Status, st.Validation, run.Validation)
	}
}

func TestGateThatNeverReturnsAValidVerdictIsInconclusive(t *testing.T) {
	cases := map[string]string{
		"no json":              "sem veredito",
		"unknown status":       fence(`{"status": "ok"}`),
		"approved but blocked": fence(`{"status": "approved", "issues": [{"severity": "alta", "description": "XSS"}]}`),
		"changes, no issues":   fence(`{"status": "changes_requested", "issues": []}`),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			exec := &fakeExecutor{outputs: map[string][]string{
				"architect": {fence(frontOnly)},
				"rev":       {out, out, out},
			}}
			run := runFlow(t, exec)

			st := run.Stories[0]
			if exec.count("rev") != 1+maxFormatRecoveries {
				t.Errorf("rev calls = %d", exec.count("rev"))
			}
			if exec.count("fe") != 1 {
				t.Errorf("an invalid verdict must not trigger fixes: fe calls = %d", exec.count("fe"))
			}
			if s := step(st.Steps, "review-formato-2"); s == nil || s.Status != StepWarning {
				t.Fatalf("steps = %s", stepIDs(st.Steps))
			}
			if v := step(st.Steps, "review").Validation; v != ValidationInconclusive {
				t.Errorf("review validation = %q", v)
			}
			if run.Result != ResultSucceeded || st.Status != StepWarning {
				t.Errorf("the run goes on and finishes: result %s, story %s", run.Result, st.Status)
			}
			if st.Validation != ValidationInconclusive || run.Validation != ValidationInconclusive {
				t.Errorf("a gate without a verdict must not approve: story %s, run %s", st.Validation, run.Validation)
			}
		})
	}
}

func TestUnresolvedIssuesFailValidationThoughTheRunFinishes(t *testing.T) {
	exec := &fakeExecutor{
		outputs: map[string][]string{
			"architect": {fence(frontOnly)},
			"rev":       {fence(`{"status": "changes_requested", "issues": [{"agent": "frontend", "severity": "média", "description": "validar e-mail"}]}`)},
		},
		onCall: func(provider.Request) {}, // the fix changes nothing
	}
	run := runFlow(t, exec)

	st := run.Stories[0]
	if run.Result != ResultSucceeded || st.Status != StepWarning {
		t.Fatalf("result %s, story %s", run.Result, st.Status)
	}
	if st.Validation != ValidationFailed || run.Validation != ValidationFailed {
		t.Errorf("story %s, run %s", st.Validation, run.Validation)
	}
	if !strings.Contains(run.Logs[len(run.Logs)-1].Message, "Validação reprovada") {
		t.Errorf("summary must tell the delivery was not approved: %q", run.Logs[len(run.Logs)-1].Message)
	}
}

func TestSimulationIsNeverAnApproval(t *testing.T) {
	ws, err := workspace.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := New(&fakeExecutor{}, ws, flowStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := o.Start(StartOptions{Stories: []stories.Story{{ID: "US-1", Title: "US-1", Content: "tela"}}, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)

	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s", run.Result)
	}
	if st := run.Stories[0]; st.Validation != ValidationNotEvaluated || run.Validation != ValidationNotEvaluated {
		t.Errorf("story %s, run %s", st.Validation, run.Validation)
	}
	if msg := run.Logs[len(run.Logs)-1].Message; !strings.Contains(msg, "nenhuma entrega foi validada") {
		t.Errorf("summary = %q", msg)
	}
}

func TestValidationAggregatesTheWorstOutcome(t *testing.T) {
	steps := func(vs ...Validation) []Step {
		var out []Step
		for _, v := range vs {
			out = append(out, Step{Validation: v})
		}
		return out
	}
	cases := []struct {
		steps []Step
		want  Validation
	}{
		{nil, ValidationNotEvaluated},
		{steps("", ""), ValidationNotEvaluated},
		{steps(ValidationPassed, ""), ValidationPassed},
		{steps(ValidationPassed, ValidationNotEvaluated), ValidationNotEvaluated},
		{steps(ValidationInconclusive, ValidationPassed), ValidationInconclusive},
		{steps(ValidationPassed, ValidationFailed, ValidationInconclusive), ValidationFailed},
	}
	for _, c := range cases {
		if got := storyValidation(c.steps); got != c.want {
			t.Errorf("storyValidation(%v) = %s, want %s", c.steps, got, c.want)
		}
	}
	// A story from an older journal carries no outcome: it is never read as
	// an approval.
	if got := runValidation([]StoryRun{{Validation: ValidationPassed}, {}}); got != ValidationNotEvaluated {
		t.Errorf("runValidation = %s", got)
	}
}
