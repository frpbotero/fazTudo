package pipeline

import (
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

func TestCleanupRunsAreNotResumable(t *testing.T) {
	o, _ := newVerifierTest(t, &fakeExecutor{}, func(int) quality.Result { return quality.Result{Passed: true} })
	o.mu.Lock()
	o.run.Kind, o.run.Result = RunCleanup, ResultFailed
	o.isJournalOwner = true
	o.mu.Unlock()
	if err := o.Resume(); !errors.Is(err, ErrCleanupNotResumable) {
		t.Errorf("Resume = %v, want ErrCleanupNotResumable", err)
	}
	if err := o.RetryFailed(0, CleanupStage.ID); !errors.Is(err, ErrCleanupNotResumable) {
		t.Errorf("RetryFailed = %v, want ErrCleanupNotResumable", err)
	}
}

func TestValidateCleanupOptions(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "internal", "pay"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "file.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}

	got, err := validateCleanupOptions(repo, CleanupOptions{Area: " internal/pay/ "})
	if err != nil || got.Area != "internal/pay" || got.Rounds != defaultCleanupRounds {
		t.Errorf("valid area: %+v, %v", got, err)
	}
	if got, err := validateCleanupOptions(repo, CleanupOptions{Area: ".", Rounds: 5}); err != nil || got.Area != "." || got.Rounds != 5 {
		t.Errorf("whole repository chosen explicitly: %+v, %v", got, err)
	}
	for _, area := range []string{"", "missing", "/etc", "../x", "internal/../..", "file.go", "escape"} {
		if _, err := validateCleanupOptions(repo, CleanupOptions{Area: area}); !errors.Is(err, ErrInvalidArea) {
			t.Errorf("area %q: err = %v, want ErrInvalidArea", area, err)
		}
	}
	for _, rounds := range []int{-1, 6} {
		if _, err := validateCleanupOptions(repo, CleanupOptions{Area: ".", Rounds: rounds}); !errors.Is(err, ErrInvalidRounds) {
			t.Errorf("rounds %d: err = %v, want ErrInvalidRounds", rounds, err)
		}
	}
}

var reLogPath = regexp.MustCompile("`(faztudo-out/saude-[^`]+)`")

// cleanupTest is a git repository with an area src/ and an agent that runs
// edit on the cleanup call only.
type cleanupTest struct {
	o    *Orchestrator
	repo string
	exec *fakeExecutor
	base string
}

func newCleanupTest(t *testing.T, edit func(repo, prompt string), check func(n int) quality.Result) *cleanupTest {
	t.Helper()
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ct := &cleanupTest{exec: &fakeExecutor{}}
	ct.exec.onCall = func(req provider.Request) {
		if req.Role == CleanupStage.Role && edit != nil {
			edit(ct.repo, req.Prompt)
		}
	}
	if check == nil {
		check = func(int) quality.Result { return quality.Result{Passed: true} }
	}
	ct.o, ct.repo = newVerifierTest(t, ct.exec, check)
	policy := ct.o.ws.Policy()
	policy.Git = true
	policy.Protected = []string{"US.md"}
	if _, err := ct.o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ct.repo, "README.md", "projeto")
	writeFile(t, ct.repo, "US.md", "# US-1")
	writeFile(t, ct.repo, "src/pay.go", "package src\n")
	writeFile(t, ct.repo, "src/pay_test.go", "package src\n// UT-PAY-01\nfunc TestUT_PAY_01(t *testing.T) {}\n")
	writeFile(t, ct.repo, "cmd/main.go", "package main\n")
	gitOut(t, ct.repo, "init", "-q", "-b", "main")
	gitOut(t, ct.repo, "add", ".")
	gitOut(t, ct.repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")
	ct.base = "main"
	return ct
}

func (ct *cleanupTest) run(t *testing.T, opts CleanupOptions) Run {
	t.Helper()
	if err := ct.o.StartCleanup(opts); err != nil {
		t.Fatal(err)
	}
	return waitDone(t, ct.o)
}

func writeDecisionLog(t *testing.T, repo, prompt, text string) {
	m := reLogPath.FindStringSubmatch(prompt)
	if m == nil {
		t.Fatalf("prompt has no decision log path:\n%s", prompt)
	}
	writeFile(t, repo, m[1], text)
}

func TestSimulatedCleanupIsApproved(t *testing.T) {
	ct := newCleanupTest(t, nil, nil)
	run := ct.run(t, CleanupOptions{Area: "src", DryRun: true})
	if run.Result != ResultSucceeded || run.Kind != RunCleanup || run.Cleanup == nil || run.Cleanup.Verdict != VerdictApproved {
		t.Fatalf("run = %+v, logs: %+v", run.Cleanup, run.Logs)
	}
	steps := run.Stories[0].Steps
	if len(steps) != 3 || steps[0].Note != "Simulação: comandos não executados." || steps[1].Status != StepDone {
		t.Errorf("steps = %+v", steps)
	}
	if !strings.Contains(ct.exec.prompts[len(ct.exec.prompts)-1], "Área: `src`") {
		t.Error("the agent must get the area")
	}
}

func TestApprovedCleanupLeavesABranchAndTheBase(t *testing.T) {
	ct := newCleanupTest(t, func(repo, prompt string) {
		writeFile(t, repo, "src/pay.go", "package src\n\n// one place\n")
		writeDecisionLog(t, repo, prompt, "- [x] juntar validação\n  - `src/pay.go:1` duplicada")
	}, nil)
	run := ct.run(t, CleanupOptions{Area: "src", Comments: true})
	c := run.Cleanup
	if run.Result != ResultSucceeded || c.Verdict != VerdictApproved {
		t.Fatalf("result = %s cleanup = %+v logs: %+v", run.Result, c, run.Logs)
	}
	branch := run.Stories[0].Branch
	if !strings.HasPrefix(branch, "faztudo/saude-src-") || c.BaseBranch != ct.base {
		t.Errorf("branch = %q base = %q", branch, c.BaseBranch)
	}
	if got := gitOut(t, ct.repo, "branch", "--show-current"); got != ct.base {
		t.Errorf("repository left on %q, want the base", got)
	}
	if files := gitOut(t, ct.repo, "diff", "--name-only", ct.base, branch); files != "src/pay.go" {
		t.Errorf("branch changes = %q, want only src/pay.go (decision log stays out of git)", files)
	}
	if !strings.Contains(c.DecisionLog, "juntar validação") || len(c.Changed) != 1 {
		t.Errorf("decision log = %q changed = %v", c.DecisionLog, c.Changed)
	}
	var logged []string
	for _, l := range run.Logs {
		logged = append(logged, l.Message)
	}
	for _, want := range []string{"Linha de base: 1 comando(s), 0 com falha", "Verificação da limpeza: 1 comando(s), 0 com falha"} {
		if !strings.Contains(strings.Join(logged, "\n"), want) {
			t.Errorf("logs missing %q:\n%s", want, strings.Join(logged, "\n"))
		}
	}
	if req := ct.exec.reqs[len(ct.exec.reqs)-1]; req.Tier != provider.TierHeavy || req.ReadOnly {
		t.Errorf("cleanup request = %+v, want heavy tier and write access", req)
	}
}

func TestRejectedCleanups(t *testing.T) {
	failNew := func(n int) quality.Result {
		if n == 1 {
			return quality.Result{Passed: true}
		}
		return quality.Result{Failures: []quality.Failure{{Fingerprint: "new"}}}
	}
	cases := []struct {
		name   string
		edit   func(t *testing.T, repo string)
		check  func(int) quality.Result
		reason string
	}{
		{"outside the area", func(t *testing.T, repo string) { writeFile(t, repo, "cmd/main.go", "package main // x\n") }, nil, "fora da área src: cmd/main.go"},
		{"acceptance test dropped", func(t *testing.T, repo string) { writeFile(t, repo, "src/pay_test.go", "package src\n") }, nil, "UT-PAY-01"},
		{"new failure", func(t *testing.T, repo string) { writeFile(t, repo, "src/pay.go", "package src // x\n") }, failNew, "unit passou a falhar"},
		{"input file changed", func(t *testing.T, repo string) { writeFile(t, repo, "US.md", "alterada") }, nil, "Arquivo de entrada"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ct := newCleanupTest(t, func(repo, _ string) { c.edit(t, repo) }, c.check)
			run := ct.run(t, CleanupOptions{Area: "src"})
			cl := run.Cleanup
			if run.Result != ResultFailed || cl.Verdict != VerdictRejected || !strings.Contains(strings.Join(cl.Reasons, " "), c.reason) {
				t.Fatalf("result = %s cleanup = %+v logs: %+v", run.Result, cl, run.Logs)
			}
			if p := renderPendencies(run); !strings.Contains(p, "Verificação da limpeza") && !strings.Contains(p, "Limpeza estrutural") {
				t.Errorf("a rejected cleanup must be listed as pending:\n%s", p)
			}
			if branch := run.Stories[0].Branch; gitOut(t, ct.repo, "branch", "--list", branch) == "" {
				t.Errorf("rejected branch %q must stay for inspection", branch)
			}
			if got := gitOut(t, ct.repo, "branch", "--show-current"); got != ct.base {
				t.Errorf("repository left on %q, want the base", got)
			}
			if data, _ := os.ReadFile(filepath.Join(ct.repo, "US.md")); string(data) != "# US-1" {
				t.Errorf("input file = %q, want the original", data)
			}
		})
	}
}

func TestOldFailuresDoNotRejectACleanup(t *testing.T) {
	old := func(int) quality.Result { return quality.Result{Failures: []quality.Failure{{Fingerprint: "old"}}} }
	ct := newCleanupTest(t, func(repo, _ string) { writeFile(t, repo, "src/pay.go", "package src // x\n") }, old)
	if run := ct.run(t, CleanupOptions{Area: "src"}); run.Cleanup.Verdict != VerdictApproved {
		t.Fatalf("a failure already in the baseline must not reject: %+v", run.Cleanup)
	}
}

func TestStoppedCleanupKeepsWorkAndReturnsToBase(t *testing.T) {
	ct := newCleanupTest(t, func(repo, _ string) { writeFile(t, repo, "src/pay.go", "package src // meio\n") }, nil)
	ct.exec.block = make(chan struct{})
	// Discovery must not block: only the cleanup call waits.
	ct.o.ws.Store(workspace.Report{Path: ct.repo, Summary: "repo"})
	if err := ct.o.StartCleanup(CleanupOptions{Area: "src"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for ct.exec.count(CleanupStage.Role) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := ct.o.StartCleanup(CleanupOptions{Area: "src"}); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second cleanup = %v, want ErrAlreadyRunning", err)
	}
	ct.o.Stop()
	run := waitDone(t, ct.o)
	if run.Result != ResultCanceled || run.Cleanup.Verdict != "" {
		t.Fatalf("result = %s verdict = %q", run.Result, run.Cleanup.Verdict)
	}
	if got := gitOut(t, ct.repo, "branch", "--show-current"); got != ct.base {
		t.Errorf("repository left on %q, want the base", got)
	}
}

func TestCleanupRefusals(t *testing.T) {
	ct := newCleanupTest(t, nil, nil)
	policy := ct.o.ws.Policy()
	policy.Git = false
	if _, err := ct.o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := ct.o.StartCleanup(CleanupOptions{Area: "src"}); !errors.Is(err, ErrCleanupNeedsGit) {
		t.Errorf("real cleanup without git = %v, want ErrCleanupNeedsGit", err)
	}
	if err := ct.o.StartCleanup(CleanupOptions{Area: "nope", DryRun: true}); !errors.Is(err, ErrInvalidArea) {
		t.Errorf("missing area = %v, want ErrInvalidArea", err)
	}
}

func TestDecisionLogIsClipped(t *testing.T) {
	ct := newCleanupTest(t, func(repo, prompt string) {
		writeDecisionLog(t, repo, prompt, strings.Repeat("x", maxDecisionLog+5000))
	}, nil)
	run := ct.run(t, CleanupOptions{Area: "src"})
	if n := len(run.Cleanup.DecisionLog); n > maxDecisionLog+100 || !strings.Contains(run.Cleanup.DecisionLog, "truncado") {
		t.Errorf("decision log has %d bytes, want it clipped at %d", n, maxDecisionLog)
	}
}
