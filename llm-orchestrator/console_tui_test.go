//go:build !desktop

package main

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUICommandParserPreservesQuotedPaths(t *testing.T) {
	got, err := splitTUIWords(`run --file "minhas US.md" --story 'US-1'`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--file", "minhas US.md", "--story", "US-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTUITabChangesVisibleSection(t *testing.T) {
	model := newTUIModel("", "")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	next := updated.(tuiModel)
	if next.tab != 1 || next.View() == model.View() {
		t.Fatalf("tab não mudou: %d", next.tab)
	}
}

func TestTUIOutputCanScrollAndClear(t *testing.T) {
	model := newTUIModel("", "")
	model.height = 10
	for i := 0; i < 12; i++ {
		updated, _ := model.Update(tuiOutput{line: "linha"})
		model = updated.(tuiModel)
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(tuiModel)
	if model.scroll != 1 {
		t.Fatalf("scroll = %d", model.scroll)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	model = updated.(tuiModel)
	if len(model.lines) != 0 || model.scroll != 0 {
		t.Fatalf("limpeza falhou: %d linhas, scroll %d", len(model.lines), model.scroll)
	}
}

func TestTUIUpdatesTargetAfterRepositoryChange(t *testing.T) {
	model := newTUIModel("/antigo", "")
	updated, _ := model.Update(tuiDone{code: 0, repo: "/novo"})
	if got := updated.(tuiModel).repo; got != "/novo" {
		t.Fatalf("repositório = %q", got)
	}
}

func TestTUIHasCodeHealthSection(t *testing.T) {
	for _, tab := range tuiTabs {
		if tab.name == "Saúde do código" && tab.defaultCommand == "health areas" {
			return
		}
	}
	t.Fatal("TUI must offer the code health commands")
}
