package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

func runs(key string, vals ...float64) []map[string]float64 {
	var out []map[string]float64
	for _, v := range vals {
		out = append(out, map[string]float64{key: v})
	}
	return out
}

func TestCompareRunsJudgesOnlyWhatTheMeasurementSupports(t *testing.T) {
	b := workspace.Benchmark{Name: "api"}
	if _, v, d := compareRuns(runs("p95_ms", 100, 101, 99), runs("p95_ms", 130, 131, 129), b); v != benchVerdictRegression || !strings.Contains(d, "p95_ms") {
		t.Errorf("30%% slower = %s (%s)", v, d)
	}
	if _, v, _ := compareRuns(runs("p95_ms", 100, 101, 99), runs("p95_ms", 104, 105, 103), b); v != benchVerdictPassed {
		t.Errorf("4%% slower within the limit = %s", v)
	}
	if _, v, d := compareRuns(runs("p95_ms", 100, 160, 60), runs("p95_ms", 130, 131, 129), b); v != benchVerdictUnclear || !strings.Contains(d, "variou") {
		t.Errorf("noisy baseline = %s (%s)", v, d)
	}
	m, v, _ := compareRuns(runs("throughput_rps", 1000, 1000, 1000), runs("throughput_rps", 800, 800, 800), b)
	if v != benchVerdictRegression || m[0].ChangePct != 20 {
		t.Errorf("throughput down 20%% = %s %+v", v, m)
	}
	if _, v, _ := compareRuns(runs("p95_ms", 100), runs("mem_mb", 50), b); v != benchVerdictUnclear {
		t.Errorf("no metric on both sides = %s", v)
	}
	if got := parseMetrics("log\n{\"p95_ms\": 12, \"samples\": 5, \"mem_mb\": 40}\n"); got["p95_ms"] != 0 || got["mem_mb"] != 40 {
		t.Errorf("a p95 from 5 samples must be dropped: %v", got)
	}
}

func TestRequiredBenchmarkRegressionBlocksAndTheFixIsMeasuredAgain(t *testing.T) {
	exec := &fakeExecutor{outputs: map[string][]string{
		"perf": {fence(`{"status":"approved","issues":[],"measurements":[]}`), fence(`{"status":"approved","issues":[],"measurements":[]}`)},
	}}
	o, _ := newParallelStoryTest(t, exec)
	o.baseStages = []Stage{
		{ID: "arch", Title: "Arq", Role: "architect", Planner: true},
		{ID: "a", Title: "A", Role: "ra", Implementer: true},
		{ID: "performance", Title: "Desempenho", Role: "perf", Selectable: true, Conditional: true, Gate: true, Measured: true, ReadOnly: true},
	}
	o.stages = o.baseStages
	policy := o.ws.Policy()
	policy.Benchmarks = []workspace.Benchmark{{Name: "pedidos", Run: "make bench", Workload: "1000 pedidos, 10 clientes", Repeat: 2, Required: true}}
	if _, err := o.ws.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	fixed := false
	exec.onCall = func(req provider.Request) {
		if req.Role == "ra" {
			if strings.Contains(req.Prompt, "Regressão de desempenho") {
				fixed = true
			}
			writeFile(t, req.WorkDir, "a.txt", fmt.Sprint(time.Now().UnixNano()))
		}
	}
	o.runCheck = func(_ context.Context, dir string, c quality.Command, _ time.Duration) quality.Result {
		p95 := 100
		if exists(dir, "a.txt") && !fixed {
			p95 = 150 // the candidate is slower until the fix
		}
		return quality.Result{Name: c.Name, Command: c.Run, Passed: true, Output: fmt.Sprintf(`{"p95_ms": %d}`, p95)}
	}
	_ = o.Start(StartOptions{Stories: threeStories[:1], Source: "Markdown"})
	run := waitDone(t, o)
	st := run.Stories[0]
	perf := step(st.Steps, "performance")
	if len(perf.Issues) != 1 || !strings.Contains(perf.Issues[0].Description, "Regressão") {
		t.Fatalf("performance step = %s %+v, logs %+v", perf.Status, perf.Issues, run.Logs)
	}
	if !fixed {
		t.Fatal("the regression did not go back to the implementer")
	}
	if len(st.Performance) != 1 || st.Performance[0].Verdict != benchVerdictPassed || st.Performance[0].Baseline == "" {
		t.Errorf("after the fix, measured again: %+v", st.Performance)
	}
	if st.Validation != ValidationPassed {
		t.Errorf("validation = %s", st.Validation)
	}
}
