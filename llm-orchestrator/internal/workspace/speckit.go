package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// GitHub Spec Kit keeps the project constitution in .specify/memory and one
// directory per feature under specs/ (spec.md, plan.md, tasks.md…).
const (
	specKitConstitution = ".specify/memory/constitution.md"
	specKitFeatures     = "specs"
	maxSpecKitFeatures  = 20
)

var specKitFiles = []string{"spec.md", "plan.md", "tasks.md", "data-model.md", "research.md", "quickstart.md"}

// specKitSection points agents at the repository's Spec Kit artifacts. It
// lists paths only: agents read the feature that matches their story, so the
// prompt does not grow with every spec. It returns "" without Spec Kit.
func specKitSection(root string) string {
	var lines []string
	if fileExists(filepath.Join(root, specKitConstitution)) {
		lines = append(lines, fmt.Sprintf("Constituição (obrigatória): `%s`.", specKitConstitution))
	}
	features := specKitFeatureList(root)
	if len(features) > 0 {
		lines = append(lines, "Features especificadas:")
		lines = append(lines, features...)
		lines = append(lines, "",
			"Se uma delas cobre esta User Story, leia spec.md e plan.md antes de agir: são a especificação e o plano aprovados.",
			"Não os reescreva. Quem implementa segue tasks.md na ordem e marca `[X]` nas tarefas concluídas.")
	}
	if len(lines) == 0 {
		return ""
	}
	return "### Spec Kit\n\n" + strings.Join(lines, "\n")
}

// specKitFeatureList returns one line per feature directory, newest first.
func specKitFeatureList(root string) []string {
	entries, err := os.ReadDir(filepath.Join(root, specKitFeatures))
	if err != nil {
		return nil
	}
	var lines []string
	for _, e := range slices.Backward(entries) {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, specKitFeatures, e.Name())
		var files []string
		for _, f := range specKitFiles {
			if fileExists(filepath.Join(dir, f)) {
				files = append(files, f)
			}
		}
		if len(files) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("- `%s/%s/`: %s", specKitFeatures, e.Name(), strings.Join(files, ", ")))
		if len(lines) == maxSpecKitFeatures {
			break
		}
	}
	return lines
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
