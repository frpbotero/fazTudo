package stories

import (
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
