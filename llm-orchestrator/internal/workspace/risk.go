package workspace

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// RiskRule raises a story's risk when the story mentions one of Keywords or
// the change touches a file matching one of Paths, and makes the Require
// stages mandatory: no flow preset nor architect plan turns them off.
type RiskRule struct {
	Name string `json:"name"`
	// Paths are slash-separated globs over the changed files; "**" spans
	// directories (e.g. "**/auth/**", "**/*.sql").
	Paths []string `json:"paths,omitempty"`
	// Keywords are lower-case fragments looked up in the story text.
	Keywords []string `json:"keywords,omitempty"`
	Level    string   `json:"level"`
	// Require are the stage IDs that must run (e.g. "security", "review").
	Require []string `json:"require,omitempty"`
}

// Risk levels, from lowest to highest.
const (
	RiskLow      = "baixo"
	RiskMedium   = "medio"
	RiskHigh     = "alto"
	RiskCritical = "critico"
)

var riskLevels = []string{RiskLow, RiskMedium, RiskHigh, RiskCritical}

// RiskRank orders levels; unknown levels rank as high, so a typo never
// lowers a requirement.
func RiskRank(level string) int {
	if i := slices.Index(riskLevels, level); i >= 0 {
		return i
	}
	return slices.Index(riskLevels, RiskHigh)
}

// DefaultRiskRules are used when the policy sets none (nil); an empty list
// turns risk rules off.
var DefaultRiskRules = []RiskRule{
	{
		Name:     "Autenticação e permissões",
		Paths:    []string{"**/auth/**", "**/*auth*", "**/security/**", "**/permission*/**", "**/rbac/**", "**/*jwt*", "**/*session*"},
		Keywords: []string{"autentica", "login", "senha", "jwt", "permiss", "rbac", "mfa", "oauth", "autoriza"},
		Level:    RiskHigh,
		Require:  []string{"threat-model", "security", "review"},
	},
	{
		Name:     "Pagamento e dados pessoais",
		Keywords: []string{"pagamento", "cartão", "cartao", "pix", "boleto", "pii", "lgpd", "dados pessoais", "cpf"},
		Level:    RiskCritical,
		Require:  []string{"threat-model", "security", "review"},
	},
	{
		Name:     "Persistência e migrations",
		Paths:    []string{"**/migrations/**", "**/migration/**", "**/*.sql", "**/schema.prisma", "**/db/**"},
		Keywords: []string{"migration", "migração", "migracao", "schema do banco", "nova tabela", "nova coluna"},
		Level:    RiskHigh,
		Require:  []string{"data", "migrations", "review"},
	},
	{
		Name:    "Infraestrutura e CI",
		Paths:   []string{".github/workflows/**", "**/Dockerfile", "**/docker-compose*", "**/compose.y*ml", "**/*.tf", "**/k8s/**", "**/helm/**"},
		Level:   RiskHigh,
		Require: []string{"security", "review"},
	},
	{
		Name:     "Desempenho",
		Keywords: []string{"desempenho", "performance", "latência", "latencia", "throughput", "tempo de resposta", "slo", "p95", "p99"},
		Level:    RiskMedium,
		Require:  []string{"performance"},
	},
	{
		Name:     "Contratos de API",
		Paths:    []string{"**/openapi*", "**/swagger*", "**/*.proto", "**/*.graphql"},
		Keywords: []string{"endpoint", "api pública", "contrato da api"},
		Level:    RiskMedium,
		Require:  []string{"review"},
	},
}

// Risk is the rules a story triggers, with why.
type Risk struct {
	Level    string   `json:"level"`
	Rules    []string `json:"rules,omitempty"`
	Reasons  []string `json:"reasons,omitempty"`
	Required []string `json:"required,omitempty"`
}

// AssessRisk applies rules to a story's text and changed files. Without any
// rule fired the level is low and nothing extra is required.
func AssessRisk(rules []RiskRule, story string, changed []string) Risk {
	r := Risk{Level: RiskLow}
	text := strings.ToLower(story)
	for _, rule := range rules {
		var why []string
		for _, kw := range rule.Keywords {
			if kw = strings.ToLower(strings.TrimSpace(kw)); kw != "" && strings.Contains(text, kw) {
				why = append(why, fmt.Sprintf("“%s” na US", kw))
				break
			}
		}
		if f, ok := firstMatch(rule.Paths, changed); ok {
			why = append(why, f+" alterado")
		}
		if len(why) == 0 {
			continue
		}
		r.Rules = append(r.Rules, rule.Name)
		r.Reasons = append(r.Reasons, rule.Name+": "+strings.Join(why, ", "))
		if RiskRank(rule.Level) > RiskRank(r.Level) {
			r.Level = rule.Level
		}
		for _, id := range rule.Require {
			if id = strings.TrimSpace(id); id != "" && !slices.Contains(r.Required, id) {
				r.Required = append(r.Required, id)
			}
		}
	}
	return r
}

// Merge keeps the higher level and every rule and requirement of both: a
// later assessment (on the real diff) only adds to an earlier one.
func (r Risk) Merge(o Risk) Risk {
	out := Risk{Level: r.Level}
	if out.Level == "" || RiskRank(o.Level) > RiskRank(out.Level) {
		out.Level = o.Level
	}
	add := func(dst []string, src ...string) []string {
		for _, s := range src {
			if !slices.Contains(dst, s) {
				dst = append(dst, s)
			}
		}
		return dst
	}
	out.Rules = add(add(nil, r.Rules...), o.Rules...)
	out.Reasons = add(add(nil, r.Reasons...), o.Reasons...)
	out.Required = add(add(nil, r.Required...), o.Required...)
	return out
}

func firstMatch(patterns, files []string) (string, bool) {
	for _, f := range files {
		for _, p := range patterns {
			if matchGlob(p, f) {
				return f, true
			}
		}
	}
	return "", false
}

// matchGlob matches a slash path against a glob where "**" spans any number
// of directories and "*" stays within one.
func matchGlob(pattern, name string) bool {
	pattern = strings.TrimPrefix(strings.TrimSpace(pattern), "./")
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "**") {
		ok, _ := path.Match(pattern, name)
		return ok
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case strings.HasPrefix(pattern[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(name)
}

func validateRiskRules(rules []RiskRule) error {
	if len(rules) > maxRiskRules {
		return fmt.Errorf("no máximo %d regras de risco", maxRiskRules)
	}
	for i, r := range rules {
		switch {
		case strings.TrimSpace(r.Name) == "":
			return fmt.Errorf("regra de risco %d: nome obrigatório", i+1)
		case !slices.Contains(riskLevels, r.Level):
			return fmt.Errorf("regra de risco %s: nível %q inválido (aceitos: %s)", r.Name, r.Level, strings.Join(riskLevels, ", "))
		case len(r.Paths) == 0 && len(r.Keywords) == 0:
			return fmt.Errorf("regra de risco %s: informe caminhos ou palavras-chave", r.Name)
		case len(r.Paths) > maxPatterns || len(r.Keywords) > maxPatterns:
			return fmt.Errorf("regra de risco %s: no máximo %d caminhos e %d palavras", r.Name, maxPatterns, maxPatterns)
		}
	}
	return nil
}

const maxRiskRules = 30
