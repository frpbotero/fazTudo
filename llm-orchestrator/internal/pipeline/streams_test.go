package pipeline

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
)

func streamIDs(streams []Stream) string {
	var ids []string
	for _, s := range streams {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, ",")
}

func waveIDs(waves [][]Stream) string {
	var out []string
	for _, w := range waves {
		out = append(out, streamIDs(w))
	}
	return strings.Join(out, " | ")
}

func TestParsePlanStreams(t *testing.T) {
	plan, ok := parsePlan(fence(`{"agents": ["frontend"], "pattern": " Clean Architecture ", "streams": [
		{"id": "API Carrinho", "agent": "Backend", "title": "API", "files": ["./src/cart/", " "], "depends_on": ["api-carrinho", "nada"]},
		{"id": "api carrinho", "agent": "frontend", "depends_on": ["API Carrinho"]},
		{"id": "x", "agent": "security"}]}`), DefaultStages)
	if !ok {
		t.Fatal("plan must parse")
	}
	if plan.Pattern != "Clean Architecture" || strings.Join(plan.Agents, ",") != "frontend,backend" {
		t.Fatalf("plan = %+v", plan)
	}
	if streamIDs(plan.Streams) != "api-carrinho,api-carrinho-2" {
		t.Fatalf("streams = %+v", plan.Streams)
	}
	api, ui := plan.Streams[0], plan.Streams[1]
	if len(api.DependsOn) != 0 || strings.Join(api.Files, ",") != "src/cart/" {
		t.Errorf("self and unknown dependencies and blank files must go: %+v", api)
	}
	if strings.Join(ui.DependsOn, ",") != "api-carrinho" || ui.Title != "api-carrinho-2" {
		t.Errorf("ui stream = %+v", ui)
	}
}

func TestParsePlanGivesUncoveredAgentsAFront(t *testing.T) {
	plan, _ := parsePlan(fence(`{"agents": ["frontend", "backend"], "streams": [
		{"id": "a", "agent": "backend"}, {"id": "b", "agent": "backend"}]}`), DefaultStages)
	if streamIDs(plan.Streams) != "a,b,frontend" || plan.Streams[2].Agent != "frontend" {
		t.Fatalf("streams = %+v", plan.Streams)
	}
}

func TestStreamWaves(t *testing.T) {
	waves := streamWaves([]Stream{
		{ID: "api", Files: []string{"src/api"}},
		{ID: "ui", Files: []string{"web/"}},
		{ID: "api-test", Files: []string{"src/api/cart.go"}},
		{ID: "e2e", DependsOn: []string{"ui", "api"}},
	})
	if got := waveIDs(waves); got != "api,ui | api-test,e2e" {
		t.Errorf("waves = %s", got)
	}

	cycle := streamWaves([]Stream{{ID: "a", DependsOn: []string{"b"}}, {ID: "b", DependsOn: []string{"a"}}})
	if got := waveIDs(cycle); got != "a | b" {
		t.Errorf("cycle waves = %s", got)
	}

	chain := Plan{Streams: []Stream{{ID: "a"}, {ID: "b", DependsOn: []string{"a"}}}}
	if chain.parallelWaves() != nil {
		t.Error("a chain has nothing to run in parallel")
	}
}

func TestHeuristicPlanSplitsFrontAndBack(t *testing.T) {
	p := heuristicPlan("Nova tela de checkout com endpoint de pagamento", DefaultStages)
	if streamIDs(p.Streams) != "frontend,backend" || p.parallelWaves() == nil {
		t.Errorf("streams = %+v", p.Streams)
	}
	if p = heuristicPlan("Ajustar o layout da tela", DefaultStages); p.Streams != nil {
		t.Errorf("one implementer needs no fronts: %+v", p.Streams)
	}
}

var streamStages = []Stage{
	{ID: "arch", Title: "Arch", Role: "arch", Planner: true, ReadOnly: true},
	{ID: "frontend", Title: "Front", Role: "fe", Selectable: true, Implementer: true},
	{ID: "backend", Title: "Back", Role: "be", Selectable: true, Implementer: true},
}

const twoStreams = "```json\n" + `{"agents": ["frontend", "backend"], "streams": [
	{"id": "ui", "agent": "frontend", "title": "Tela", "files": ["web/"]},
	{"id": "api", "agent": "backend", "title": "API", "files": ["src/"]}]}` + "\n```"

func newStreamTest(t *testing.T, exec *fakeExecutor, git bool) (*Orchestrator, string) {
	t.Helper()
	if git {
		if _, err := osexec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	}
	exec.outputs = map[string][]string{"arch": {twoStreams}}
	o, repo := newVerifierTest(t, exec, func(int) quality.Result { return quality.Result{Passed: true} })
	o.baseStages = streamStages
	o.stages = o.baseStages
	policy := o.ws.Policy()
	policy.Git = git
	policy.PromoteUnverified = true // these stages judge nothing; the tests are about the fronts
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "README.md", "loja")
	return o, repo
}

func isStreamRole(role string) bool { return strings.Contains(role, " · ") }

func TestParallelStreamsRunInWorktreesAndMerge(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newStreamTest(t, exec, true)
	var started sync.WaitGroup
	started.Add(2)
	exec.onCall = func(req provider.Request) {
		if !isStreamRole(req.Role) {
			return
		}
		started.Done()
		if !waitTimeout(&started, time.Second) {
			t.Error("fronts of the same wave must run in parallel")
		}
		_, front, _ := strings.Cut(req.Role, "· ")
		writeFile(t, req.WorkDir, strings.ToLower(front)+".txt", req.Role)
	}

	_ = o.Start(realStory("x"))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	steps := run.Stories[0].Steps
	if got := stepIDs(steps); got != "arch:done stream-ui:done stream-api:done frontend:omitted backend:omitted" {
		t.Fatalf("steps = %s", got)
	}
	for _, name := range []string{"tela.txt", "api.txt"} {
		if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
			t.Errorf("%s must be merged into the repository: %v", name, err)
		}
	}
	for _, s := range steps[1:3] {
		if s.Commit == nil || s.Kind != "stream" || s.Agent == "" {
			t.Errorf("stream step = %+v", s)
		}
	}
	for _, r := range exec.reqs {
		if isStreamRole(r.Role) && r.WorkDir == repo {
			t.Errorf("%s must run in its own worktree", r.Role)
		}
		if r.Role == "fe · Tela" && !strings.Contains(r.Prompt, `outro agente faz "API"`) {
			t.Error("a front must know what runs beside it")
		}
	}
	if out, _ := osexec.Command("git", "-C", repo, "worktree", "list").Output(); strings.Count(string(out), "\n") != 1 {
		t.Errorf("worktrees must be removed:\n%s", out)
	}
}

func TestConflictingStreamIsRedoneOnTheMergedCode(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newStreamTest(t, exec, true)
	exec.onCall = func(req provider.Request) {
		if isStreamRole(req.Role) {
			writeFile(t, req.WorkDir, "shared.txt", req.Role+"\n")
		}
	}

	_ = o.Start(realStory("x"))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if exec.count("be · API") != 2 {
		t.Fatalf("the conflicting front must be redone, calls = %v", exec.roles)
	}
	last := exec.reqs[len(exec.reqs)-1]
	if last.WorkDir != repo || !strings.Contains(last.Prompt, "[CONFLITO]") {
		t.Errorf("redo must run in the repository with the conflict note: %+v", last.WorkDir)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "shared.txt")); string(data) != "be · API\n" {
		t.Errorf("shared.txt = %q", data)
	}
}

func TestStreamsRunInSequenceWithoutGit(t *testing.T) {
	exec := &fakeExecutor{}
	o, repo := newStreamTest(t, exec, false)
	_ = o.Start(realStory("x"))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if exec.count("fe · Tela") != 1 || exec.count("be · API") != 1 || exec.count("fe") != 0 {
		t.Fatalf("calls = %v", exec.roles)
	}
	for _, r := range exec.reqs {
		if r.WorkDir != repo {
			t.Errorf("without git every front runs in the repository, got %s", r.WorkDir)
		}
	}
}

func TestRetryFailedStreamRestartsTheFronts(t *testing.T) {
	exec := &fakeExecutor{failAt: 4} // discovery, architect, ui, api
	o, _ := newStreamTest(t, exec, false)
	o.attempts = 1
	_ = o.Start(realStory("x"))
	run := waitDone(t, o)
	if run.Result != ResultFailed {
		t.Fatalf("result = %s", run.Result)
	}
	if got := stepIDs(run.Stories[0].Steps); !strings.Contains(got, "stream-api:failed") {
		t.Fatalf("steps = %s", got)
	}
	if err := o.RetryFailed(0, "stream-api"); err != nil {
		t.Fatal(err)
	}
	run = waitDone(t, o)
	if run.Result != ResultSucceeded || exec.count("be · API") != 2 {
		t.Fatalf("result = %s, calls = %v", run.Result, exec.roles)
	}
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestSequentialPolicyDropsParallelFronts(t *testing.T) {
	exec := &fakeExecutor{}
	o, _ := newStreamTest(t, exec, true)
	policy := o.ws.Policy()
	policy.Sequential = true
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	_ = o.Start(realStory("x"))
	run := waitDone(t, o)
	if run.Result != ResultSucceeded {
		t.Fatalf("result = %s, logs: %+v", run.Result, run.Logs)
	}
	if exec.count("fe · Tela") != 0 || exec.count("be · API") != 0 {
		t.Errorf("sequential development must not split the plan into fronts: %v", exec.roles)
	}
	if exec.count("fe") != 1 || exec.count("be") != 1 {
		t.Errorf("the implementers run as regular stages, one after the other: %v", exec.roles)
	}
}
