package stories

import (
	"strings"
	"testing"
)

func TestDetectPromptNumbersNonBlankLines(t *testing.T) {
	p := DetectPrompt("# T\n\n**US 1** login\n" + strings.Repeat("x", 300))
	for _, want := range []string{"1| # T", "3| **US 1** login", "4| " + strings.Repeat("x", maxOutlineLine) + "…"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "2|") {
		t.Error("blank lines must be skipped")
	}
}

func TestFromModelSplitsAtReportedLines(t *testing.T) {
	md := "# Backlog\nContexto.\n\n" +
		"**Login**\nComo usuário, quero entrar.\n\n" +
		"**Logout**\nComo usuário, quero sair.\n"
	out := "Claro!\n```json\n{\"stories\":[{\"line\":7,\"id\":\"S-2\"},{\"line\":4,\"id\":\"\"},{\"line\":4},{\"line\":99}]}\n```"

	doc, err := FromModel(md, out)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Method != MethodModel || len(doc.Stories) != 2 || doc.Preamble != "# Backlog\nContexto." {
		t.Fatalf("unexpected doc: %+v", doc)
	}
	if s := doc.Stories[0]; s.Title != "Login" || s.ID != "US 1" || strings.Contains(s.Content, "Logout") {
		t.Errorf("story 0 = %+v", s)
	}
	if s := doc.Stories[1]; s.Title != "Logout" || s.ID != "S-2" || !strings.Contains(s.Content, "quero sair") {
		t.Errorf("story 1 = %+v", s)
	}
}

func TestFromModelHeadingStoryEndsAtShallowerHeading(t *testing.T) {
	md := "## Épico\n### Cadastro\nA\n## Anexos\nB\n"
	doc, err := FromModel(md, `{"stories":[{"line":2}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Stories) != 1 || strings.Contains(doc.Stories[0].Content, "Anexos") {
		t.Fatalf("unexpected: %+v", doc.Stories)
	}
}

func TestFromModelRejectsBadOutput(t *testing.T) {
	for _, out := range []string{"sem json", `{"stories":`, `{"stories":[]}`, `{"stories":[{"line":0}]}`} {
		if _, err := FromModel("# A\nb\n", out); err == nil {
			t.Errorf("FromModel(%q) should fail", out)
		}
	}
}
