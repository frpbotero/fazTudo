package pipeline

import (
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/stories"
)

var threeStories = []stories.Story{
	{ID: "US-1", Title: "US-1 Catálogo", Content: "<<um>>"},
	{ID: "US-2", Title: "US-2 Perfil", Content: "<<dois>>"},
	{ID: "US-3", Title: "US-3 Checkout", Content: "<<tres>> usa o catálogo da US-1"},
}

// independentPair makes US-1 and US-2 independent and US-3 wait for both.
var independentPair = fence(`{"stories": [
	{"id": "US-1", "depends_on": []},
	{"id": "us-2", "depends_on": ["nada"], "areas": ["src/profile/"]},
	{"id": "US-3", "depends_on": ["US-1", "US-2"]}]}`)

func TestStoryKeysFallBackToPosition(t *testing.T) {
	keys := storyKeys([]stories.Story{{ID: "US-1"}, {}, {ID: "us-1"}, {ID: "US-4"}})
	if got := strings.Join(keys, ","); got != "#1,#2,#3,US-4" {
		t.Errorf("keys = %s", got)
	}
}

func TestParseAnalysisBuildsWaves(t *testing.T) {
	nodes, ok := parseAnalysis(independentPair, storyKeys(threeStories))
	if !ok {
		t.Fatal("analysis must parse")
	}
	if got := fmt.Sprint(storyWaves(nodes)); got != "[[0 1] [2]]" {
		t.Errorf("waves = %s", got)
	}
	if len(nodes[1].DependsOn) != 0 || nodes[1].Files[0] != "src/profile/" {
		t.Errorf("US-2 = %+v", nodes[1])
	}

	partial, _ := parseAnalysis(fence(`{"stories": [{"id": "US-1"}]}`), storyKeys(threeStories))
	if got := fmt.Sprint(storyWaves(partial)); got != "[[0] [1] [2]]" {
		t.Errorf("unlisted stories must wait for the previous one: %s", got)
	}
}

func TestReferencedDependenciesInSimulation(t *testing.T) {
	nodes := referencedDependencies(threeStories, storyKeys(threeStories))
	if got := fmt.Sprint(storyWaves(nodes)); got != "[[0 1] [2]]" {
		t.Errorf("waves = %s", got)
	}
}

func TestParallelStoriesInSimulation(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{AnalysisStage.Role: {independentPair}}}
	o, repo := newTest(t, exec)
	writeFile(t, repo, "CLAUDE.md", "Login antes de perfil.")
	var both sync.WaitGroup
	both.Add(2)
	exec.onCall = func(req provider.Request) {
		if req.Role == "ra" && !strings.Contains(req.Prompt, "<<tres>>") {
			both.Done()
			if !waitTimeout(&both, time.Second) {
				t.Error("stories of the same wave must run in parallel")
			}
		}
	}
	_ = o.Start(StartOptions{Stories: threeStories, Source: "Markdown", DryRun: true})
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if got := fmt.Sprint(run.Waves); got != "[[0 1] [2]]" {
		t.Fatalf("waves = %s", got)
	}
	if s := run.Stories[2]; s.Wave != 2 || strings.Join(s.DependsOn, ",") != "US-1,US-2" {
		t.Errorf("US-3 = wave %d, depends on %v", s.Wave, s.DependsOn)
	}
	for _, r := range exec.reqs {
		if !strings.Contains(r.Prompt, "Login antes de perfil.") {
			t.Errorf("%s prompt lacks the project instructions", r.Role)
		}
		if r.Role == AnalysisStage.Role {
			continue
		}
		switch p := r.Prompt; {
		case strings.Contains(p, "<<um>>") && !strings.Contains(p, "outras branches"):
			t.Error("US-1 must know US-2 runs beside it")
		case strings.Contains(p, "<<um>>") && !strings.Contains(p, "só portas 4400 a 4419"):
			t.Error("US-1 must get its own port range")
		case strings.Contains(p, "<<dois>>") && !strings.Contains(p, "só portas 4420 a 4439"):
			t.Error("US-2 must get a port range apart from US-1")
		case strings.Contains(p, "<<tres>>") && !strings.Contains(p, "- US-2 Perfil"):
			t.Error("US-3 must see US-1 and US-2 as delivered")
		}
	}
}

// newParallelStoryTest runs real stories over a git repository; agents write
// files through write.
func newParallelStoryTest(t *testing.T, exec *fakeExecutor) (*Orchestrator, string) {
	t.Helper()
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	o.baseStages = testStages
	o.stages = o.baseStages
	o.attempts = 1
	policy := o.ws.Policy()
	policy.Git = true
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "README.md", "loja")
	return o, repo
}

func twoIndependent() StartOptions {
	return StartOptions{Stories: threeStories[:2], Source: "Markdown"}
}

// storyMarker returns which story a prompt belongs to.
func storyMarker(prompt string) string {
	for _, m := range []string{"um", "dois", "tres"} {
		if strings.Contains(prompt, "<<"+m+">>") {
			return m
		}
	}
	return ""
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := osexec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestParallelStoriesRunInWorktreesAndIntegrate(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{AnalysisStage.Role: {independentPair}}}
	o, repo := newParallelStoryTest(t, exec)
	exec.onCall = func(req provider.Request) {
		if m := storyMarker(req.Prompt); m != "" && req.Role == "ra" {
			writeFile(t, req.WorkDir, m+".txt", m+"\n")
		}
	}
	_ = o.Start(twoIndependent())
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	for _, f := range []string{"um.txt", "dois.txt"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err != nil {
			t.Errorf("%s must be integrated: %v", f, err)
		}
	}
	if len(run.Integrations) != 1 || gitOut(t, repo, "branch", "--show-current") != run.BaseBranch {
		t.Errorf("repository must end on the base branch %q, not on %v", run.BaseBranch, run.Integrations)
	}
	if out := gitOut(t, repo, "branch", "--contains", run.Integrations[0]); !strings.Contains(out, run.BaseBranch) {
		t.Errorf("the base branch must contain the integration branch:\n%s", out)
	}
	if run.Stories[0].Branch == "" || run.Stories[0].Branch == run.Stories[1].Branch {
		t.Errorf("each story needs its own branch: %q %q", run.Stories[0].Branch, run.Stories[1].Branch)
	}
	for _, r := range exec.reqs {
		if r.Role != AnalysisStage.Role && storyMarker(r.Prompt) != "" && r.WorkDir == repo {
			t.Errorf("%s ran in the repository instead of a worktree", r.Role)
		}
	}
	if out := gitOut(t, repo, "worktree", "list"); strings.Count(out, "\n") != 0 {
		t.Errorf("worktrees must be removed:\n%s", out)
	}
}

func TestConflictingStoriesAreResolvedByIntegration(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{AnalysisStage.Role: {independentPair}}}
	o, repo := newParallelStoryTest(t, exec)
	exec.onCall = func(req provider.Request) {
		switch m := storyMarker(req.Prompt); {
		case req.Role == IntegrationStage.Role:
			writeFile(t, req.WorkDir, "shared.txt", "um e dois\n")
		case m != "" && req.Role == "ra":
			writeFile(t, req.WorkDir, "shared.txt", m+"\n")
		}
	}
	_ = o.Start(twoIndependent())
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	merge := run.Stories[1].Steps[len(run.Stories[1].Steps)-1]
	if merge.ID != IntegrationStage.ID || merge.Status != StepDone || merge.Commit == nil {
		t.Fatalf("integration step = %+v", merge)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "shared.txt")); string(data) != "um e dois\n" {
		t.Errorf("shared.txt = %q", data)
	}
	if gitOut(t, repo, "status", "--porcelain") != "" {
		t.Error("the merge must be concluded")
	}
}

func TestUnresolvedConflictLeavesStoryOnItsBranch(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{AnalysisStage.Role: {independentPair}}}
	o, repo := newParallelStoryTest(t, exec)
	exec.onCall = func(req provider.Request) {
		if m := storyMarker(req.Prompt); m != "" && req.Role == "ra" {
			writeFile(t, req.WorkDir, "shared.txt", m+"\n")
		}
	}
	_ = o.Start(twoIndependent())
	run := waitDone(t, o)
	if run.Result != ResultSucceeded || run.Stories[1].Status != StepWarning {
		t.Fatalf("result = %s, US-2 = %s", run.Result, run.Stories[1].Status)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "shared.txt")); string(data) != "um\n" {
		t.Errorf("the aborted merge must keep US-1's file, got %q", data)
	}
	if gitOut(t, repo, "status", "--porcelain") != "" {
		t.Error("the merge must be aborted")
	}
}

func TestRetryResumesParallelStoryOnItsBranch(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{AnalysisStage.Role: {independentPair}}}
	o, repo := newParallelStoryTest(t, exec)
	var mu sync.Mutex
	failed := false
	exec.onCall = func(req provider.Request) {
		if m := storyMarker(req.Prompt); m != "" && req.Role == "ra" {
			writeFile(t, req.WorkDir, m+".txt", m+"\n")
		}
	}
	exec.failIf = func(req provider.Request) bool {
		mu.Lock()
		defer mu.Unlock()
		if storyMarker(req.Prompt) != "dois" || req.Role != "rb" || failed {
			return false
		}
		failed = true
		writeFile(t, req.WorkDir, "meio.txt", "parcial\n")
		return true
	}
	_ = o.Start(twoIndependent())
	run := waitDone(t, o)
	if run.Result != ResultFailed || run.Stories[0].Status != StepDone || run.Stories[1].Status != StepFailed {
		t.Fatalf("result = %s, stories = %s, %s", run.Result, run.Stories[0].Status, run.Stories[1].Status)
	}
	branch := run.Stories[1].Branch
	if !strings.Contains(gitOut(t, repo, "show", "--stat", branch), "meio.txt") {
		t.Error("an interrupted story must keep its partial work on its branch")
	}

	if err := o.RetryFailed(1, "b"); err != nil {
		t.Fatal(err)
	}
	run = waitDone(t, o)
	if run.Result != ResultSucceeded || run.Stories[1].Branch != branch {
		t.Fatalf("result = %s, branch %q, logs: %+v", run.Result, run.Stories[1].Branch, run.Logs)
	}
	if exec.count("ra") != 2 {
		t.Errorf("US-1 must not run again and US-2 resumes after A: roles = %v", exec.roles)
	}
	for _, f := range []string{"um.txt", "dois.txt", "meio.txt"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err != nil {
			t.Errorf("%s must be integrated: %v", f, err)
		}
	}
}

func TestRetryFailedAnalysisStartsAgain(t *testing.T) {
	exec := &fakeExecutor{failAt: 2} // discovery, then the analysis fails
	o, _ := newTest(t, exec)
	_ = o.Start(StartOptions{Stories: threeStories, DryRun: true})
	run := waitDone(t, o)
	if run.Result != ResultFailed || run.Analysis.Status != StepFailed || run.Stories[0].Status != StepCanceled {
		t.Fatalf("result = %s, analysis = %+v", run.Result, run.Analysis)
	}
	if err := o.RetryFailed(-1, AnalysisStage.ID); err != nil {
		t.Fatal(err)
	}
	if run = waitDone(t, o); run.Result != ResultSucceeded {
		t.Fatalf("retry result = %s", run.Result)
	}
}

// initRepo versions repo with a first commit, as the user's repository would
// be, and returns its branch.
func initRepo(t *testing.T, repo string) string {
	t.Helper()
	gitOut(t, repo, "init", "-q")
	gitOut(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "add", "-A")
	gitOut(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--no-verify", "-m", "inicial")
	return gitOut(t, repo, "branch", "--show-current")
}

// baseHas reports whether the base branch already holds file.
func baseHas(t *testing.T, repo, base, file string) bool {
	t.Helper()
	return osexec.Command("git", "-C", repo, "cat-file", "-e", base+":"+file).Run() == nil
}

func TestEachWaveIsMergedIntoTheBaseBeforeTheNext(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{AnalysisStage.Role: {independentPair}}}
	o, repo := newParallelStoryTest(t, exec)
	base := initRepo(t, repo)
	var sawWave1InBase, startedFromBase bool
	exec.onCall = func(req provider.Request) {
		switch m := storyMarker(req.Prompt); {
		case m == "" || req.Role != "ra":
		case m == "tres":
			// Wave 2 starts only once the base holds wave 1.
			sawWave1InBase = baseHas(t, repo, base, "um.txt") && baseHas(t, repo, base, "dois.txt")
			_, err := os.Stat(filepath.Join(req.WorkDir, "um.txt"))
			startedFromBase = err == nil
			writeFile(t, req.WorkDir, "tres.txt", "tres\n")
		default:
			writeFile(t, req.WorkDir, m+".txt", m+"\n")
		}
	}
	_ = o.Start(StartOptions{Stories: threeStories, Source: "Markdown"})
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if !sawWave1InBase || !startedFromBase {
		t.Errorf("wave 2 began before the base held wave 1 (base has it: %v, worktree has it: %v)", sawWave1InBase, startedFromBase)
	}
	if got := gitOut(t, repo, "branch", "--show-current"); got != base {
		t.Errorf("repository must end on %q, got %q", base, got)
	}
	for _, f := range []string{"um.txt", "dois.txt", "tres.txt"} {
		if !baseHas(t, repo, base, f) {
			t.Errorf("%s is missing from the base branch", f)
		}
	}
	if len(run.Integrations) != 1 {
		t.Errorf("integrations = %v: only the parallel wave needs an integration branch", run.Integrations)
	}
}

func TestSequentialStoriesAreMergedIntoTheBaseOneByOne(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newParallelStoryTest(t, exec)
	base := initRepo(t, repo)
	var firstInBaseForSecond bool
	exec.onCall = func(req provider.Request) {
		if req.Role != "ra" {
			return
		}
		switch storyMarker(req.Prompt) {
		case "um":
			writeFile(t, repo, "um.txt", "um\n")
		case "dois":
			firstInBaseForSecond = baseHas(t, repo, base, "um.txt")
			writeFile(t, repo, "dois.txt", "dois\n")
		}
	}
	_ = o.Start(twoIndependent())
	if run := waitDone(t, o); run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if !firstInBaseForSecond {
		t.Error("the second story began before the first was merged into the base")
	}
	if !baseHas(t, repo, base, "dois.txt") || gitOut(t, repo, "branch", "--show-current") != base {
		t.Error("the last story must be merged and the repository left on the base")
	}
}

func TestSequentialPolicyRunsOneStoryAtATimeWithoutAnalysis(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{AnalysisStage.Role: {independentPair}}}
	o, repo := newParallelStoryTest(t, exec)
	policy := o.ws.Policy()
	policy.Sequential = true
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	base := initRepo(t, repo)
	var running, overlapped int32
	exec.onCall = func(req provider.Request) {
		if req.Role != "ra" {
			return
		}
		if atomic.AddInt32(&running, 1) > 1 {
			atomic.StoreInt32(&overlapped, 1)
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		if m := storyMarker(req.Prompt); m != "" {
			writeFile(t, repo, m+".txt", m+"\n")
		}
	}
	_ = o.Start(twoIndependent())
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if exec.count(AnalysisStage.Role) != 0 {
		t.Error("sequential development needs no dependency analysis")
	}
	if overlapped == 1 {
		t.Error("stories overlapped in sequential mode")
	}
	if got := fmt.Sprint(run.Waves); got != "[[0] [1]]" || len(run.Integrations) != 0 {
		t.Errorf("waves = %s, integrations = %v", got, run.Integrations)
	}
	if !baseHas(t, repo, base, "um.txt") || !baseHas(t, repo, base, "dois.txt") {
		t.Error("each story must still be merged into the base")
	}
}
