package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/quality"
	"llm-orchestrator/internal/skills"
	"llm-orchestrator/internal/vcs"
	"llm-orchestrator/internal/workspace"
)

// journalSchema is the version of the saved run. Journals before manifests
// carry none (zero): they still load, and a resume revalidates before any
// promotion since nothing tells what their evidence was produced with.
const journalSchema = 2

// RunManifest is what a run was made of: its inputs, the repositories'
// snapshots, the policy, the prompts and playbooks, and the tools. A resume
// compares it with the present to know which results still hold. It never
// holds secrets: only hashes, names and versions.
type RunManifest struct {
	Schema     int       `json:"schema"`
	CreatedAt  time.Time `json:"created_at"`
	AppVersion string    `json:"app_version,omitempty"`
	Env        string    `json:"env"`
	// Inputs hash the document's preamble and each story ("story:<i>").
	Inputs map[string]string `json:"inputs"`
	// Protected hash the input files guarded during the run.
	Protected map[string]string `json:"protected,omitempty"`
	// Heads are the repositories' commits (keyed as Run.Bases) at the start.
	Heads map[string]string `json:"heads,omitempty"`
	// Policy hashes the parts of the policy a result depends on, by part:
	// "checks", "flow", "risk", "other".
	Policy map[string]string `json:"policy"`
	// Stages hash each stage's instructions, playbooks included.
	Stages    map[string]string `json:"stages"`
	Bundle    string            `json:"bundle"`
	Profiles  []string          `json:"profiles,omitempty"`
	Overrides map[string]string `json:"overrides,omitempty"`
	// Providers are the CLIs' versions, by name, when known.
	Providers map[string]string `json:"providers,omitempty"`
}

// stateReporter is the router's view of the providers, for the manifest.
type stateReporter interface{ States() []provider.State }

// SetAppVersion records the app's version in every run's manifest.
func (o *Orchestrator) SetAppVersion(v string) {
	o.mu.Lock()
	o.appVersion = v
	o.mu.Unlock()
}

// buildManifest describes a run of opts against the repository as it is now.
func (o *Orchestrator) buildManifest(ctx context.Context, opts StartOptions) RunManifest {
	policy := o.ws.Policy()
	repo := o.ws.Path()
	o.mu.RLock()
	version := o.appVersion
	o.mu.RUnlock()
	m := RunManifest{
		Schema: journalSchema, CreatedAt: time.Now(), AppVersion: version, Env: runtime.GOOS + "/" + runtime.GOARCH,
		Inputs: map[string]string{"preamble": sha(opts.Preamble)},
		Policy: policyParts(policy, o.ws.Checks()), Stages: map[string]string{},
		Bundle: skills.Version(), Profiles: slices.Clone(policy.Profiles),
	}
	for i, s := range opts.Stories {
		m.Inputs[storyInput(i)] = sha(s.ID + "\x00" + s.Title + "\x00" + s.Content)
	}
	if guard := quality.NewGuard(repo, protectedPatterns(policy, opts)); guard != nil {
		for _, f := range guard.Files() {
			if data, err := os.ReadFile(filepath.Join(repo, f)); err == nil {
				if m.Protected == nil {
					m.Protected = map[string]string{}
				}
				m.Protected[f] = sha(string(data))
			}
		}
	}
	for name, ov := range projectOverrides(repo) {
		if m.Overrides == nil {
			m.Overrides = map[string]string{}
		}
		m.Overrides[name] = ov.Hash
	}
	o.mu.RLock()
	for _, s := range o.stages {
		m.Stages[s.ID] = stageHash(s, o.texts)
	}
	o.mu.RUnlock()
	if r, ok := o.exec.(stateReporter); ok {
		for _, st := range r.States() {
			if st.Version != "" {
				if m.Providers == nil {
					m.Providers = map[string]string{}
				}
				m.Providers[st.Name] = st.Version
			}
		}
	}
	if !opts.DryRun && policy.Git {
		if g, _, err := vcs.Open(ctx, repo); err == nil && g != nil {
			if head, err := g.Head(ctx); err == nil {
				m.Heads = map[string]string{".": head}
			}
		}
	}
	return m
}

func storyInput(i int) string { return fmt.Sprintf("story:%d", i) }

// policyParts hashes the policy by what depends on each part: the commands
// judge the verification, the flow and risk rules decide the stages.
func policyParts(p workspace.Policy, checks []quality.Command) map[string]string {
	h := func(v any) string {
		data, _ := json.Marshal(v)
		return sha(string(data))
	}
	flow := struct {
		Flow     string
		Disabled []string
	}{p.Flow, p.DisabledStages}
	commands := struct {
		Checks, Migration, Benchmark []quality.Command
		Timeout                      int
	}{checks, p.MigrationChecks, p.BenchmarkChecks, p.CheckTimeoutS}
	other := p
	other.Checks, other.MigrationChecks, other.BenchmarkChecks, other.CheckTimeoutS = nil, nil, nil, 0
	other.Flow, other.DisabledStages, other.RiskRules = "", nil, nil
	return map[string]string{"checks": h(commands), "flow": h(flow), "risk": h(p.RiskRules), "other": h(other)}
}

// stageHash identifies what a stage was told: its texts and its playbooks.
func stageHash(s Stage, texts promptTexts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\x00%s\x00%s\x00%s\x00", s.ID, s.System, s.Task, stageInstructions(s))
	for _, l := range s.Lenses {
		fmt.Fprintf(&b, "%s\x00%s\x00", l.ID, l.Focus)
		for _, n := range l.Skills {
			fmt.Fprintf(&b, "%s=%s\x00", n, skills.Hash(n))
		}
	}
	for _, n := range s.Skills {
		fmt.Fprintf(&b, "%s=%s\x00", n, skills.Hash(n))
	}
	if s.Implementer {
		b.WriteString(texts.fixTask)
	}
	if s.Gate {
		b.WriteString(texts.verifyTask)
	}
	return sha(b.String())
}

// Invalidation is a reason a result saved by a stopped run no longer holds,
// and the first stage that must run again because of it (0: all of them).
type Invalidation struct {
	Reason string `json:"reason"`
	// From is the stage index to redo from; Stage names it.
	From  int    `json:"-"`
	Stage string `json:"stage"`
}

// invalidations compares the saved manifest of the run with the present one
// for story si. Relations the orchestrator knows invalidate from the stage
// that depends on what changed; anything else redoes the story from the
// start. A run saved without a manifest is revalidated from its first
// executable check.
func (o *Orchestrator) invalidations(saved *RunManifest, cur RunManifest, si int) []Invalidation {
	verifierAt := slices.IndexFunc(o.stages, func(s Stage) bool { return s.Verifier })
	var out []Invalidation
	add := func(from int, reason string) {
		if from < 0 {
			return // a flow without executable checks has nothing to revalidate
		}
		out = append(out, Invalidation{Reason: reason, From: from, Stage: o.stages[from].ID})
	}
	if saved == nil || saved.Schema < journalSchema {
		add(verifierAt, "execução salva por uma versão anterior, sem manifesto: as verificações rodam de novo antes de promover")
		return out
	}
	if saved.Inputs[storyInput(si)] != cur.Inputs[storyInput(si)] {
		add(0, "o texto da US mudou")
	}
	if saved.Inputs["preamble"] != cur.Inputs["preamble"] {
		add(0, "o documento de entrada mudou")
	}
	if changed := changedKeys(saved.Protected, cur.Protected); len(changed) > 0 {
		add(0, "arquivo de entrada protegido mudou ("+strings.Join(changed, ", ")+")")
	}
	for _, part := range []string{"flow", "risk", "other"} {
		if saved.Policy[part] != cur.Policy[part] {
			add(0, "a política mudou ("+policyPartLabel(part)+")")
		}
	}
	if saved.Policy["checks"] != cur.Policy["checks"] {
		add(verifierAt, "os comandos de verificação mudaram")
	}
	if saved.Bundle != cur.Bundle || !slices.Equal(saved.Profiles, cur.Profiles) || changedKeys(saved.Overrides, cur.Overrides) != nil {
		// Each stage's hash covers its playbooks: the stages below say where.
		if saved.Bundle != cur.Bundle && len(saved.Stages) == 0 {
			add(0, "o conjunto de skills mudou")
		}
		if !slices.Equal(saved.Profiles, cur.Profiles) || changedKeys(saved.Overrides, cur.Overrides) != nil {
			add(0, "perfis de stack ou complementos do projeto mudaram")
		}
	}
	for i, s := range o.stages {
		if old, ok := saved.Stages[s.ID]; ok && old != cur.Stages[s.ID] {
			add(i, "as instruções de "+s.Title+" mudaram")
			break // the first changed stage is where the redo starts
		}
	}
	return out
}

func policyPartLabel(part string) string {
	switch part {
	case "flow":
		return "fluxo de agentes"
	case "risk":
		return "regras de risco"
	}
	return "demais opções"
}

func changedKeys(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if b[k] != v {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// earliest is where a redo starts given the invalidations.
func earliest(stageAt int, invs []Invalidation) int {
	for _, inv := range invs {
		stageAt = min(stageAt, inv.From)
	}
	return stageAt
}

// branchMoved tells whether the story's branch holds commits the run did not
// make: the code its saved results judged is not the code there now.
func branchMoved(ctx context.Context, repo string, r StoryRun) (string, bool) {
	if r.Branch == "" || r.Head == "" {
		return "", false
	}
	g, _, err := vcs.Open(ctx, repo)
	if err != nil || g == nil {
		return "", false
	}
	now, err := g.Rev(ctx, r.Branch)
	switch {
	case err != nil:
		return "a branch " + r.Branch + " não existe mais", true
	case now == r.Head:
		return "", false
	case !g.IsAncestor(ctx, r.Head, now):
		return "a branch " + r.Branch + " foi reescrita fora da execução", true
	}
	// The run's own commits (work saved on an interruption) all say so.
	subjects, err := g.Subjects(ctx, r.Head, now)
	if err != nil || slices.ContainsFunc(subjects, func(s string) bool { return !strings.HasPrefix(s, "faztudo") }) {
		return "a branch " + r.Branch + " recebeu commits fora da execução", true
	}
	return "", false
}

// Operation is a side effect on the repositories that must happen once, such
// as a merge into the base or a pull request: its key is stable across
// interruptions, so a resumed run finds it done instead of repeating it.
type Operation struct {
	Key    string    `json:"key"`
	Kind   string    `json:"kind"`
	Target string    `json:"target"`
	Result string    `json:"result,omitempty"`
	At     time.Time `json:"at"`
}

func operationKey(kind string, parts ...string) string {
	return sha(kind + "\x00" + strings.Join(parts, "\x00"))
}

// operationDone returns the recorded operation with key, if any.
func (o *Orchestrator) operationDone(key string) (Operation, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	i := slices.IndexFunc(o.run.Operations, func(op Operation) bool { return op.Key == key })
	if i < 0 {
		return Operation{}, false
	}
	return o.run.Operations[i], true
}

func (o *Orchestrator) recordOperation(op Operation) {
	op.At = time.Now()
	o.mu.Lock()
	o.run.Operations = append(o.run.Operations, op)
	o.mu.Unlock()
	o.notify()
}
