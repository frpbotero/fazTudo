package stories

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// maxOutlineLine caps each line sent to the model; it only needs structure.
const maxOutlineLine = 120

// DetectSystem instructs the model that finds story starts in a document.
const DetectSystem = `Você identifica User Stories em documentos Markdown de backlog.
Recebe o documento com o número de cada linha no formato "N| texto".
Uma User Story começa na linha que a abre (normalmente um título como "US-A01 — ...", "HU 3", "História: ..."
ou o primeiro "Como <papel>, quero ..."). Títulos de épico, seções gerais, índices, tabelas e checklists não são US.
Responda SOMENTE com JSON, sem texto extra nem bloco de código:
{"stories":[{"line":<número da linha onde a US começa>,"id":"<identificador, ex.: US-A01; vazio se não houver>"}]}
Liste todas as US, na ordem do documento.`

// DetectPrompt renders md with line numbers for the model.
func DetectPrompt(md string) string {
	var b strings.Builder
	b.WriteString("Documento:\n\n")
	for i, l := range splitLines(md) {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if r := []rune(l); len(r) > maxOutlineLine {
			l = string(r[:maxOutlineLine]) + "…"
		}
		fmt.Fprintf(&b, "%d| %s\n", i+1, l)
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
