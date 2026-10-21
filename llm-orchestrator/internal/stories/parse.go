// Package stories splits a Markdown document into individual user stories.
package stories

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxStories caps how many stories a single document may yield.
const MaxStories = 50

// Story is one user story extracted from a document.
type Story struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// Document is the result of parsing a Markdown file.
type Document struct {
	// Preamble is the text before the first story (epic, shared context).
	Preamble string  `json:"preamble,omitempty"`
	Stories  []Story `json:"stories"`
}

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	reFence   = regexp.MustCompile("^\\s*(```|~~~)")
	// Singular forms only: "User Stories" / "Histórias" are container headings.
	reStory = regexp.MustCompile(`(?i)\b(?:US|HU)[-_ ]?\d+\b|\buser story\b|\bhist[óo]ria\b`)
	reID    = regexp.MustCompile(`(?i)\b(US|HU)[-_ ]?(\d+)\b`)
)

type heading struct {
	line  int
	level int
	title string
}

// Parse splits md into stories. Story headings are Markdown headings that
// look like "US-101", "HU 3", "User Story" or "História". When none is found
// the whole document is treated as a single story.
func Parse(md string) Document {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	lines := strings.Split(md, "\n")

	var all []heading
	inFence := false
	for i, l := range lines {
		if reFence.MatchString(l) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := reHeading.FindStringSubmatch(l); m != nil {
			all = append(all, heading{line: i, level: len(m[1]), title: strings.TrimSpace(m[2])})
		}
	}

	var candidates []heading
	for _, h := range all {
		if reStory.MatchString(h.title) {
			candidates = append(candidates, h)
		}
	}
	if len(candidates) == 0 {
		content := strings.TrimSpace(md)
		if content == "" {
			return Document{}
		}
		return Document{Stories: []Story{{ID: "US 1", Title: firstTitle(all, lines), Content: content}}}
	}

	// Stories live at the shallowest level used by a story heading; deeper
	// matches (e.g. "### Histórico") stay inside their story.
	level := candidates[0].level
	for _, c := range candidates {
		level = min(level, c.level)
	}

	var starts []heading
	for _, c := range candidates {
		if c.level == level {
			starts = append(starts, c)
		}
	}

	doc := Document{Preamble: strings.TrimSpace(strings.Join(lines[:starts[0].line], "\n"))}
	for i, s := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1].line
		}
		// A shallower heading (a new epic/section) also closes the story.
		for _, h := range all {
			if h.line > s.line && h.line < end && h.level < level {
				end = h.line
				break
			}
		}
		doc.Stories = append(doc.Stories, Story{
			ID:      storyID(s.title, i),
			Title:   s.title,
			Content: strings.TrimSpace(strings.Join(lines[s.line:end], "\n")),
		})
		if len(doc.Stories) == MaxStories {
			break
		}
	}
	return doc
}

func storyID(title string, index int) string {
	if m := reID.FindStringSubmatch(title); m != nil {
		return strings.ToUpper(m[1]) + "-" + m[2]
	}
	return fmt.Sprintf("US %d", index+1)
}

func firstTitle(all []heading, lines []string) string {
	if len(all) > 0 {
		return all[0].title
	}
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			if len([]rune(t)) > 80 {
				return string([]rune(t)[:80]) + "…"
			}
			return t
		}
	}
	return "User Story"
}
