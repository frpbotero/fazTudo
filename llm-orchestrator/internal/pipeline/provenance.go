package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/skills"
)

// overrideDir holds a project's complements to the bundled skills, one
// <skill>.md each. They are protected: no agent may rewrite the playbooks
// it is told to follow.
const overrideDir = ".faztudo/skills"

// overridePattern protects the complements like the input documents.
const overridePattern = overrideDir + "/*.md"

// SkillOverride is a project's complement to a bundled skill, with where it
// came from and its version.
type SkillOverride struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// projectOverrides finds the complements under repo for bundled skills;
// files naming no bundled skill are ignored.
func projectOverrides(repo string) map[string]SkillOverride {
	out := map[string]SkillOverride{}
	for _, name := range skills.Names() {
		rel := filepath.ToSlash(filepath.Join(overrideDir, name+".md"))
		data, err := os.ReadFile(filepath.Join(repo, rel))
		if err != nil || len(data) == 0 {
			continue
		}
		sum := sha256.Sum256(data)
		out[name] = SkillOverride{Path: rel, Hash: hex.EncodeToString(sum[:])}
	}
	return out
}

// CallRecord is what one agent call ran with: the bundle and each playbook's
// version, the prompts' hashes and the parameters, so an outcome can be
// traced to the exact instructions behind it.
type CallRecord struct {
	Bundle    string     `json:"bundle"`
	Skills    []SkillRef `json:"skills,omitempty"`
	Profiles  []SkillRef `json:"profiles,omitempty"`
	PromptSHA string     `json:"prompt_sha256"`
	SystemSHA string     `json:"system_sha256,omitempty"`
	Role      string     `json:"role"`
	Tier      string     `json:"tier,omitempty"`
	ReadOnly  bool       `json:"read_only,omitempty"`
	Diagnose  bool       `json:"diagnose,omitempty"`
	Strict    bool       `json:"strict_commands,omitempty"`
	MaxTurns  int        `json:"max_turns,omitempty"`
	Provider  string     `json:"provider,omitempty"`
	Model     string     `json:"model,omitempty"`
	// PromptBytes is the prompt's size; Truncated counts the artifacts cut
	// to fit the context budget.
	PromptBytes int `json:"prompt_bytes"`
	Truncated   int `json:"truncated,omitempty"`
}

// SkillRef is a playbook as a call read it: its bundled version and, when
// the project complements it, the complement's.
type SkillRef struct {
	Name         string `json:"name"`
	Hash         string `json:"hash"`
	Override     string `json:"override,omitempty"`
	OverrideHash string `json:"override_hash,omitempty"`
}

var (
	reSkillLine   = regexp.MustCompile("(?m)^- `([a-z0-9-]+)`: `[^`]+SKILL\\.md`$")
	reProfileLine = regexp.MustCompile("(?m)^- perfil de stack `([a-z0-9-]+)`: ")
	reOverride    = regexp.MustCompile("(?m)^  - complemento do projeto: `([^`]+)`$")
)

// callRecord reads the playbooks a request's prompt points to; the prompt
// is built by writeSkillsFor, so its lines are the record's source.
func callRecord(req provider.Request, resp *provider.Response, overrides map[string]SkillOverride) CallRecord {
	r := CallRecord{
		Bundle: skills.Version(), PromptSHA: sha(req.Prompt), Role: req.Role, Tier: string(req.Tier),
		ReadOnly: req.ReadOnly, Diagnose: req.Diagnose, Strict: req.StrictCommands, MaxTurns: req.MaxTurns,
	}
	if req.System != "" {
		r.SystemSHA = sha(req.System)
	}
	r.PromptBytes, r.Truncated = len(req.Prompt), strings.Count(req.Prompt, truncationMark)
	if resp != nil {
		r.Provider, r.Model = resp.Name, resp.Model
	}
	for _, m := range reSkillLine.FindAllStringSubmatch(req.Prompt, -1) {
		ref := SkillRef{Name: m[1], Hash: skills.Hash(m[1])}
		if o, ok := overrides[m[1]]; ok && slices.ContainsFunc(reOverride.FindAllStringSubmatch(req.Prompt, -1), func(x []string) bool { return x[1] == o.Path }) {
			ref.Override, ref.OverrideHash = o.Path, o.Hash
		}
		r.Skills = append(r.Skills, ref)
	}
	for _, m := range reProfileLine.FindAllStringSubmatch(req.Prompt, -1) {
		r.Profiles = append(r.Profiles, SkillRef{Name: m[1], Hash: skills.ProfileHash(m[1])})
	}
	return r
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// recordCall adds a call's record to its step.
func (o *Orchestrator) recordCall(ref stepRef, req provider.Request, resp *provider.Response) {
	rec := callRecord(o.limited(req), resp, o.overrides())
	o.updateStep(ref, func(s *Step) { s.Calls = append(s.Calls, rec) })
}

// overrides are the complements of the repository being run.
func (o *Orchestrator) overrides() map[string]SkillOverride {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.skillOverrides
}
