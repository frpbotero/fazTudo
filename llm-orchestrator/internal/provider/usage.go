package provider

import (
	"bufio"
	"encoding/json"
	"errors"
	"strings"
)

// Usage is the token accounting a CLI reports for one run. InputTokens counts
// every prompt token, cached or not, so totals compare across CLIs; Claude
// reports fresh, cache-write and cache-read tokens apart and they are summed.
type Usage struct {
	InputTokens     int64   `json:"input_tokens"`
	CachedTokens    int64   `json:"cached_input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens,omitempty"`
	CostUSD         float64 `json:"cost_usd,omitempty"`
	Turns           int     `json:"turns,omitempty"`
}

// ErrTurnLimit means the agent used every turn it was given (--max-turns)
// before finishing. The response that comes with it holds the partial work.
var ErrTurnLimit = errors.New("limite de turnos atingido")

// parsed is a CLI's structured output reduced to the final text and usage.
type parsed struct {
	Text      string
	Usage     *Usage
	TurnLimit bool
}

// parseClaude reads the single result object of `claude -p --output-format
// json`. Stderr may be mixed into out on failures, so every line is tried.
func parseClaude(out string) (parsed, bool) {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var r struct {
			Type     string  `json:"type"`
			Subtype  string  `json:"subtype"`
			Result   string  `json:"result"`
			NumTurns int     `json:"num_turns"`
			Cost     float64 `json:"total_cost_usd"`
			Usage    struct {
				Input       int64 `json:"input_tokens"`
				CacheWrite  int64 `json:"cache_creation_input_tokens"`
				CacheRead   int64 `json:"cache_read_input_tokens"`
				OutputCount int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(line), &r) != nil || r.Type != "result" {
			continue
		}
		u := r.Usage
		return parsed{
			Text: r.Result,
			Usage: &Usage{
				InputTokens:  u.Input + u.CacheWrite + u.CacheRead,
				CachedTokens: u.CacheRead,
				OutputTokens: u.OutputCount,
				CostUSD:      r.Cost,
				Turns:        r.NumTurns,
			},
			TurnLimit: r.Subtype == "error_max_turns",
		}, true
	}
	return parsed{}, false
}

// parseCodex reads the JSONL events of `codex exec --json`: the last agent
// message is the answer and every completed turn adds to the usage.
func parseCodex(out string) (parsed, bool) {
	var p parsed
	found := false
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage struct {
				Input     int64 `json:"input_tokens"`
				Cached    int64 `json:"cached_input_tokens"`
				Output    int64 `json:"output_tokens"`
				Reasoning int64 `json:"reasoning_output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "item.completed" && ev.Item.Type == "agent_message":
			p.Text, found = ev.Item.Text, true
		case ev.Type == "turn.completed":
			if p.Usage == nil {
				p.Usage = &Usage{}
			}
			p.Usage.InputTokens += ev.Usage.Input
			p.Usage.CachedTokens += ev.Usage.Cached
			p.Usage.OutputTokens += ev.Usage.Output
			p.Usage.ReasoningTokens += ev.Usage.Reasoning
			p.Usage.Turns++
		}
	}
	return p, found
}

// parseGemini reads the object printed by `gemini -p --output-format json`.
// It is indented over many lines, so decoding starts at the first brace.
func parseGemini(out string) (parsed, bool) {
	start := strings.Index(out, "{")
	if start < 0 {
		return parsed{}, false
	}
	var r struct {
		Response *string `json:"response"`
		Stats    struct {
			Models map[string]struct {
				Tokens struct {
					Prompt     int64 `json:"prompt"`
					Candidates int64 `json:"candidates"`
					Cached     int64 `json:"cached"`
					Thoughts   int64 `json:"thoughts"`
				} `json:"tokens"`
			} `json:"models"`
		} `json:"stats"`
	}
	if json.NewDecoder(strings.NewReader(out[start:])).Decode(&r) != nil || r.Response == nil {
		return parsed{}, false
	}
	p := parsed{Text: *r.Response}
	if len(r.Stats.Models) > 0 {
		p.Usage = &Usage{}
		for _, m := range r.Stats.Models {
			p.Usage.InputTokens += m.Tokens.Prompt
			p.Usage.CachedTokens += m.Tokens.Cached
			p.Usage.OutputTokens += m.Tokens.Candidates
			p.Usage.ReasoningTokens += m.Tokens.Thoughts
		}
	}
	return p, true
}
