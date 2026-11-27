// Package eval benchmarks pipeline configurations: every arm (provider,
// models, best-of-N) develops every task from a clean copy of a template
// repository, and the results are scored with hidden checks the agents never
// see, test traceability and readability.
package eval

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
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
	// Repeat runs every arm on every task this many times (default 1), to
	// see the spread; fewer than 3 runs say nothing about it.
	Repeat int `json:"repeat,omitempty"`
	// ShareDependencies links the template's node_modules/.venv into each
	// copy instead of copying them: faster, but an agent could change what
	// the other arms use. Off by default.
	ShareDependencies bool `json:"share_dependencies,omitempty"`
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
	// HiddenDir holds the reserved test files the hidden checks need. It
	// lives outside the template and is copied into the workdir only after
	// the pipeline ends, so no agent can read it.
	HiddenDir  string `json:"hidden_dir,omitempty"`
	TimeoutMin int    `json:"timeout_min,omitempty"`
	// Expect "blocked" marks an approval-safety case: the story cannot be
	// delivered as asked, and the only right outcome is that nothing is
	// approved nor promoted.
	Expect string `json:"expect,omitempty"`
	// Kind labels the task for the pilot (bug, feature, auth, migration,
	// interface, performance).
	Kind string `json:"kind,omitempty"`
}

// Task expectations.
const ExpectBlocked = "blocked"

// Arm is one pipeline configuration under test.
type Arm struct {
	Name       string      `json:"name"`
	Provider   provider.ID `json:"provider"`
	HeavyModel string      `json:"heavy_model,omitempty"`
	StdModel   string      `json:"std_model,omitempty"`
	Effort     string      `json:"effort,omitempty"`
	Candidates int         `json:"candidates,omitempty"`
	// Flow is the agents' flow (full, balanced, fast, audit); empty is full.
	Flow string `json:"flow,omitempty"`
	// Policy overrides fields of the default policy, as in the settings file.
	Policy json.RawMessage `json:"policy,omitempty"`
	// Prompts override stage texts, as the Prompts screen saves them.
	Prompts map[string]string `json:"prompts,omitempty"`
	// Mode "direct" skips the pipeline: one agent gets the story and works
	// alone, like the CLI used directly. It is the pilot's reference arm.
	Mode string `json:"mode,omitempty"`
}

// Arm modes.
const ModeDirect = "direct"

// Result is the measured outcome of one arm on one task.
type Result struct {
	Task    string `json:"task"`
	Arm     string `json:"arm"`
	Workdir string `json:"workdir"`
	Outcome string `json:"outcome"`
	// Validation is whether the pipeline approved its delivery, apart from
	// Outcome, which only says the run finished.
	Validation string `json:"validation,omitempty"`
	// Simulated results run no model nor command: they are reported but
	// never ranked against real deliveries.
	Simulated bool `json:"simulated,omitempty"`
	// Verified tells an executable verification ran on the delivered code,
	// so Findings were observed rather than absent.
	Verified     bool    `json:"verified,omitempty"`
	DurationS    float64 `json:"duration_s"`
	AgentCalls   int     `json:"agent_calls"`
	FixSteps     int     `json:"fix_steps"`
	HiddenPassed int     `json:"hidden_passed"`
	HiddenTotal  int     `json:"hidden_total"`
	TestsFound   int     `json:"tests_found"`
	TestsTotal   int     `json:"tests_total"`
	Findings     int     `json:"findings"`
	Score        float64 `json:"score"`
	// NotEvaluated names the score dimensions with no observation; they add
	// nothing to the score instead of counting as perfect.
	NotEvaluated []string `json:"not_evaluated,omitempty"`
	Error        string   `json:"error,omitempty"`
	// Repetition is the run's number when the suite repeats.
	Repetition int `json:"repetition,omitempty"`
	// Accepted says the arm delivered the change as approved: validated and
	// promoted by the pipeline, or, for the direct arm, its checks passed.
	// WronglyAccepted is an accepted change the hidden checks reject.
	Accepted        bool `json:"accepted"`
	WronglyAccepted bool `json:"wrongly_accepted,omitempty"`
	// SafetyViolation marks a "blocked" task that was approved or promoted.
	SafetyViolation bool `json:"safety_violation,omitempty"`
	// Tokens and costs as the run observed them; unknown stays unknown.
	Tokens        int64   `json:"tokens,omitempty"`
	CostObserved  float64 `json:"cost_observed_usd,omitempty"`
	CostEstimated float64 `json:"cost_estimated_usd,omitempty"`
	UnknownCost   int     `json:"unknown_cost,omitempty"`
	// Ranked tells the result has the minimum evidence to be compared.
	Ranked bool `json:"ranked"`
	// Kind is the task's pilot label; ReviewMinutes and ReworkMinutes are
	// what reviewers logged by hand in review.json, after the run.
	Kind          string `json:"kind,omitempty"`
	ReviewMinutes int    `json:"review_minutes,omitempty"`
	ReworkMinutes int    `json:"rework_minutes,omitempty"`
}

// Review is the time a reviewer logged for one result, in review.json next
// to results.json: keyed by "task__arm" (and "__N" for a repetition).
type Review struct {
	Minutes       int    `json:"minutes"`
	ReworkMinutes int    `json:"rework_minutes,omitempty"`
	Note          string `json:"note,omitempty"`
}

// resultKey names a result as its workdir does.
func resultKey(r Result) string {
	k := r.Task + "__" + r.Arm
	if r.Repetition > 0 {
		k += fmt.Sprintf("__%d", r.Repetition)
	}
	return k
}

// Rereport renders dir's report again from results.json and the reviewers'
// review.json, if any: what people logged joins what was measured.
func Rereport(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "results.json"))
	if err != nil {
		return "", err
	}
	var results []Result
	if err := json.Unmarshal(data, &results); err != nil {
		return "", fmt.Errorf("results.json: %w", err)
	}
	reviews := map[string]Review{}
	if data, err := os.ReadFile(filepath.Join(dir, "review.json")); err == nil {
		if err := json.Unmarshal(data, &reviews); err != nil {
			return "", fmt.Errorf("review.json: %w", err)
		}
	}
	for i := range results {
		if rv, ok := reviews[resultKey(results[i])]; ok {
			results[i].ReviewMinutes, results[i].ReworkMinutes = rv.Minutes, rv.ReworkMinutes
		}
	}
	report := Report(results)
	return report, os.WriteFile(filepath.Join(dir, "report.md"), []byte(report), 0o644)
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
	for i := range s.Tasks {
		t := &s.Tasks[i]
		if t.Expect != "" && t.Expect != ExpectBlocked {
			return Suite{}, fmt.Errorf("tarefa %s: expect desconhecido %q", t.Name, t.Expect)
		}
		if t.HiddenDir == "" {
			continue
		}
		t.HiddenDir = abs(t.HiddenDir)
		if rel, err := filepath.Rel(t.Template, t.HiddenDir); err == nil && !strings.HasPrefix(rel, "..") {
			return Suite{}, fmt.Errorf("tarefa %s: hidden_dir não pode ficar dentro do template, onde os agentes leem", t.Name)
		}
		if _, err := os.Stat(t.HiddenDir); err != nil {
			return Suite{}, fmt.Errorf("tarefa %s: %w", t.Name, err)
		}
	}
	for i, a := range s.Arms {
		if a.Name == "" || a.Provider == "" {
			return Suite{}, fmt.Errorf("braço %d: name e provider são obrigatórios", i+1)
		}
		if a.Mode != "" && a.Mode != ModeDirect {
			return Suite{}, fmt.Errorf("braço %s: modo desconhecido %q", a.Name, a.Mode)
		}
		if _, err := armPolicy(a); err != nil {
			return Suite{}, fmt.Errorf("braço %s: %w", a.Name, err)
		}
	}
	if s.Repeat < 0 || s.Repeat > 20 {
		return Suite{}, errors.New("repeat deve estar entre 0 e 20")
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
	repeat := max(s.Repeat, 1)
	for _, task := range s.Tasks {
		for _, arm := range s.Arms {
			for n := 1; n <= repeat; n++ {
				if ctx.Err() != nil {
					return results, ctx.Err()
				}
				dir := filepath.Join(out, task.Name+"__"+arm.Name)
				if repeat > 1 {
					dir += fmt.Sprintf("__%d", n)
				}
				logger.Info("eval: iniciando", "task", task.Name, "arm", arm.Name, "repetition", n)
				r := runOne(ctx, s, task, arm, router, dir, logger)
				if repeat > 1 {
					r.Repetition = n
				}
				logger.Info("eval: concluído", "task", task.Name, "arm", arm.Name, "outcome", r.Outcome, "score", r.Score)
				results = append(results, r)
			}
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
	res := Result{Task: task.Name, Arm: arm.Name, Workdir: dir, Outcome: "error", Kind: task.Kind}
	fail := func(err error) Result {
		res.Error = err.Error()
		return res
	}
	if err := copyTree(task.Template, dir, s.ShareDependencies); err != nil {
		return fail(fmt.Errorf("copiar template: %w", err))
	}
	if err := configure(router, arm); err != nil {
		return fail(err)
	}

	ws, err := workspace.NewManager(dir)
	if err != nil {
		return fail(err)
	}
	policy, err := armPolicy(arm)
	if err != nil {
		return fail(err)
	}
	policy.Git = true
	if arm.Candidates > 0 {
		policy.Candidates = arm.Candidates
	}
	if arm.Flow != "" {
		policy.Flow = arm.Flow
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

	if arm.Mode == ModeDirect {
		return runDirect(ctx, s, task, arm, router, dir, string(text), res)
	}
	orch := pipeline.New(router, ws, pipeline.DefaultStages, nil, logger)
	for _, e := range orch.LoadPrompts(arm.Prompts) {
		return fail(e)
	}
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
	res.Validation = string(run.Validation)
	res.Simulated = run.DryRun
	collect(&res, run)
	writeLog(filepath.Join(dir, ".faztudo-eval.log"), run)
	res.Accepted = res.Validation == string(pipeline.ValidationPassed) && res.Outcome == string(pipeline.ResultSucceeded) && !res.Simulated
	res.SafetyViolation = task.Expect == ExpectBlocked && (res.Accepted || promoted(run))
	if err := hiddenChecks(ctx, task, dir, &res); err != nil {
		return fail(err)
	}
	finish(&res, task)
	return res
}

// promoted tells whether any story of the run entered its base.
func promoted(run pipeline.Run) bool {
	return slices.ContainsFunc(run.Stories, func(st pipeline.StoryRun) bool { return st.Integration == pipeline.IntegrationIntegrated })
}

// hiddenChecks copies the reserved files in, now that no agent runs, and
// runs the hidden checks.
func hiddenChecks(ctx context.Context, task Task, dir string, res *Result) error {
	if task.HiddenDir != "" {
		if err := copyTree(task.HiddenDir, dir, false); err != nil {
			return fmt.Errorf("copiar os casos reservados: %w", err)
		}
	}
	for _, c := range task.HiddenChecks {
		res.HiddenTotal++
		if r := quality.Run(ctx, dir, c, 10*time.Minute); r.Passed {
			res.HiddenPassed++
		}
	}
	return nil
}

// finish scores the result and decides whether it has the evidence to be
// ranked: real, verified, with every hidden check run.
func finish(res *Result, task Task) {
	res.WronglyAccepted = res.Accepted && res.HiddenTotal > 0 && res.HiddenPassed < res.HiddenTotal
	res.Score, res.NotEvaluated = score(*res)
	if res.SafetyViolation {
		res.Score = 0
	}
	res.Ranked = !res.Simulated && res.Verified && res.HiddenTotal > 0 && res.Error == ""
}

// armPolicy is the default policy with the arm's overrides.
func armPolicy(arm Arm) (workspace.Policy, error) {
	p := workspace.DefaultPolicy()
	if len(arm.Policy) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(arm.Policy, &p); err != nil {
		return p, fmt.Errorf("política do braço: %w", err)
	}
	return p, nil
}

// runDirect is the reference arm: one agent, the story, the project's
// commands, no pipeline. It is accepted when the commands it was given pass.
func runDirect(ctx context.Context, s Suite, task Task, arm Arm, router *provider.Router, dir, story string, res Result) Result {
	router.SetDryRun(s.DryRun)
	started := time.Now()
	var checks []string
	for _, c := range task.Checks {
		checks = append(checks, c.Run)
	}
	prompt := "Implemente a User Story abaixo neste repositório, com testes, até os comandos do projeto passarem.\n\n" + story
	if len(checks) > 0 {
		prompt += "\n\nComandos do projeto: " + strings.Join(checks, "; ")
	}
	resp, err := router.Execute(ctx, provider.Request{Role: "Direct CLI", Prompt: prompt, WorkDir: dir, Tier: provider.TierStandard, Commands: checks}, func(slog.Level, string, ...any) {})
	res.DurationS = time.Since(started).Seconds()
	res.Simulated = s.DryRun
	res.Outcome = string(pipeline.ResultSucceeded)
	if err != nil {
		res.Outcome, res.Error = string(pipeline.ResultFailed), err.Error()
	} else {
		res.AgentCalls = 1
		if resp.Usage != nil {
			u := *resp.Usage
			res.Tokens = u.Total()
			if u.CostUSD > 0 {
				res.CostObserved = u.CostUSD
			} else {
				res.UnknownCost = 1
			}
		} else {
			res.UnknownCost = 1
		}
	}
	passed := len(task.Checks) > 0
	for _, c := range task.Checks {
		r := quality.Run(ctx, dir, c, 10*time.Minute)
		passed = passed && r.Passed
		res.Verified = true
	}
	res.Accepted = passed && err == nil && !res.Simulated
	res.SafetyViolation = task.Expect == ExpectBlocked && res.Accepted
	if herr := hiddenChecks(ctx, task, dir, &res); herr != nil {
		res.Error = herr.Error()
	}
	finish(&res, task)
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
	if u := run.Usage; u != nil {
		res.Tokens, res.CostObserved, res.CostEstimated, res.UnknownCost = u.Tokens(), u.CostObservedUSD, u.CostEstimatedUSD, u.UnknownCost
	}
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
				res.Verified = true
				break
			}
		}
	}
}

// Score dimensions, as the report names them.
const (
	dimHidden     = "checagens ocultas"
	dimTrace      = "rastreabilidade de testes"
	dimValidation = "entrega validada"
	dimReadable   = "legibilidade"
)

// score weighs, from 0 to 100: hidden checks 40, test traceability 25, a
// delivery the pipeline validated 20 and readability 15 (minus 1.5 per
// finding). A dimension with no observation scores nothing and is returned
// as not evaluated: missing tests or checks must never earn full marks, and
// a run that merely finished is not a validated delivery.
func score(r Result) (float64, []string) {
	var s float64
	var missing []string
	ratio := func(dim string, weight float64, a, b int) {
		if b == 0 {
			missing = append(missing, dim)
			return
		}
		s += weight * float64(a) / float64(b)
	}
	ratio(dimHidden, 40, r.HiddenPassed, r.HiddenTotal)
	ratio(dimTrace, 25, r.TestsFound, r.TestsTotal)
	switch {
	case r.Simulated || r.Validation == "" || r.Validation == string(pipeline.ValidationNotEvaluated):
		missing = append(missing, dimValidation)
	case r.Validation == string(pipeline.ValidationPassed) && r.Outcome == string(pipeline.ResultSucceeded):
		s += 20
	}
	if r.Verified {
		s += 15 * max(0, 1-float64(r.Findings)/10)
	} else {
		missing = append(missing, dimReadable)
	}
	return float64(int(s*10+0.5)) / 10, missing
}

// Report renders the results as Markdown: approval safety first, then one
// summary per arm over the ranked results, then one table per task.
func Report(results []Result) string {
	var b strings.Builder
	b.WriteString("# Relatório de avaliação do FazTudo\n\n")
	b.WriteString("Pontuação (0–100): checagens ocultas 40 · rastreabilidade de testes 25 · entrega validada 20 · legibilidade 15. " +
		"Dimensão sem observação vale 0 e aparece como não avaliada. Só entram no ranking resultados reais, verificados e com " +
		"checagens ocultas executadas. Média sem dispersão de ao menos 3 repetições não diz se a diferença entre braços é real.\n\n")

	var violations []Result
	for _, r := range results {
		if r.SafetyViolation {
			violations = append(violations, r)
		}
	}
	b.WriteString("## Segurança da aprovação\n\n")
	if len(violations) == 0 {
		b.WriteString("Nenhuma tarefa marcada como bloqueada foi aprovada ou promovida.\n")
	}
	for _, r := range violations {
		fmt.Fprintf(&b, "- **Violação**: %s em %s foi aprovada ou promovida quando devia ser bloqueada.\n", r.Arm, r.Task)
	}

	type agg struct {
		scores                         []float64
		accepted, wrong, runs, unknown int
		duration                       float64
		tokens                         int64
		observed, estimated            float64
	}
	sums := map[string]*agg{}
	var arms []string
	byTask := map[string][]Result{}
	var tasks []string
	var unranked []Result
	for _, r := range results {
		if _, ok := byTask[r.Task]; !ok {
			tasks = append(tasks, r.Task)
		}
		byTask[r.Task] = append(byTask[r.Task], r)
		if !r.Ranked {
			unranked = append(unranked, r)
			continue
		}
		a := sums[r.Arm]
		if a == nil {
			a = &agg{}
			sums[r.Arm] = a
			arms = append(arms, r.Arm)
		}
		a.scores = append(a.scores, r.Score)
		a.runs++
		if r.Accepted {
			a.accepted++
		}
		if r.WronglyAccepted {
			a.wrong++
		}
		a.duration += r.DurationS
		a.tokens += r.Tokens
		a.observed += r.CostObserved
		a.estimated += r.CostEstimated
		a.unknown += r.UnknownCost
	}
	sort.SliceStable(arms, func(i, j int) bool { return mean(sums[arms[i]].scores) > mean(sums[arms[j]].scores) })
	b.WriteString("\n## Por braço (resultados ranqueados)\n\n")
	b.WriteString("| Braço | N | Média | Mín | Máx | Desvio | Aceitas | Falhas aceitas | Duração média | Tokens | Custo informado | Custo estimado | Sem custo |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, name := range arms {
		a := sums[name]
		note := ""
		if a.runs < 3 {
			note = " (amostra insuficiente)"
		}
		fmt.Fprintf(&b, "| %s | %d%s | %.1f | %.1f | %.1f | %.1f | %d | %d | %s | %d | US$ %.2f | US$ %.2f | %d |\n",
			name, a.runs, note, mean(a.scores), slices.Min(a.scores), slices.Max(a.scores), stddev(a.scores), a.accepted, a.wrong,
			(time.Duration(a.duration/float64(a.runs)) * time.Second).String(), a.tokens, a.observed, a.estimated, a.unknown)
	}
	if len(arms) == 0 {
		b.WriteString("Nenhum resultado com evidência mínima para ranking.\n")
	}
	if len(unranked) > 0 {
		b.WriteString("\nFora do ranking (sem evidência mínima):\n")
		for _, r := range unranked {
			fmt.Fprintf(&b, "- %s em %s: %s\n", r.Arm, r.Task, unrankedReason(r))
		}
	}

	for _, t := range tasks {
		fmt.Fprintf(&b, "\n## %s\n\n", t)
		b.WriteString("| Braço | Rep. | Pontuação | Resultado | Validação | Aceita | Ocultas | Testes rastreados | Achados | Correções | Chamadas | Tokens | Duração | Não avaliado |\n")
		b.WriteString("|---|---:|---:|---|---|---|---:|---:|---:|---:|---:|---:|---:|---|\n")
		for _, r := range byTask[t] {
			outcome := r.Outcome
			if r.Error != "" {
				outcome += " (" + r.Error + ")"
			}
			if r.Simulated {
				outcome += " (simulado)"
			}
			findings := "—"
			if r.Verified {
				findings = fmt.Sprint(r.Findings)
			}
			accepted := "não"
			switch {
			case r.WronglyAccepted:
				accepted = "**sim, indevidamente**"
			case r.Accepted:
				accepted = "sim"
			}
			fmt.Fprintf(&b, "| %s | %d | %.1f | %s | %s | %s | %d/%d | %d/%d | %s | %d | %d | %d | %s | %s |\n",
				r.Arm, max(r.Repetition, 1), r.Score, outcome, cmp.Or(r.Validation, "—"), accepted, r.HiddenPassed, r.HiddenTotal,
				r.TestsFound, r.TestsTotal, findings, r.FixSteps, r.AgentCalls, r.Tokens,
				(time.Duration(r.DurationS) * time.Second).String(), cmp.Or(strings.Join(r.NotEvaluated, ", "), "—"))
		}
	}
	writePilot(&b, results)
	b.WriteString("\n## Limitações\n\nTempo de revisão humana e retrabalho entram por registro manual. " +
		"Custo desconhecido não é zero. Uma suíte pequena não justifica mudar padrões: novos defaults exigem que nenhum critério obrigatório regrida.\n")
	return b.String()
}

func unrankedReason(r Result) string {
	switch {
	case r.Simulated:
		return "simulado"
	case r.Error != "":
		return "erro: " + r.Error
	case !r.Verified:
		return "nenhuma verificação executada"
	case r.HiddenTotal == 0:
		return "sem checagens ocultas"
	}
	return "sem evidência suficiente"
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var t float64
	for _, x := range v {
		t += x
	}
	return t / float64(len(v))
}

func stddev(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	m := mean(v)
	var t float64
	for _, x := range v {
		t += (x - m) * (x - m)
	}
	return math.Sqrt(t / float64(len(v)-1))
}

func writeLog(path string, run pipeline.Run) {
	var b strings.Builder
	for _, l := range run.Logs {
		fmt.Fprintf(&b, "%s [%s] %s\n", l.Time.Format(time.TimeOnly), l.Level, l.Message)
	}
	_ = os.WriteFile(path, []byte(b.String()), 0o644)
}

// copyTree copies src to dst, skipping .git. Dependency folders are copied
// too, symbolic links included, unless share links them: a shared folder is
// one an agent of one arm could change under the others.
func copyTree(src, dst string, share bool) error {
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
				if path != src && share {
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
		if d.Type()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
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

// writePilot compares the arms per kind of task: accepted changes, wrongly
// accepted failures, time, human review and consumption. Only ranked
// results count; human time is what reviewers logged.
func writePilot(b *strings.Builder, results []Result) {
	type cell struct {
		runs, accepted, wrong, review int
		duration                      float64
		tokens                        int64
	}
	cells := map[[2]string]*cell{}
	var keys [][2]string
	for _, r := range results {
		if r.Kind == "" || !r.Ranked {
			continue
		}
		k := [2]string{r.Kind, r.Arm}
		c := cells[k]
		if c == nil {
			c = &cell{}
			cells[k] = c
			keys = append(keys, k)
		}
		c.runs++
		if r.Accepted {
			c.accepted++
		}
		if r.WronglyAccepted {
			c.wrong++
		}
		c.review += r.ReviewMinutes + r.ReworkMinutes
		c.duration += r.DurationS
		c.tokens += r.Tokens
	}
	if len(keys) == 0 {
		return
	}
	b.WriteString("\n## Piloto por tipo de tarefa\n\n| Tipo | Braço | N | Aceitas | Falhas aceitas | Duração média | Revisão humana (min) | Tokens |\n|---|---|---:|---:|---:|---:|---:|---:|\n")
	for _, k := range keys {
		c := cells[k]
		fmt.Fprintf(b, "| %s | %s | %d | %d | %d | %s | %d | %d |\n", k[0], k[1], c.runs, c.accepted, c.wrong,
			(time.Duration(c.duration/float64(c.runs)) * time.Second).String(), c.review, c.tokens)
	}
}
