package pipeline

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

func deliveryRun(repo string, at time.Time) Run {
	return Run{
		Repository: repo, StartedAt: &at, Result: ResultSucceeded, Validation: ValidationPassed,
		Manifest: &RunManifest{AppVersion: "1.2.3", Env: "linux/amd64", Bundle: "abc", Policy: map[string]string{"other": "0123456789abcdef"}},
		Stories: []StoryRun{{
			ID: "US-1", Title: "US-1 Carrinho", Status: StepWarning, Validation: ValidationFailed, Integration: IntegrationBlocked,
			Branch: "faztudo/us-1", StartCommit: "aaa", Head: "bbb", BuiltOn: map[string]string{"US-0": "ccc"},
			Trace: []quality.Trace{{Requirement: quality.Requirement{ID: "UT-CART-01"}, Status: quality.TraceProven,
				Cases: []quality.CaseRef{{Command: "unit", Name: "TestCart/UT-CART-01", Status: quality.CasePassed}}}},
			Steps: []Step{
				{ID: "checks", Title: "Checks", Status: StepWarning, Validation: ValidationFailed,
					Checks: []quality.Result{{Name: "unit", Command: "go test ./...", Passed: false, ExitCode: 1}},
					Issues: []Issue{{Agent: "backend", Severity: "alta", Description: "falhou com GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123 e senha=hunter2"}}},
			},
		}},
		Promotions: []Promotion{{Stories: []string{"US-1"}, Branch: "faztudo/us-1", Commit: "bbb", Status: IntegrationBlocked, Reason: "validação reprovada",
			Repos: []RepoPromotion{{Path: ".", Base: "main", Candidate: "bbb", Before: "aaa", After: "aaa", Status: IntegrationBlocked}}}},
	}
}

func newDeliveryTest(t *testing.T) (*Orchestrator, string, string) {
	t.Helper()
	repo, config := t.TempDir(), t.TempDir()
	ws, err := workspace.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	o := New(&fakeExecutor{}, ws, testStages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetJournalDir(filepath.Join(config, "runs"))
	return o, repo, config
}

func TestDeliveryPackageHasTheEvidenceAndNoSecret(t *testing.T) {
	o, repo, config := newDeliveryTest(t)
	o.writeDelivery(deliveryRun(repo, time.Now()))
	d := o.Snapshot().Delivery
	if d == nil || !strings.HasPrefix(d.Dir, filepath.Join(config, "entregas")) {
		t.Fatalf("delivery = %+v", d)
	}
	for _, f := range []string{"README.md", "US-1.md", "US-1.json", "conjunto-01.md"} {
		info, err := os.Stat(filepath.Join(d.Dir, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s is readable by others: %v", f, info.Mode())
		}
	}
	if info, _ := os.Stat(d.Dir); info.Mode().Perm() != 0o700 {
		t.Errorf("package dir = %v", info.Mode())
	}
	md, _ := os.ReadFile(filepath.Join(d.Dir, "US-1.md"))
	for _, want := range []string{"Commit de partida", "`aaa`", "UT-CART-01", "TestCart/UT-CART-01", "go test ./...", "Pendências", "US-0", "app 1.2.3"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("US-1.md lacks %q:\n%s", want, md)
		}
	}
	all := string(md)
	for _, f := range []string{"US-1.json", "conjunto-01.md", "README.md"} {
		data, _ := os.ReadFile(filepath.Join(d.Dir, f))
		all += string(data)
	}
	for _, leak := range []string{"ghp_abcdefghijklmnopqrstuvwxyz0123", "hunter2"} {
		if strings.Contains(all, leak) {
			t.Errorf("the package exposes %q", leak)
		}
	}
	data, _ := os.ReadFile(filepath.Join(d.Dir, "US-1.json"))
	var e deliveryEvidence
	if err := json.Unmarshal(data, &e); err != nil || e.Head != "bbb" || len(e.Trace) != 1 || len(e.Pending) != 1 {
		t.Errorf("evidence json = %+v, %v", e, err)
	}
	index, _ := os.ReadFile(filepath.Join(d.Dir, "README.md"))
	if !strings.Contains(string(index), "complementa os checks do CI") {
		t.Error("the package must not present itself as a guarantee")
	}
	if strings.Contains(string(index), "succeeded") || !strings.Contains(string(index), "; resultado conclu") {
		t.Errorf("the result is said in Portuguese, pending work included:\n%s", index)
	}
}

func TestDeliveryPackagesAreRetained(t *testing.T) {
	o, repo, config := newDeliveryTest(t)
	policy := o.ws.Policy()
	policy.DeliveryRetention = 2
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.Local)
	for i := range 4 {
		o.writeDelivery(deliveryRun(repo, start.Add(time.Duration(i)*time.Minute)))
	}
	entries, _ := os.ReadDir(deliveryRoot(filepath.Join(config, "runs"), repo))
	if len(entries) != 2 || entries[0].Name() != "20260101-100200" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("kept %v, want the 2 newest", names)
	}
}

func TestFinishedRunWritesItsPackage(t *testing.T) {
	o, _, _ := newDeliveryTest(t)
	if err := o.Start(one(true)); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, o)
	if run.Delivery == nil || len(run.Delivery.Files) == 0 {
		t.Fatalf("delivery = %+v", run.Delivery)
	}
	index, _ := os.ReadFile(filepath.Join(run.Delivery.Dir, "README.md"))
	if !strings.Contains(string(index), "Simulação") {
		t.Error("a simulated run's package must say nothing in it is evidence")
	}
}

func TestStoryDeliveryReadsCleanly(t *testing.T) {
	st := StoryRun{
		ID: "US-1", Branch: "faztudo/us-1-20261127-071408",
		Risk:      &workspace.Risk{Level: "baixo"},
		Readiness: &Readiness{Criteria: []Criterion{{Text: "Complete marca a tarefa", Status: CriterionMissing, Evidence: "não há método Complete"}, {Text: "sem evidência", Status: CriterionAmbiguous}}},
	}
	md := redactSecrets(renderStoryDelivery(Run{}, st))
	for _, want := range []string{
		"| Risco | baixo |",
		"`faztudo/us-1-20261127-071408`",
		"- Complete marca a tarefa — " + CriterionMissing + ": não há método Complete\n",
		"- sem evidência — " + CriterionAmbiguous + "\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("US-1.md lacks %q:\n%s", want, md)
		}
	}
}

func TestDeliveryNamesEachScenarioByItsPlanID(t *testing.T) {
	st := StoryRun{ID: "US-1", Acceptance: []ScenarioResult{
		{ID: "AC-2", Criterion: "recusa zero", Scenario: "quantidade 0", Status: ScenarioAccepted, Tests: []string{"UT-CART-02"}},
		{Criterion: "extra", Scenario: "s", Status: ScenarioAccepted, Extra: true, Evidence: "src/a.ts:3"},
	}}
	md := renderStoryDelivery(Run{}, st)
	for _, want := range []string{"- **aceito** AC-2 · recusa zero: quantidade 0 — UT-CART-02\n", "- **aceito** extra (fora do plano): s — src/a.ts:3\n"} {
		if !strings.Contains(md, want) {
			t.Errorf("US-1.md lacks %q:\n%s", want, md)
		}
	}
}
