package pipeline

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// Plan is the architect's decision about which agents a story needs.
type Plan struct {
	Agents []string `json:"agents"`
	// Pattern is the design pattern the implementers follow: the project's
	// own or, without one, the architect's choice.
	Pattern string `json:"pattern,omitempty"`
	// Streams split the implementation into development fronts; fronts
	// without dependencies between them run in parallel.
	Streams []Stream `json:"streams,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	// Source tells how the plan was obtained: "architect", "heuristic"
	// (simulation) or "fallback" (architect output could not be parsed).
	Source string `json:"source"`
}

// Includes reports whether the plan selected the stage.
func (p Plan) Includes(id string) bool { return slices.Contains(p.Agents, id) }

// Issue is a problem a quality gate wants fixed, assigned to an implementer.
type Issue struct {
	Agent       string `json:"agent"`
	Severity    string `json:"severity,omitempty"`
	Description string `json:"description"`
	Location    string `json:"location,omitempty"`
	// Infra marks a failure no code change can fix, such as a command that ran
	// out of time: it goes to the report, never to an implementer.
	Infra bool `json:"infra,omitempty"`
	// Class, Fingerprints, Titles and Files identify the failure behind the
	// issue. Only the orchestrator's own checks set them.
	Class        string   `json:"class,omitempty"`
	Fingerprints []string `json:"fingerprints,omitempty"`
	Titles       []string `json:"titles,omitempty"`
	Files        []string `json:"files,omitempty"`
}

// Verdict is a quality gate's structured outcome.
type Verdict struct {
	Status string  `json:"status"` // approved | changes_requested
	Issues []Issue `json:"issues"`
}

const (
	VerdictApproved         = "approved"
	VerdictChangesRequested = "changes_requested"
)

// NeedsChanges reports whether the gate asked for fixes.
func (v Verdict) NeedsChanges() bool {
	return v.Status == VerdictChangesRequested && len(v.Issues) > 0
}

var reFence = regexp.MustCompile("(?s)```(?:json|JSON)?[ \\t]*\\n(.*?)```")

// lastJSON decodes the last fenced JSON block in text that satisfies ok.
func lastJSON[T any](text string, ok func(T) bool) (T, bool) {
	var zero T
	blocks := reFence.FindAllStringSubmatch(text, -1)
	for i := len(blocks) - 1; i >= 0; i-- {
		var v T
		if json.Unmarshal([]byte(blocks[i][1]), &v) == nil && ok(v) {
			return v, true
		}
	}
	return zero, false
}

// stripJSONBlocks removes fenced JSON blocks (the machine-readable verdict)
// from a report before it is shown to another agent.
func stripJSONBlocks(text string) string {
	return strings.TrimSpace(reFence.ReplaceAllString(text, ""))
}

// parsePlan extracts the architect's plan, keeping only agents the pipeline
// can delegate to and guaranteeing at least one implementer.
func parsePlan(output string, stages []Stage) (Plan, bool) {
	raw, ok := lastJSON(output, func(p Plan) bool { return p.Agents != nil })
	if !ok {
		return Plan{}, false
	}
	plan := Plan{Reason: strings.TrimSpace(raw.Reason), Pattern: strings.TrimSpace(raw.Pattern), Source: "architect"}
	plan.Streams = validStreams(raw.Streams, stages)
	requested := normalize(raw.Agents)
	for _, st := range plan.Streams {
		requested = append(requested, st.Agent)
	}
	for _, s := range stages {
		if s.Selectable && slices.Contains(requested, s.ID) {
			plan.Agents = append(plan.Agents, s.ID)
		}
	}
	plan = ensureImplementer(plan, stages)
	plan.Streams = coverAgents(plan.Streams, plan, stages)
	return plan, true
}

// parseVerdict extracts a gate verdict. Issues are reassigned to a valid
// implementer when the gate named an unknown or missing one.
func parseVerdict(output string, stages []Stage, plan Plan) (Verdict, bool) {
	v, ok := lastJSON(output, func(v Verdict) bool { return v.Status != "" })
	if !ok {
		return Verdict{}, false
	}
	v.Status = strings.ToLower(strings.TrimSpace(v.Status))
	var issues []Issue
	for _, is := range v.Issues {
		if strings.TrimSpace(is.Description) == "" {
			continue
		}
		is.Infra, is.Class, is.Fingerprints, is.Titles, is.Files = false, "", nil, nil, nil // not a model's to claim
		is.Agent = strings.ToLower(strings.TrimSpace(is.Agent))
		if !isImplementer(stages, is.Agent) {
			is.Agent = defaultImplementer(stages, plan)
		}
		issues = append(issues, is)
	}
	v.Issues = issues
	return v, true
}

// fullPlan selects every delegable agent.
func fullPlan(stages []Stage, source string) Plan {
	p := Plan{Source: source}
	for _, s := range stages {
		if s.Selectable {
			p.Agents = append(p.Agents, s.ID)
		}
	}
	return p
}

var heuristics = map[string][]string{
	"frontend": {"tela", "interface", "ui", "ux", "front", "componente", "página", "pagina", "layout", "botão", "formulário", "css", "react", "visual", "responsiv", "acessív"},
	"backend":  {"api", "endpoint", "banco", "backend", "serviço", "servico", "regra de negócio", "persist", "integração", "server", "jwt", "autentica", "dados", "fila"},
	"security": {"auth", "autentica", "senha", "token", "jwt", "permiss", "rbac", "mfa", "pagamento", "pii", "lgpd", "segurança", "owasp", "criptograf", "antifraude"},
	"e2e":      {"e2e", "playwright", "fluxo", "jornada", "ponta a ponta"},
	"docs":     {"documenta", "manual", "readme", "tutorial", "changelog", "guia de uso"},
}

// heuristicPlan approximates the architect in simulation mode, where no model
// produces a plan, so the selection logic can still be exercised.
func heuristicPlan(story string, stages []Stage) Plan {
	text := strings.ToLower(story)
	matches := func(id string) bool {
		return slices.ContainsFunc(heuristics[id], func(kw string) bool { return strings.Contains(text, kw) })
	}
	p := Plan{Source: "heuristic", Reason: "Simulação: agentes escolhidos por palavras-chave da User Story."}
	for _, s := range stages {
		if !s.Selectable {
			continue
		}
		if matches(s.ID) || (s.ID == "e2e" && matches("frontend")) {
			p.Agents = append(p.Agents, s.ID)
		}
	}
	p = ensureImplementer(p, stages)
	p.Streams = heuristicStreams(p, stages)
	return p
}

// heuristicStreams gives each selected implementer its own front, so the
// parallel flow is exercised in simulation when a story spans several.
func heuristicStreams(p Plan, stages []Stage) []Stream {
	var streams []Stream
	for _, s := range stages {
		if s.Implementer && p.Includes(s.ID) {
			streams = append(streams, Stream{ID: s.ID, Agent: s.ID, Title: s.Title})
		}
	}
	if len(streams) < 2 {
		return nil
	}
	return streams
}

func ensureImplementer(p Plan, stages []Stage) Plan {
	if slices.ContainsFunc(p.Agents, func(id string) bool { return isImplementer(stages, id) }) {
		return p
	}
	for _, s := range stages {
		if s.Implementer {
			p.Agents = append([]string{s.ID}, p.Agents...)
			if p.Reason != "" {
				p.Reason += " "
			}
			p.Reason += "(Nenhum implementador selecionado; " + s.Title + " incluído.)"
			break
		}
	}
	return p
}

func isImplementer(stages []Stage, id string) bool {
	return slices.ContainsFunc(stages, func(s Stage) bool { return s.Implementer && s.ID == id })
}

// defaultImplementer picks who fixes an unassigned issue: the last
// implementer the plan selected (backend before frontend when both are).
func defaultImplementer(stages []Stage, plan Plan) string {
	var last string
	for _, s := range stages {
		if s.Implementer && (plan.Includes(s.ID) || last == "") {
			last = s.ID
		}
	}
	return last
}

func normalize(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strings.ToLower(strings.TrimSpace(id))
	}
	return out
}

// Assessment is the Passo 0 decision about what the repository already does.
type Assessment struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// Passo 0 decisions.
const (
	DecisionReuse     = "reuse"     // already meets the acceptance criteria
	DecisionExtend    = "extend"    // partly there: implement only the delta
	DecisionRefactor  = "refactor"  // there, with an incompatible model
	DecisionImplement = "implement" // not there, dependencies ready
	DecisionBlock     = "block"     // missing dependency or open product decision
)

var decisions = []string{DecisionReuse, DecisionExtend, DecisionRefactor, DecisionImplement, DecisionBlock}

// parseAssessment extracts the Passo 0 decision; anything unreadable means
// implement, so a confused assessor never skips work silently.
func parseAssessment(output string) Assessment {
	a, ok := lastJSON(output, func(a Assessment) bool { return a.Decision != "" })
	if !ok {
		return Assessment{Decision: DecisionImplement}
	}
	a.Decision = strings.ToLower(strings.TrimSpace(a.Decision))
	a.Reason = strings.TrimSpace(a.Reason)
	if !slices.Contains(decisions, a.Decision) {
		a.Decision = DecisionImplement
	}
	return a
}
