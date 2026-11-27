package pipeline

import (
	"strings"
	"testing"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
)

func TestRedactRemovesSensitiveData(t *testing.T) {
	in := `login maria@loja.com senha=hunter2 Authorization: Bearer abc.def-ghi CPF 123.456.789-09 cartão 4111 1111 1111 1111 token "xyz"`
	out := redact(in)
	for _, leak := range []string{"maria@loja.com", "hunter2", "abc.def-ghi", "123.456.789-09", "4111 1111 1111 1111", "xyz"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q survived: %s", leak, out)
		}
	}
	if !strings.Contains(out, "login") || !strings.Contains(out, "cartão") {
		t.Errorf("ordinary words were removed: %s", out)
	}
}

func TestScenarioIsHeldToTheExecutedEvidence(t *testing.T) {
	traces := []quality.Trace{
		{Requirement: quality.Requirement{ID: "UT-CART-01"}, Status: quality.TraceProven,
			Cases: []quality.CaseRef{{Name: "UT-CART-01", Status: quality.CasePassed, Attachments: []string{"shot.png"}}}},
		{Requirement: quality.Requirement{ID: "UT-CART-02"}, Status: quality.TraceFailed},
		{Requirement: quality.Requirement{ID: "UT-CART-03"}, Status: quality.TraceExitOnly},
	}
	cases := []struct {
		in     ScenarioResult
		status string
	}{
		{ScenarioResult{Criterion: "c", Method: "unitário", Status: "aceito", Tests: []string{"UT-CART-01"}}, ScenarioAccepted},
		{ScenarioResult{Criterion: "c", Method: "unitário", Status: "aceito", Tests: []string{"UT-CART-02"}}, ScenarioRejected},
		{ScenarioResult{Criterion: "c", Method: "unitário", Status: "aceito", Tests: []string{"UT-CART-03"}}, ScenarioNoEvidence},
		{ScenarioResult{Criterion: "c", Method: "e2e", Status: "aceito"}, ScenarioNoEvidence},
		{ScenarioResult{Criterion: "c", Method: "inspeção", Status: "aceito", Evidence: "src/cart.ts:42 trata o vazio"}, ScenarioAccepted},
		{ScenarioResult{Criterion: "c", Method: "inspeção", Status: "aceito", Evidence: "parece certo"}, ScenarioNoEvidence},
		{ScenarioResult{Criterion: "c", Status: "talvez"}, ScenarioNoEvidence},
		{ScenarioResult{Criterion: "c", Status: "recusado"}, ScenarioRejected},
	}
	for i, c := range cases {
		got := checkScenario(c.in, traces)
		if got.Status != c.status {
			t.Errorf("%d: %+v = %s (%s), want %s", i, c.in, got.Status, got.Note, c.status)
		}
	}
	if got := checkScenario(cases[0].in, traces); len(got.Artifacts) != 1 {
		t.Errorf("artifacts of the proven case = %v", got.Artifacts)
	}
	if got := checkScenario(cases[4].in, traces); got.Responsible == "" {
		t.Error("an inspection needs someone who vouches for it")
	}
}

var acceptanceStages = []Stage{
	{ID: "spec", Title: "Spec", Role: "spec", Specifier: true},
	{ID: "frontend", Title: "Front", Role: "fe", Implementer: true},
	{ID: "checks", Title: "Checks", Role: "gate", Verifier: true},
	{ID: "acceptance", Title: "QA", Role: "qa", Gate: true, Acceptor: true, ReadOnly: true},
	{ID: "docs", Title: "Docs", Role: "doc"},
}

// newAcceptanceTest has UT-CART-01 proven by a reported case.
func newAcceptanceTest(t *testing.T, qa string) (*Orchestrator, *fakeExecutor, string) {
	t.Helper()
	exec := &fakeExecutor{outputs: map[string][]string{"qa": {qa}}}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result {
		return reported(quality.Case{Name: "TestCart/UT-CART-01", Status: quality.CasePassed})
	})
	o.baseStages = acceptanceStages
	o.stages = o.baseStages
	exec.onCall = func(req provider.Request) {
		if req.Role == "fe" {
			writeFile(t, repo, "tests/cart.test.mjs", "test('UT-CART-01', () => {})\n")
		}
	}
	return o, exec, repo
}

const cartDoc = "Carrinho\n\n## Plano de testes\n| ID | Caso |\n|---|---|\n| UT-CART-01 | adiciona item |\n"

func TestAcceptedScenarioWithAProvenCasePasses(t *testing.T) {
	o, exec, _ := newAcceptanceTest(t, fence(`{"status":"approved","issues":[],"scenarios":[
		{"criterion":"adiciona item","scenario":"dado carrinho vazio","method":"unitário","status":"aceito","tests":["UT-CART-01"],"evidence":"caso passou; cliente ana@x.com"}]}`))
	_ = o.Start(realStory(cartDoc))
	run := waitDone(t, o)
	st := run.Stories[0]
	if st.Validation != ValidationPassed || len(st.Acceptance) != 1 || st.Acceptance[0].Status != ScenarioAccepted {
		t.Fatalf("validation %s, acceptance %+v, logs %+v", st.Validation, st.Acceptance, run.Logs)
	}
	if strings.Contains(st.Acceptance[0].Evidence, "ana@x.com") {
		t.Errorf("evidence kept an e-mail: %q", st.Acceptance[0].Evidence)
	}
	prompt := lastPromptOf(exec, "qa")
	for _, want := range []string{"Evidência executada", "UT-CART-01", quality.TraceProven, "\"scenarios\""} {
		if !strings.Contains(prompt, want) {
			t.Errorf("QA prompt lacks %q", want)
		}
	}
}

func TestApprovalWithoutEvidenceIsInconclusive(t *testing.T) {
	o, _, _ := newAcceptanceTest(t, fence(`{"status":"approved","issues":[],"scenarios":[
		{"criterion":"adiciona item","method":"e2e","status":"aceito","evidence":"a implementação adiciona"}]}`))
	_ = o.Start(realStory(cartDoc))
	run := waitDone(t, o)
	st := run.Stories[0]
	if st.Validation != ValidationInconclusive || st.Status != StepWarning {
		t.Fatalf("validation %s, status %s, acceptance %+v", st.Validation, st.Status, st.Acceptance)
	}
	if st.Acceptance[0].Status != ScenarioNoEvidence {
		t.Errorf("scenario = %+v", st.Acceptance[0])
	}
}

func TestRejectedScenarioGoesBackToTheImplementer(t *testing.T) {
	o, exec, _ := newAcceptanceTest(t, fence(`{"status":"approved","issues":[],"scenarios":[
		{"criterion":"remove item","scenario":"carrinho com 1 item","method":"unitário","status":"recusado","evidence":"não há remoção"}]}`))
	_ = o.Start(realStory(cartDoc))
	run := waitDone(t, o)
	st := run.Stories[0]
	qa := st.Steps[stepIndex(st.Steps, "acceptance")]
	if len(qa.Issues) != 1 || !strings.Contains(qa.Issues[0].Description, "remove item") {
		t.Fatalf("an approval with a rejected scenario must request changes: %+v", qa.Issues)
	}
	if exec.count("fe") < 2 {
		t.Errorf("the implementer was not asked to fix it: %d call(s)", exec.count("fe"))
	}
}

func TestCodeChangedAfterAcceptanceInvalidatesIt(t *testing.T) {
	o, exec, repo := newAcceptanceTest(t, fence(`{"status":"approved","issues":[],"scenarios":[
		{"criterion":"adiciona item","method":"unitário","status":"aceito","tests":["UT-CART-01"]}]}`))
	fe := exec.onCall
	exec.onCall = func(req provider.Request) {
		fe(req)
		if req.Role == "doc" { // a later stage that touches code
			writeFile(t, repo, "src/cart.js", "export const x = 2\n")
		}
	}
	_ = o.Start(realStory(cartDoc))
	run := waitDone(t, o)
	st := run.Stories[0]
	if st.Acceptance[0].Status != ScenarioInvalidated || st.Validation != ValidationInconclusive {
		t.Fatalf("acceptance %+v, validation %s", st.Acceptance, st.Validation)
	}
}

func TestPlannedScenarioQALeftOutHasNoEvidence(t *testing.T) {
	o, exec, _ := newAcceptanceTest(t, fence(`{"status":"approved","issues":[],"scenarios":[
		{"criterion":"adiciona item","method":"unitário","status":"aceito","tests":["UT-CART-01"]}]}`))
	o.baseStages = append([]Stage{{ID: "arch", Title: "Arq", Role: "arch", Planner: true}}, acceptanceStages...)
	o.stages = o.baseStages
	exec.outputs["arch"] = []string{fence(`{"agents":["frontend"],"acceptance":[
		{"criterion":"adiciona item","scenario":"dado carrinho vazio","method":"unitário"},
		{"criterion":"recusa quantidade zero","scenario":"quantidade 0","method":"unitário","negative":true}]}`)}
	_ = o.Start(realStory(cartDoc))
	run := waitDone(t, o)
	st := run.Stories[0]
	if len(st.Acceptance) != 2 || st.Acceptance[1].Status != ScenarioNoEvidence || !st.Acceptance[1].Negative {
		t.Fatalf("acceptance = %+v", st.Acceptance)
	}
	if st.Validation != ValidationInconclusive {
		t.Errorf("validation = %s", st.Validation)
	}
}

func TestRedactRemovesOnlyCardShapedNumbers(t *testing.T) {
	for _, card := range []string{"4111 1111 1111 1111", "4111-1111-1111-1111", "4111111111111111", "3782 822463 10005"} {
		if out := redact("cartão " + card); strings.Contains(out, card) {
			t.Errorf("%q survived: %s", card, out)
		}
	}
	// Branch names, run IDs and timestamps carry long digit runs that are not
	// cards, even when the digits happen to pass the Luhn check.
	for _, keep := range []string{"faztudo/us-1-20261127-071408", "20261127T071345-b9c3ee27", "pedido 4111 1111 1111 1112", "1-2026-1127-0714-08"} {
		if out := redact(keep); out != keep {
			t.Errorf("%q became %q", keep, out)
		}
	}
}
