// Package eval benchmarks pipeline configurations: every arm (provider,
// models, best-of-N) develops every task from a clean copy of a template
// repository, and the results are scored with hidden checks the agents never
// see, test traceability and readability.
package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"llm-orchestrator/internal/pipeline"
	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/stories"
	"llm-orchestrator/internal/workspace"
)

// Suite is the eval definition file.
type Suite struct {
	Tasks []Task `json:"tasks"`
	Arms  []Arm  `json:"arms"`
	// Output is where workdirs, results.json and report.md are written.
	Output string `json:"output"`
	// DryRun simulates the agents; useful to validate a suite file.
	DryRun bool `json:"dry_run"`
}

// Task is one development exercise.
type Task struct {
	Name string `json:"name"`
	// Template is the starting repository, copied for each arm.
	Template string `json:"template"`
	// Story is the Markdown file (relative to Template) with the user stories.
	Story string `json:"story"`
	// Checks are given to the agents; empty uses the detected commands.
	Checks []quality.Command `json:"checks,omitempty"`
	// HiddenChecks run only after the pipeline, to score the result.
	HiddenChecks []quality.Command `json:"hidden_checks,omitempty"`
	TimeoutMin   int               `json:"timeout_min,omitempty"`
}

// Arm is one pipeline configuration under test.
type Arm struct {
	Name       string      `json:"name"`
	Provider   provider.ID `json:"provider"`
	HeavyModel string      `json:"heavy_model,omitempty"`
	StdModel   string      `json:"std_model,omitempty"`
	Effort     string      `json:"effort,omitempty"`
	Candidates int         `json:"candidates,omitempty"`
}

// Result is the measured outcome of one arm on one task.
type Result struct {
	Task         string  `json:"task"`
	Arm          string  `json:"arm"`
	Workdir      string  `json:"workdir"`
	Outcome      string  `json:"outcome"`
	DurationS    float64 `json:"duration_s"`
	AgentCalls   int     `json:"agent_calls"`
	FixSteps     int     `json:"fix_steps"`
	HiddenPassed int     `json:"hidden_passed"`
	HiddenTotal  int     `json:"hidden_total"`
	TestsFound   int     `json:"tests_found"`
	TestsTotal   int     `json:"tests_total"`
	Findings     int     `json:"findings"`
	Score        float64 `json:"score"`
	Error        string  `json:"error,omitempty"`
}

const defaultTimeout = 90 * time.Minute

// Load reads and validates a suite file. Relative paths are resolved against
// the file's directory.
func Load(path string) (Suite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, err
	}
	var s Suite
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Suite{}, fmt.Errorf("%s: %w", path, err)
	}
	base := filepath.Dir(path)
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	if s.Output == "" {
		s.Output = "eval-results"
	}
	s.Output = abs(s.Output)
	switch {
	case len(s.Tasks) == 0:
		return Suite{}, errors.New("a suíte não tem tarefas")
	case len(s.Arms) == 0:
		return Suite{}, errors.New("a suíte não tem braços (arms)")
	}
	for i := range s.Tasks {
		t := &s.Tasks[i]
		t.Template = abs(t.Template)
		if t.Name == "" || t.Template == "" || t.Story == "" {
			return Suite{}, fmt.Errorf("tarefa %d: name, template e story são obrigatórios", i+1)
		}
		if _, err := os.Stat(filepath.Join(t.Template, t.Story)); err != nil {
			return Suite{}, fmt.Errorf("tarefa %s: %w", t.Name, err)
		}
	}
	for i, a := range s.Arms {
		if a.Name == "" || a.Provider == "" {
			return Suite{}, fmt.Errorf("braço %d: name e provider são obrigatórios", i+1)
		}
	}
	return s, nil
}

// Run executes every arm on every task, sequentially, and writes the report.
func Run(ctx context.Context, s Suite, router *provider.Router, logger *slog.Logger) ([]Result, error) {
	stamp := time.Now().Format("20060102-150405")
	out := filepath.Join(s.Output, stamp)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	var results []Result
	for _, task := range s.Tasks {
		for _, arm := range s.Arms {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			logger.Info("eval: iniciando", "task", task.Name, "arm", arm.Name)
			r := runOne(ctx, s, task, arm, router, filepath.Join(out, task.Name+"__"+arm.Name), logger)
			logger.Info("eval: concluído", "task", task.Name, "arm", arm.Name, "outcome", r.Outcome, "score", r.Score)
			results = append(results, r)
		}
	}
	data, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "results.json"), data, 0o644); err != nil {
		return results, err
	}
	if err := os.WriteFile(filepath.Join(out, "report.md"), []byte(Report(results)), 0o644); err != nil {
		return results, err
	}
	logger.Info("eval: relatório salvo", "path", filepath.Join(out, "report.md"))
	return results, nil
}

func runOne(ctx context.Context, s Suite, task Task, arm Arm, router *provider.Router, dir string, logger *slog.Logger) Result {
	res := Result{Task: task.Name, Arm: arm.Name, Workdir: dir, Outcome: "error"}
	fail := func(err error) Result {
		res.Error = err.Error()
		return res
	}
	if err := copyTree(task.Template, dir); err != nil {
		return fail(fmt.Errorf("copiar template: %w", err))
	}
	if err := configure(router, arm); err != nil {
		return fail(err)
	}

	ws, err := workspace.NewManager(dir)
	if err != nil {
		return fail(err)
	}
	policy := workspace.DefaultPolicy()
	policy.Git = true
	if arm.Candidates > 0 {
		policy.Candidates = arm.Candidates
	}
	if len(task.Checks) > 0 {
		policy.AutoChecks, policy.Checks = false, task.Checks
	}
	if _, err := ws.SetPolicy(policy); err != nil {
		return fail(err)
	}

	text, err := os.ReadFile(filepath.Join(dir, task.Story))
	if err != nil {
		return fail(err)
	}
	doc := stories.Parse(string(text))
	if len(doc.Stories) == 0 {
		doc.Stories = []stories.Story{{ID: task.Name, Title: task.Name, Content: string(text)}}
	}

	orch := pipeline.New(router, ws, pipeline.DefaultStages, nil, logger)
	started := time.Now()
	if err := orch.Start(pipeline.StartOptions{
		Stories: doc.Stories, Preamble: doc.Preamble, Source: "Eval",
		DryRun: s.DryRun, DocumentName: task.Story,
	}); err != nil {
		return fail(err)
	}
	timeout := defaultTimeout
	if task.TimeoutMin > 0 {
		timeout = time.Duration(task.TimeoutMin) * time.Minute
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	if err := orch.Wait(waitCtx); err != nil {
		orch.Stop()
		_ = orch.Wait(context.Background())
		res.Error = "tempo limite excedido"
	}
	cancel()
	res.DurationS = time.Since(started).Seconds()

	run := orch.Snapshot()
	res.Outcome = string(run.Result)
	collect(&res, run)
	writeLog(filepath.Join(dir, ".faztudo-eval.log"), run)

	for _, c := range task.HiddenChecks {
		res.HiddenTotal++
		if r := quality.Run(ctx, dir, c, 10*time.Minute); r.Passed {
			res.HiddenPassed++
		}
	}
	res.Score = score(res)
	return res
}

// configure points the router at the arm's provider and models, with failover
// off so the arm measures what it names.
func configure(router *provider.Router, arm Arm) error {
	off := false
	cur := router.Config().Providers[arm.Provider]
	if arm.HeavyModel != "" {
		cur.HeavyModel = arm.HeavyModel
	}
	if arm.StdModel != "" {
		cur.StdModel = arm.StdModel
	}
	if arm.Effort != "" {
		cur.Effort = arm.Effort
	}
	err := router.UpdateConfig(provider.ConfigUpdate{
		DefaultProvider: arm.Provider,
		Failover:        &off,
		Providers:       map[provider.ID]provider.Settings{arm.Provider: cur},
	})
	if err != nil {
		return fmt.Errorf("braço %s: %w", arm.Name, err)
	}
	return nil
}

// collect reads process metrics from the final run snapshot.
func collect(res *Result, run pipeline.Run) {
	for _, st := range run.Stories {
		for _, step := range st.Steps {
			if step.Provider != "" {
				res.AgentCalls++
			}
			if step.Kind == "fix" && step.Status == pipeline.StepDone {
				res.FixSteps++
			}
		}
		for _, t := range st.Trace {
			res.TestsTotal++
			if t.Found() {
				res.TestsFound++
			}
		}
		// The last executed verification reflects the delivered code.
		for i := len(st.Steps) - 1; i >= 0; i-- {
			if s := st.Steps[i]; s.Status == pipeline.StepDone && (s.Checks != nil || s.Findings != nil) {
				res.Findings += len(s.Findings)
				break
			}
		}
	}
}

// score weighs, from 0 to 100: hidden checks 40, test traceability 25,
// pipeline success 20 and readability 15 (minus 1.5 per finding).
func score(r Result) float64 {
	ratio := func(a, b int) float64 {
		if b == 0 {
			return 1
		}
		return float64(a) / float64(b)
	}
	s := 40*ratio(r.HiddenPassed, r.HiddenTotal) + 25*ratio(r.TestsFound, r.TestsTotal)
	if r.Outcome == string(pipeline.ResultSucceeded) {
		s += 20
	}
	s += 15 * max(0, 1-float64(r.Findings)/10)
	return float64(int(s*10+0.5)) / 10
}

// Report renders the results as Markdown: one table per task plus the mean
// score of each arm.
func Report(results []Result) string {
	var b strings.Builder
	b.WriteString("# Relatório de avaliação do FazTudo\n\n")
	b.WriteString("Pontuação (0–100): checagens ocultas 40 · rastreabilidade de testes 25 · pipeline concluído 20 · legibilidade 15.\n\n")

	byTask := map[string][]Result{}
	var tasks []string
	sums := map[string][2]float64{}
	var arms []string
	for _, r := range results {
		if _, ok := byTask[r.Task]; !ok {
			tasks = append(tasks, r.Task)
		}
		byTask[r.Task] = append(byTask[r.Task], r)
		if _, ok := sums[r.Arm]; !ok {
			arms = append(arms, r.Arm)
		}
		v := sums[r.Arm]
		sums[r.Arm] = [2]float64{v[0] + r.Score, v[1] + 1}
	}

	b.WriteString("## Média por braço\n\n| Braço | Pontuação média | Tarefas |\n|---|---:|---:|\n")
	sort.SliceStable(arms, func(i, j int) bool {
		a, c := sums[arms[i]], sums[arms[j]]
		return a[0]/a[1] > c[0]/c[1]
	})
	for _, a := range arms {
		v := sums[a]
		fmt.Fprintf(&b, "| %s | %.1f | %d |\n", a, v[0]/v[1], int(v[1]))
	}

	for _, t := range tasks {
		fmt.Fprintf(&b, "\n## %s\n\n", t)
		b.WriteString("| Braço | Pontuação | Resultado | Checagens ocultas | Testes rastreados | Achados | Correções | Chamadas | Duração |\n")
		b.WriteString("|---|---:|---|---:|---:|---:|---:|---:|---:|\n")
		for _, r := range byTask[t] {
			outcome := r.Outcome
			if r.Error != "" {
				outcome += " (" + r.Error + ")"
			}
			fmt.Fprintf(&b, "| %s | %.1f | %s | %d/%d | %d/%d | %d | %d | %d | %s |\n",
				r.Arm, r.Score, outcome, r.HiddenPassed, r.HiddenTotal, r.TestsFound, r.TestsTotal,
				r.Findings, r.FixSteps, r.AgentCalls, (time.Duration(r.DurationS) * time.Second).String())
		}
	}
	return b.String()
}

func writeLog(path string, run pipeline.Run) {
	var b strings.Builder
	for _, l := range run.Logs {
		fmt.Fprintf(&b, "%s [%s] %s\n", l.Time.Format(time.TimeOnly), l.Level, l.Message)
	}
	_ = os.WriteFile(path, []byte(b.String()), 0o644)
}

// copyTree copies src to dst, skipping .git and linking dependency folders
// instead of copying them.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git":
				return filepath.SkipDir
			case "node_modules", ".venv", "venv":
				if path != src {
					if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
						return err
					}
					if err := os.Symlink(path, target); err != nil {
						return err
					}
					return filepath.SkipDir
				}
			}
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		info, _ := d.Info()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
