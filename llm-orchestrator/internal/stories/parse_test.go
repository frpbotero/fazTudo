package stories

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseMultipleStoriesWithPreamble(t *testing.T) {
	md := "# Épico: Pagamentos\n\nContexto comum a todas as US.\n\n" +
		"## US-101: Checkout\nComo cliente…\n\n### Critérios\n- a\n\n" +
		"## US-102 — Estorno\nComo operador…\n\n" +
		"```md\n## US-999 dentro de código\n```\n\n" +
		"## HU 7 Relatórios\nComo gestor…\n"

	doc := Parse(md)
	if len(doc.Stories) != 3 {
		t.Fatalf("stories = %d, want 3: %+v", len(doc.Stories), doc.Stories)
	}
	if !strings.Contains(doc.Preamble, "Contexto comum") {
		t.Errorf("preamble = %q", doc.Preamble)
	}

	want := []struct{ id, title, contains string }{
		{"US-101", "US-101: Checkout", "### Critérios"},
		{"US-102", "US-102 — Estorno", "US-999 dentro de código"},
		{"HU-7", "HU 7 Relatórios", "Como gestor"},
	}
	for i, w := range want {
		s := doc.Stories[i]
		if s.ID != w.id || s.Title != w.title || !strings.Contains(s.Content, w.contains) {
			t.Errorf("story %d = %+v, want id=%s title=%s containing %q", i, s, w.id, w.title, w.contains)
		}
	}
	if strings.Contains(doc.Stories[0].Content, "US-102") {
		t.Error("story content must stop at the next story")
	}
}

func TestParseContainerHeadingIsNotAStory(t *testing.T) {
	md := "# User Stories\n\n## User Story: login\nA\n\n## User Story: logout\nB\n"
	doc := Parse(md)
	if len(doc.Stories) != 2 || doc.Stories[0].ID != "US 1" || doc.Stories[1].ID != "US 2" {
		t.Fatalf("unexpected stories: %+v", doc.Stories)
	}
}

func TestParseShallowerHeadingClosesStory(t *testing.T) {
	md := "## US-1 A\nconteúdo A\n# Anexos\nnão faz parte\n"
	doc := Parse(md)
	if len(doc.Stories) != 1 || strings.Contains(doc.Stories[0].Content, "Anexos") {
		t.Fatalf("story should end at shallower heading: %+v", doc.Stories)
	}
}

func TestParseWithoutStoryHeadings(t *testing.T) {
	doc := Parse("# Carrinho de compras\n\nComo cliente, quero…\n")
	if len(doc.Stories) != 1 || doc.Stories[0].Title != "Carrinho de compras" {
		t.Fatalf("unexpected: %+v", doc.Stories)
	}

	if doc := Parse("   \n"); len(doc.Stories) != 0 {
		t.Errorf("blank document should have no stories: %+v", doc.Stories)
	}
}

func TestParseLetterPrefixedIDsAcrossEpics(t *testing.T) {
	md := "# Backlog de User Stories\n\n### Estrutura de cada US\n1. **História**\n\n" +
		"## Épico A — Fundação\nIntro do épico.\n\n" +
		"### US-A01 — Cadastrar tenant\n**Como** admin…\n\n" +
		"### US-A02 — Autenticar\n**Como** usuário…\n\n" +
		"## Épico B — Reservas\nIntro B.\n\n" +
		"### US-B01 — Unidade\n**Como** gestor…\n"

	doc := Parse(md)
	if doc.Method != MethodHeadings || len(doc.Stories) != 3 {
		t.Fatalf("stories = %d (%s), want 3: %+v", len(doc.Stories), doc.Method, doc.Stories)
	}
	for i, id := range []string{"US-A01", "US-A02", "US-B01"} {
		if doc.Stories[i].ID != id {
			t.Errorf("story %d id = %q, want %q", i, doc.Stories[i].ID, id)
		}
	}
	if strings.Contains(doc.Stories[1].Content, "Épico B") {
		t.Error("an epic heading must close the previous story")
	}
}

func TestParseEpicPrefixedIDsBeatSectionTitles(t *testing.T) {
	md := "# Backlog\n\n## 5. Template único de User Story\n\n````markdown\n### US-<ÉPICO>-<NN> — Título\n```gherkin\nCenário: x\n```\n````\n\n" +
		"## 9. User Stories\n\n### Épico `plataforma`\n\n### US-PLT-01 — Publicar loja\n\nComo equipe, quero.\n\n### US-PLT-02 — Servir arquivos\n\nComo operador, quero.\n\n" +
		"### Épico `contrato`\n\n### US-CTR-01 — Padronizar paginação\n\nComo operador, quero.\n"
	doc := Parse(md)
	if doc.Method != MethodHeadings || len(doc.Stories) != 3 {
		t.Fatalf("method %s, %d stories: %+v", doc.Method, len(doc.Stories), doc.Stories)
	}
	for i, id := range []string{"US-PLT-01", "US-PLT-02", "US-CTR-01"} {
		if doc.Stories[i].ID != id {
			t.Errorf("story %d id = %q, want %q", i, doc.Stories[i].ID, id)
		}
	}
	if strings.Contains(doc.Stories[1].Content, "US-CTR-01") || strings.Contains(doc.Stories[2].Content, "Épico `contrato`") {
		t.Error("a story must end at the next story or epic heading")
	}
}

func TestParseKeepsShortPrefixIDs(t *testing.T) {
	doc := Parse("## US-A01 — a\n\ntexto\n\n## HU 3 — b\n\ntexto\n")
	if len(doc.Stories) != 2 || doc.Stories[0].ID != "US-A01" || doc.Stories[1].ID != "HU-3" {
		t.Fatalf("stories = %+v", doc.Stories)
	}
}

func TestParseAcceptsMoreThanFiftyStories(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&b, "### US-X-%02d — história %d\n\nComo a, quero b.\n\n", i, i)
	}
	if got := len(Parse(b.String()).Stories); got != 80 {
		t.Errorf("stories = %d, want 80", got)
	}
}
