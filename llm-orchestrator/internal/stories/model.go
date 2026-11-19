package stories

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// maxOutlineLine caps each line sent to the model; it only needs structure.
const maxOutlineLine = 120

// DetectInstructions tells the model what counts as a story; a user may
// rewrite them. DetectContract is the format FromModel reads, and stays fixed.
const (
	DetectInstructions = `Você identifica User Stories em documentos Markdown de backlog.
Recebe o documento com o número de cada linha no formato "N| texto".
Uma User Story começa na linha que a abre (normalmente um título como "US-A01 — ...", "HU 3", "História: ..."
ou o primeiro "Como <papel>, quero ..."). Títulos de épico, seções gerais, índices, tabelas e checklists não são US.
`
	DetectContract = `Responda SOMENTE com JSON, sem texto extra nem bloco de código:
{"stories":[{"line":<número da linha onde a US começa>,"id":"<identificador, ex.: US-A01; vazio se não houver>"}]}
Liste todas as US, na ordem do documento.`
)

// DetectSystem instructs the model that finds story starts in a document.
const DetectSystem = DetectInstructions + DetectContract

// largeDocLineThreshold triggers structural outline filtering for large backlogs,
// keeping token usage low instead of forwarding thousands of body lines.
const largeDocLineThreshold = 100

var reRolePattern = regexp.MustCompile(`(?i)\bcomo\s+(?:um|uma|o|a)?\b|\bcomo\b.*\bquero\b`)

func isStructuralLine(l string) bool {
	return reHeading.MatchString(l) ||
		reStoryID.MatchString(l) ||
		reStory.MatchString(l) ||
		reRolePattern.MatchString(l) ||
		strings.HasPrefix(l, "**") ||
		strings.HasPrefix(l, "- ") ||
		strings.HasPrefix(l, "* ")
}

// DetectPrompt renders md with line numbers for the model.
func DetectPrompt(md string) string {
	var b strings.Builder
	b.WriteString("Documento:\n\n")
	lines := splitLines(md)
	isLarge := len(lines) > largeDocLineThreshold
	inFence := false
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		if reFence.MatchString(trimmed) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if isLarge && !isStructuralLine(trimmed) {
			continue
		}
		if r := []rune(trimmed); len(r) > maxOutlineLine {
			trimmed = string(r[:maxOutlineLine]) + "…"
		}
		fmt.Fprintf(&b, "%d| %s\n", i+1, trimmed)
	}
	return b.String()
}

// FromModel splits md at the story starts reported by the model's output
// (see DetectSystem). Invalid or out-of-range lines are ignored.
func FromModel(md, output string) (Document, error) {
	var reply struct {
		Stories []struct {
			Line int    `json:"line"`
			ID   string `json:"id"`
		} `json:"stories"`
	}
	start, end := strings.Index(output, "{"), strings.LastIndex(output, "}")
	if start < 0 || end < start {
		return Document{}, errors.New("resposta do modelo sem JSON")
	}
	if err := json.Unmarshal([]byte(output[start:end+1]), &reply); err != nil {
		return Document{}, fmt.Errorf("resposta do modelo inválida: %w", err)
	}

	lines := splitLines(md)
	all := headings(lines)
	level := make(map[int]int, len(all))
	for _, h := range all {
		level[h.line] = h.level
	}

	ids := map[int]string{}
	for _, s := range reply.Stories {
		n := s.Line - 1
		if n < 0 || n >= len(lines) || strings.TrimSpace(lines[n]) == "" {
			continue
		}
		if _, dup := ids[n]; !dup {
			ids[n] = strings.TrimSpace(s.ID)
		}
	}
	if len(ids) == 0 {
		return Document{}, errors.New("o modelo não encontrou User Stories")
	}

	var starts []heading
	for n := range ids {
		starts = append(starts, heading{line: n, level: level[n], title: startTitle(lines[n])})
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].line < starts[j].line })

	doc := split(lines, all, starts)
	for i := range doc.Stories {
		if id := ids[starts[i].line]; id != "" && !reID.MatchString(doc.Stories[i].Title) {
			doc.Stories[i].ID = id
		}
	}
	doc.Method = MethodModel
	return doc, nil
}

// startTitle strips Markdown decoration from a story's first line.
func startTitle(line string) string {
	t := strings.TrimSpace(line)
	if m := reHeading.FindStringSubmatch(t); m != nil {
		t = m[2]
	}
	t = strings.Trim(t, "*_ ")
	if r := []rune(t); len(r) > 120 {
		t = string(r[:120]) + "…"
	}
	return t
}
