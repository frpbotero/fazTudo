package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

// Benchmark defaults.
const (
	defaultBenchRepeat     = 3
	defaultMaxRegression   = 10.0
	defaultMaxNoise        = 15.0
	minPercentileSamples   = 20 // fewer samples make p95 a guess
	benchVerdictPassed     = "aprovado"
	benchVerdictRegression = "regressão"
	benchVerdictUnclear    = "inconclusivo"
)

// Metric is one measured quantity, compared between baseline and candidate.
type Metric struct {
	Name      string  `json:"name"`
	Baseline  float64 `json:"baseline"`
	Candidate float64 `json:"candidate"`
	// ChangePct is the worsening in percent (negative when it improved);
	// NoisePct the larger spread of the two sides' runs.
	ChangePct float64 `json:"change_pct"`
	NoisePct  float64 `json:"noise_pct"`
	// HigherIsBetter tells throughput from latency, memory, CPU, queries.
	HigherIsBetter bool `json:"higher_is_better,omitempty"`
}

// BenchResult is a benchmark's comparison with the conditions it ran under.
type BenchResult struct {
	Name     string   `json:"name"`
	Workload string   `json:"workload"`
	Env      string   `json:"env"`
	Warmup   int      `json:"warmup"`
	Repeat   int      `json:"repeat"`
	Baseline string   `json:"baseline_commit,omitempty"`
	Metrics  []Metric `json:"metrics,omitempty"`
	Verdict  string   `json:"verdict"`
	Detail   string   `json:"detail,omitempty"`
	Required bool     `json:"required,omitempty"`
}

// measuredMetrics are the fields a benchmark may print, and which way is better.
var measuredMetrics = []struct {
	key    string
	higher bool
}{
	{"p50_ms", false}, {"p95_ms", false}, {"throughput_rps", true}, {"mem_mb", false}, {"cpu_pct", false}, {"queries", false},
}

// applicableBenchmarks keeps the policy's benchmarks for the stacks found.
func applicableBenchmarks(p workspace.Policy, report *workspace.Report) []workspace.Benchmark {
	var out []workspace.Benchmark
	for _, b := range p.Benchmarks {
		if b.Stack == "" || (report != nil && slices.ContainsFunc(report.Stacks, func(s workspace.Stack) bool { return strings.EqualFold(s.Name, b.Stack) })) {
			out = append(out, b)
		}
	}
	return out
}

// runBenchmarks measures each benchmark on the story's starting snapshot and
// on the candidate, alternating the two after the warm-up so both see the
// same machine. It returns false when ctx was canceled.
func (o *Orchestrator) runBenchmarks(ctx context.Context, ss *session, st *storyState) ([]BenchResult, bool) {
	list := applicableBenchmarks(ss.policy, ss.report)
	if len(list) == 0 || ss.dry {
		return nil, true
	}
	baseDir, cleanup, err := o.snapshotDir(ctx, ss, st.startCommit)
	if cleanup != nil {
		defer cleanup()
	}
	timeout := time.Duration(ss.policy.CheckTimeoutS) * time.Second
	env := fmt.Sprintf("%s/%s, %d CPUs", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	var results []BenchResult
	for _, b := range list {
		repeat := cmpInt(b.Repeat, defaultBenchRepeat)
		r := BenchResult{Name: b.Name, Workload: b.Workload, Env: env, Warmup: b.Warmup, Repeat: repeat, Baseline: st.startCommit, Required: b.Required}
		if err != nil {
			r.Verdict, r.Detail = benchVerdictUnclear, "sem linha de base para comparar: "+err.Error()
			results = append(results, r)
			continue
		}
		cmd := quality.Command{Name: b.Name, Run: b.Run, Resource: b.Resource}
		o.logf(LevelInfo, "Benchmark %s: %d aquecimento(s), %d medição(ões) de cada lado.", b.Name, b.Warmup, repeat)
		var base, cand []map[string]float64
		var failure string
		for i := 0; i < b.Warmup+repeat && failure == ""; i++ {
			for _, side := range []struct {
				dir  string
				into *[]map[string]float64
			}{{baseDir, &base}, {ss.repo, &cand}} {
				res := o.runGated(ctx, ss, side.dir, cmd, timeout)
				if ctx.Err() != nil {
					return nil, false
				}
				if !res.Passed {
					failure = fmt.Sprintf("o comando falhou (%s)", exitLabel(res))
					break
				}
				if i >= b.Warmup {
					*side.into = append(*side.into, parseMetrics(res.Output))
				}
			}
		}
		if failure != "" {
			r.Verdict, r.Detail = benchVerdictUnclear, failure
		} else {
			r.Metrics, r.Verdict, r.Detail = compareRuns(base, cand, b)
		}
		results = append(results, r)
	}
	return results, true
}

// parseMetrics reads the last JSON object line of a benchmark's output. A
// percentile from too few samples is dropped rather than trusted.
func parseMetrics(output string) map[string]float64 {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var raw map[string]float64
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		if n, ok := raw["samples"]; ok && n < minPercentileSamples {
			delete(raw, "p50_ms")
			delete(raw, "p95_ms")
		}
		return raw
	}
	return nil
}

// compareRuns takes each metric's median per side and judges the change.
// Too much spread, or a metric only one side has, proves nothing: the
// result is inconclusive rather than a precision the runs do not have.
func compareRuns(base, cand []map[string]float64, b workspace.Benchmark) ([]Metric, string, string) {
	maxReg := cmpFloat(b.MaxRegressionPct, defaultMaxRegression)
	maxNoise := cmpFloat(b.MaxNoisePct, defaultMaxNoise)
	var metrics []Metric
	verdict, notes := benchVerdictPassed, []string{}
	for _, m := range measuredMetrics {
		bv, bn, okB := medianOf(base, m.key)
		cv, cn, okC := medianOf(cand, m.key)
		if !okB || !okC {
			continue
		}
		metric := Metric{Name: m.key, Baseline: bv, Candidate: cv, NoisePct: math.Max(bn, cn), HigherIsBetter: m.higher}
		if bv != 0 {
			metric.ChangePct = (cv - bv) / bv * 100
			if m.higher {
				metric.ChangePct = -metric.ChangePct
			}
		}
		metrics = append(metrics, metric)
		switch {
		case metric.NoisePct > maxNoise:
			if verdict == benchVerdictPassed {
				verdict = benchVerdictUnclear
			}
			notes = append(notes, fmt.Sprintf("%s variou %.0f%% entre execuções (limite %.0f%%)", m.key, metric.NoisePct, maxNoise))
		case metric.ChangePct > maxReg:
			verdict = benchVerdictRegression
			notes = append(notes, fmt.Sprintf("%s piorou %.1f%% (limite %.0f%%)", m.key, metric.ChangePct, maxReg))
		}
	}
	if len(metrics) == 0 {
		return nil, benchVerdictUnclear, "nenhuma métrica lida dos dois lados (imprima uma linha JSON com p50_ms, p95_ms, throughput_rps…)"
	}
	return metrics, verdict, strings.Join(notes, "; ")
}

// medianOf is the median of key across runs and their spread, as the
// largest distance from the median in percent of it.
func medianOf(runs []map[string]float64, key string) (float64, float64, bool) {
	var vals []float64
	for _, r := range runs {
		if v, ok := r[key]; ok {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 || len(vals) < len(runs) {
		return 0, 0, false
	}
	slices.Sort(vals)
	med := vals[len(vals)/2]
	if len(vals)%2 == 0 {
		med = (vals[len(vals)/2-1] + vals[len(vals)/2]) / 2
	}
	spread := 0.0
	if med != 0 {
		for _, v := range vals {
			spread = math.Max(spread, math.Abs(v-med)/math.Abs(med)*100)
		}
	}
	return med, spread, true
}

// snapshotDir checks out commit in a temporary worktree, with the
// repository's dependencies linked in.
func (o *Orchestrator) snapshotDir(ctx context.Context, ss *session, commit string) (string, func(), error) {
	if ss.git == nil || commit == "" {
		return "", nil, fmt.Errorf("sem git ou sem commit de partida")
	}
	prefix, err := ss.git.Prefix(ctx)
	if err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp("", "faztudo-snapshot-")
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(tmp, "repo")
	if err := ss.git.AddWorktreeAt(ctx, dir, commit); err != nil {
		os.RemoveAll(tmp)
		return "", nil, fmt.Errorf("worktree do commit de partida: %w", err)
	}
	cleanup := func() {
		_ = ss.git.RemoveWorktree(context.WithoutCancel(ctx), dir)
		os.RemoveAll(tmp)
	}
	work := filepath.Join(dir, prefix)
	linkDependencies(ss.repo, work, ss.git.Nested())
	return work, cleanup, nil
}

// benchmarkEvidence renders the comparisons for the performance specialist.
func benchmarkEvidence(results []BenchResult) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Benchmarks: linha de base × candidato\n\n")
	for _, r := range results {
		fmt.Fprintf(&b, "### %s — %s\n\nWorkload: %s. Ambiente: %s. %d aquecimento(s), %d medição(ões) de cada lado, alternadas.\n\n",
			r.Name, r.Verdict, r.Workload, r.Env, r.Warmup, r.Repeat)
		for _, m := range r.Metrics {
			fmt.Fprintf(&b, "- %s: %.2f → %.2f (%+.1f%% de piora; ruído %.0f%%)\n", m.Name, m.Baseline, m.Candidate, m.ChangePct, m.NoisePct)
		}
		if r.Detail != "" {
			fmt.Fprintf(&b, "\n%s\n", r.Detail)
		}
		b.WriteString("\n")
	}
	b.WriteString("Proponha correção só com profiling ou medição que mostre o gargalo; a correção passa pelos testes do projeto.\n\n")
	return b.String()
}

// benchmarkIssues turns regressions of required benchmarks into blocking
// issues: the measurement decides, not the specialist's reading of it.
func (o *Orchestrator) benchmarkIssues(st *storyState, results []BenchResult) ([]Issue, bool) {
	var issues []Issue
	unclear := false
	for _, r := range results {
		switch {
		case !r.Required:
		case r.Verdict == benchVerdictRegression:
			issues = append(issues, Issue{
				Agent:        defaultImplementer(o.stages, st.plan),
				Severity:     "alta",
				Description:  fmt.Sprintf("Regressão de desempenho em %s (%s): %s.", r.Name, r.Workload, r.Detail),
				Location:     "desempenho",
				Fingerprints: []string{quality.TextFingerprint("benchmark", r.Name)},
				Titles:       []string{"Regressão de desempenho"},
			})
		case r.Verdict == benchVerdictUnclear:
			unclear = true
		}
	}
	return issues, unclear
}

func cmpInt(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func cmpFloat(v, def float64) float64 {
	if v > 0 {
		return v
	}
	return def
}
