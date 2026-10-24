// Package stories splits a Markdown document into individual user stories.
package stories

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxStories caps how many stories a single document may yield.
const MaxStories = 100

// How a document's stories were found.
const (
	MethodHeadings = "headings" // story headings matched by pattern
	MethodModel    = "model"    // story starts identified by an LLM
	MethodWhole    = "whole"    // no story found: the whole document is one story
)

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
	Method   string  `json:"method,omitempty"`
}

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	reFence   = regexp.MustCompile("^\\s*(```|~~~)")
	// IDs may carry an epic prefix, glued or separated ("US-A01", "HU B3",
	// "US-PLT-01"). reStoryID is the strong signal; the singular words
	// "user story" / "história" count only when no heading has an ID, since
	// section titles such as "5. Template de User Story" also contain them
	// ("User Stories" / "Histórias" are container headings).
	reStoryID = regexp.MustCompile(`(?i)\b(?:US|HU)[-_ ]?(?:[A-Z]{1,6}[-_ ]?)?\d+\b`)
	reStory   = regexp.MustCompile(`(?i)\buser story\b|\bhist[óo]ria\b`)
	reID      = regexp.MustCompile(`(?i)\b(US|HU)[-_ ]?((?:[A-Z]{1,6}[-_ ]?)?\d+)\b`)
)

type heading struct {
	line  int
	level int // 0 for a story start that is not a Markdown heading
	title string
}

// Parse splits md into stories. Story headings are Markdown headings that
// look like "US-101", "US-A01", "HU 3", "User Story" or "História". When none
// is found the whole document is treated as a single story.
func Parse(md string) Document {
	lines := splitLines(md)
	all := headings(lines)

	candidates := storyHeadings(all)
	if len(candidates) == 0 {
		content := strings.TrimSpace(strings.Join(lines, "\n"))
		if content == "" {
			return Document{}
		}
		return Document{
			Stories: []Story{{ID: "US 1", Title: firstTitle(all, lines), Content: content}},
			Method:  MethodWhole,
		}
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
	doc := split(lines, all, starts)
	doc.Method = MethodHeadings
	return doc
}

// storyHeadings picks the headings that open a story: those carrying an ID,
// or, when none does, those naming a "user story" or "história".
func storyHeadings(all []heading) []heading {
	var withID, named []heading
	for _, h := range all {
		switch {
		case reStoryID.MatchString(h.title):
			withID = append(withID, h)
		case reStory.MatchString(h.title):
			named = append(named, h)
		}
	}
	if len(withID) > 0 {
		return withID
	}
	return named
}

// split cuts lines into one story per start. A story ends at the next start
// or at a heading shallower than its own (a new epic/section).
func split(lines []string, all, starts []heading) Document {
	doc := Document{Preamble: strings.TrimSpace(strings.Join(lines[:starts[0].line], "\n"))}
	for i, s := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1].line
		}
		for _, h := range all {
			if h.line > s.line && h.line < end && h.level < s.level {
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

func splitLines(md string) []string {
	return strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
}

// headings lists the Markdown headings outside fenced code blocks.
func headings(lines []string) []heading {
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
	return all
}

func storyID(title string, index int) string {
	if m := reID.FindStringSubmatch(title); m != nil {
		return strings.ToUpper(m[1]) + "-" + strings.ToUpper(strings.NewReplacer(" ", "-", "_", "-").Replace(strings.TrimRight(m[2], "-_ ")))
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
