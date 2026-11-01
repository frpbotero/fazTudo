package pipeline

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/workspace"
)

// Turn limits per agent call when the policy sets none. Writing stages need
// room to edit and check; read-only ones only read and answer.
const (
	defaultWriteTurns = 60
	defaultReadTurns  = 30
	minReadTurns      = 10
)

// agentLimits are the per-call bounds and permissions of the current run.
type agentLimits struct {
	writeTurns int
	readTurns  int
	stop       bool     // a turn limit ends the run instead of leaving a pendency
	commands   []string // shell commands writing agents may run
}

// useLimits derives the agent limits of a run from its policy: the project
// checks, the tools to reproduce a failure and the policy's extra commands are
// the only shell writers may run.
func (o *Orchestrator) useLimits(policy workspace.Policy, checks []quality.Command) {
	l := agentLimits{writeTurns: defaultWriteTurns, readTurns: defaultReadTurns, stop: policy.StopOnTurnLimit}
	if policy.MaxTurns > 0 {
		l.writeTurns, l.readTurns = policy.MaxTurns, max(policy.MaxTurns/2, minReadTurns)
	}
	l.commands = append(quality.RepairCommands(checks), policy.AgentCommands...)
	o.mu.Lock()
	o.limits = l
	o.mu.Unlock()
}

// limited applies the run's limits to a request.
func (o *Orchestrator) limited(req provider.Request) provider.Request {
	o.mu.RLock()
	l := o.limits
	o.mu.RUnlock()
	if req.ReadOnly {
		req.MaxTurns = l.readTurns
		return req
	}
	req.MaxTurns, req.Commands = l.writeTurns, l.commands
	return req
}

// turnLimited keeps the partial work of an agent that ran out of turns: the
// step becomes a pendency and the run goes on, unless the policy stops it.
func (o *Orchestrator) turnLimited(ref stepRef, req provider.Request, resp *provider.Response, err error) (*provider.Response, error) {
	o.mu.RLock()
	stop := o.limits.stop
	o.mu.RUnlock()
	if stop || resp == nil {
		return nil, fmt.Errorf("%w (%d turnos)", err, req.MaxTurns)
	}
	o.logf(LevelWarn, "%s esgotou o limite de %d turnos; o trabalho parcial segue e fica como pendência.", o.stepTitle(ref), req.MaxTurns)
	return resp, nil
}

// markTurnLimited flags a step whose agent stopped at the turn limit.
func (o *Orchestrator) markTurnLimited(ref stepRef, resp *provider.Response, turns int) {
	if !resp.TurnLimited {
		return
	}
	o.updateStep(ref, func(s *Step) {
		s.Status = StepWarning
		s.Note = "Limite de turnos atingido"
		s.Error = fmt.Sprintf("O agente esgotou o limite de %d turnos antes de terminar; revise o trabalho parcial.", turns)
	})
}

// UsageRecord is one agent call in the token log (tokens.jsonl).
type UsageRecord struct {
	Time        time.Time   `json:"time"`
	Run         string      `json:"run"` // when the run started, RFC 3339
	Repo        string      `json:"repo"`
	Story       string      `json:"story,omitempty"`
	Task        string      `json:"task"` // step ID
	Role        string      `json:"role"`
	Attempt     int         `json:"attempt"`
	Agent       provider.ID `json:"agent"`
	Model       string      `json:"model"`
	Effort      string      `json:"effort,omitempty"`
	Reported    bool        `json:"usage_reported"` // false: the CLI gave no token count
	TurnLimited bool        `json:"turn_limited,omitempty"`
	DurationMS  int64       `json:"duration_ms"`
	provider.Usage
}

// SetUsageLog appends a record per real agent call to path; "" disables it.
func (o *Orchestrator) SetUsageLog(path string) {
	o.usageMu.Lock()
	o.usagePath = path
	o.usageMu.Unlock()
}

// recordUsage logs a finished agent call. Simulated and failed calls carry
// no usage and are left out.
func (o *Orchestrator) recordUsage(ref stepRef, req provider.Request, resp *provider.Response, attempt int) {
	if resp == nil || resp.Simulated {
		return
	}
	o.mu.RLock()
	rec := UsageRecord{
		Time: time.Now(), Repo: o.run.Repository, Task: ref.id, Role: req.Role, Attempt: attempt,
		Agent: resp.Provider, Model: resp.Model, Effort: resp.Effort,
		TurnLimited: resp.TurnLimited, DurationMS: resp.Duration.Milliseconds(),
	}
	if o.run.StartedAt != nil {
		rec.Run = o.run.StartedAt.Format(time.RFC3339)
	}
	if ref.story >= 0 && ref.story < len(o.run.Stories) {
		rec.Story = o.run.Stories[ref.story].ID
	}
	o.mu.RUnlock()
	if resp.Usage != nil {
		rec.Reported, rec.Usage = true, *resp.Usage
	}

	o.usageMu.Lock()
	defer o.usageMu.Unlock()
	if o.usagePath == "" {
		return
	}
	if err := appendJSONLine(o.usagePath, rec); err != nil {
		o.logger.Warn("token log not written", "path", o.usagePath, "err", err)
	}
}

func appendJSONLine(path string, v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}

// usageTotal sums the calls of one run.
type usageTotal struct {
	run, repo  string
	calls      int
	unreported int
	provider.Usage
}

func (t *usageTotal) add(r UsageRecord) {
	t.calls++
	if !r.Reported {
		t.unreported++
	}
	t.InputTokens += r.InputTokens
	t.CachedTokens += r.CachedTokens
	t.OutputTokens += r.OutputTokens
	t.ReasoningTokens += r.ReasoningTokens
	t.CostUSD += r.CostUSD
}

// WriteUsageSummary prints the token log as one line per run, in log order,
// plus the overall total: what `faztudo -tokens` shows.
func WriteUsageSummary(log io.Reader, w io.Writer) error {
	var order []string
	runs := map[string]*usageTotal{}
	all := usageTotal{run: "TOTAL"}
	sc := bufio.NewScanner(log)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var r UsageRecord
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		t := runs[r.Run]
		if t == nil {
			t = &usageTotal{run: r.Run, repo: filepath.Base(r.Repo)}
			runs[r.Run] = t
			order = append(order, r.Run)
		}
		t.add(r)
		all.add(r)
	}
	if err := sc.Err(); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "execução\trepositório\tchamadas\tentrada\tcache\tsaída\tcusto (US$)\t")
	for _, id := range order {
		writeUsageRow(tw, runs[id])
	}
	writeUsageRow(tw, &all)
	if err := tw.Flush(); err != nil {
		return err
	}
	if all.unreported > 0 {
		fmt.Fprintf(w, "\n%d chamada(s) sem contagem de tokens informada pelo CLI.\n", all.unreported)
	}
	return nil
}

func writeUsageRow(w io.Writer, t *usageTotal) {
	fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%.4f\t\n",
		t.run, t.repo, t.calls, t.InputTokens, t.CachedTokens, t.OutputTokens+t.ReasoningTokens, t.CostUSD)
}
