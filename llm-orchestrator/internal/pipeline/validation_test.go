package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
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

// preflightExecutor is a fake whose providers answer a preflight.
type preflightExecutor struct {
	fakeExecutor
	warnings []string
	err      error
	need     *provider.Requirements
}

func (p *preflightExecutor) Preflight(_ context.Context, need provider.Requirements) ([]string, error) {
	p.need = &need
	return p.warnings, p.err
}

func TestRunDoesNotStartWhenNoProviderCanServeThePolicy(t *testing.T) {
	exec := &preflightExecutor{err: fmt.Errorf("%w: Codex CLI sem restrição de comandos", provider.ErrNoCapableProvider)}
	ws, err := workspace.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy := ws.Policy()
	policy.StrictCommands = true
	if _, err := ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	o := New(exec, ws, testStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	err = o.Start(one(false))
	if !errors.Is(err, provider.ErrNoCapableProvider) {
		t.Fatalf("err = %v", err)
	}
	if exec.need == nil || !exec.need.StrictCommands {
		t.Errorf("the policy's requirements must reach the preflight: %+v", exec.need)
	}
	if o.Snapshot().Running || len(exec.prompts) != 0 {
		t.Error("a refused run must not start any agent")
	}

	// A simulation calls no CLI: no preflight.
	exec.need = nil
	if err := o.Start(one(true)); err != nil || exec.need != nil {
		t.Fatalf("dry run: err %v, preflight %+v", err, exec.need)
	}
	waitDone(t, o)

	// Warnings do not stop the run; they are logged.
	exec.err, exec.warnings = nil, []string{"Codex CLI não aplica limite de turnos"}
	if err := o.Start(one(false)); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if !slices.ContainsFunc(run.Logs, func(l LogEntry) bool { return l.Level == LevelWarn && strings.Contains(l.Message, "limite de turnos") }) {
		t.Error("preflight warnings must be logged")
	}
}

func TestWritingRequestsCarryStrictCommands(t *testing.T) {
	ws, err := workspace.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := New(&fakeExecutor{}, ws, testStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.useLimits(workspace.Policy{StrictCommands: true}, nil)
	if !o.limited(provider.Request{}).StrictCommands {
		t.Error("writing request must demand an allowlist")
	}
	if o.limited(provider.Request{ReadOnly: true}).StrictCommands {
		t.Error("read-only requests run no command to restrict")
	}
}
