package workspace

import "testing"

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"**/auth/**", "api/src/auth/login.go", true},
		{"**/auth/**", "auth/login.go", true},
		{"**/auth/**", "api/author.go", false},
		{"**/*.sql", "db/migrations/001.sql", true},
		{"**/*.sql", "001.sql", true},
		{".github/workflows/**", ".github/workflows/ci.yml", true},
		{"**/Dockerfile", "Dockerfile", true},
		{"src/*.ts", "src/a/b.ts", false},
		{"src/*.ts", "src/b.ts", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.name); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v", c.pattern, c.name, got)
		}
	}
}

func TestAssessRiskFromStoryAndDiff(t *testing.T) {
	r := AssessRisk(DefaultRiskRules, "Como cliente quero ver meu perfil", []string{"src/profile.tsx"})
	if r.Level != RiskLow || len(r.Required) != 0 {
		t.Errorf("plain story = %+v", r)
	}

	r = AssessRisk(DefaultRiskRules, "Login com senha e MFA", nil)
	if r.Level != RiskHigh || len(r.Required) != 3 || r.Required[0] != "threat-model" || r.Rules[0] != "Autenticação e permissões" {
		t.Errorf("auth story = %+v", r)
	}

	diff := AssessRisk(DefaultRiskRules, "Mostrar pedidos", []string{"db/migrations/002_pedidos.sql"})
	if diff.Level != RiskHigh || len(diff.Reasons) != 1 || diff.Reasons[0] != "Persistência e migrations: db/migrations/002_pedidos.sql alterado" {
		t.Errorf("diff = %+v", diff)
	}

	merged := r.Merge(diff).Merge(AssessRisk(DefaultRiskRules, "pagamento via pix", nil))
	if merged.Level != RiskCritical || len(merged.Rules) != 3 {
		t.Errorf("merge keeps the highest level and every rule: %+v", merged)
	}
	if low := merged.Merge(Risk{Level: RiskLow}); low.Level != RiskCritical {
		t.Error("a later assessment never lowers the risk")
	}
}

func TestRiskRulesAreValidated(t *testing.T) {
	p := DefaultPolicy().Normalize()
	if len(p.RiskRules) != len(DefaultRiskRules) {
		t.Fatal("nil rules mean the defaults")
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.RiskRules = []RiskRule{{Name: "x", Level: "enorme", Keywords: []string{"y"}}}
	if p.Validate() == nil {
		t.Error("unknown level must be rejected")
	}
	p.RiskRules = []RiskRule{{Name: "x", Level: RiskHigh}}
	if p.Validate() == nil {
		t.Error("a rule that matches nothing must be rejected")
	}
	p.RiskRules = []RiskRule{}
	if p.Normalize().RiskRules == nil || len(p.Normalize().RiskRules) != 0 {
		t.Error("an empty list turns the rules off")
	}
}
