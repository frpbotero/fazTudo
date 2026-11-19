package pipeline

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"llm-orchestrator/internal/stories"
)

// maxPromptBytes bounds a customized prompt: every byte goes to every call.
const maxPromptBytes = 32 << 10

// Errors of a prompt edit.
var (
	ErrUnknownPrompt = errors.New("prompt desconhecido")
	ErrInvalidPrompt = errors.New("prompt inválido")
)

// Catalog keys outside the stages.
const (
	promptDetect = "stories.detect"
	promptRules  = "rules.engineering"
	promptFix    = "fix.task"
	promptVerify = "verify.task"
	promptAudit  = "audit.notice"
)

// PromptItem is one editable text of the application. Output contracts come
// along for reading only: the orchestrator parses them, so they stay in code.
type PromptItem struct {
	Key         string   `json:"key"`
	Group       string   `json:"group"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Value       string   `json:"value"`
	Default     string   `json:"default"`
	Customized  bool     `json:"customized"`
	Contract    string   `json:"contract,omitempty"`
	Parts       []string `json:"parts"`
	Warning     string   `json:"warning,omitempty"`
}

var (
	partsSystem = []string{"Regras do agente", "Papel (este texto)"}
	partsRules  = []string{"Papel da etapa", "Regras (este texto)"}
	partsTask   = []string{
		"Repositório e Discovery", "Instruções do projeto", "Regras de entrega", "User Story",
		"Etapas anteriores", "Skills", "Tarefa (este texto)", "Contrato de saída",
	}
	partsFix = []string{
		"Repositório e Discovery", "Instruções do projeto", "Regras de entrega", "User Story", "Plano do arquiteto",
		"Dossiê da investigação", "Correções atribuídas", "Tarefa (este texto)", "Contrato de saída",
	}
)

// promptCatalog lists the customizable texts of stages and of the fixed
// stages, in the order the application uses them.
func promptCatalog(stages []Stage) []PromptItem {
	items := []PromptItem{{
		Key: promptDetect, Group: "Preparação", Title: "Identificação de User Stories",
		Description: "Encontra onde cada US começa no documento enviado.", Default: strings.TrimRight(stories.DetectInstructions, "\n"),
		Contract: stories.DetectContract, Parts: []string{"Instruções (este texto)", "Contrato de saída", "Documento numerado"},
	}}
	items = append(items, stageItems("Preparação", DiscoveryStage)...)
	items = append(items, stageItems("Preparação", AnalysisStage)...)
	for _, s := range stages {
		items = append(items, stageItems("Fluxo da US", s)...)
		for _, l := range s.Lenses {
			items = append(items, PromptItem{
				Key: "lens." + l.ID, Group: "Foco do code review", Title: l.Title, Default: l.Focus,
				Description: "O que uma das revisões olha.", Parts: []string{"Tarefa de revisão", "Foco (este texto)"},
			})
		}
	}
	items = append(items, PromptItem{
		Key: promptRules, Group: "Regras compartilhadas", Title: "Regras de engenharia", Default: engineeringRules,
		Description: "Valem para frontend, backend e integração.", Parts: partsRules,
		Warning: "Estas regras contêm limites de economia e de segurança. Removê-las pode aumentar o uso de tokens e reduzir a qualidade.",
	})
	items = append(items, stageItems("Investigação e correção", InvestigatorStage)...)
	items = append(items,
		PromptItem{
			Key: promptFix, Group: "Investigação e correção", Title: "Tarefa de correção", Default: fixTask,
			Description: "Como o implementador trabalha uma falha.", Contract: strings.TrimSpace(fixInstructions), Parts: partsFix,
		},
		PromptItem{
			Key: promptVerify, Group: "Investigação e correção", Title: "Reavaliação após correção", Default: verifyTask,
			Description: "Abre a nova rodada de uma revisão.", Contract: strings.TrimSpace(verdictInstructions),
			Parts: []string{"Problemas apontados", "Correções aplicadas", "Tarefa (este texto)", "Contrato de saída"},
		},
		PromptItem{
			Key: promptAudit, Group: "Casos especiais", Title: "Aviso do modo auditoria", Default: strings.TrimSpace(auditNotice),
			Description: "Avisa que a US já foi desenvolvida.", Parts: []string{"User Story", "Aviso (este texto)", "Tarefa"},
		},
	)
	items = append(items, stageItems("Casos especiais", IntegrationStage)...)
	items = append(items, stageItems("Casos especiais", SelectorStage)...)
	return append(items, stageItems("Saúde do código", CleanupStage)...)
}

// stageItems lists what a stage has to edit; a stage that calls no model has
// nothing.
func stageItems(group string, s Stage) []PromptItem {
	var items []PromptItem
	if s.System != "" {
		items = append(items, PromptItem{
			Key: "stage." + s.ID + ".system", Group: group, Title: s.Title + ": papel", Default: s.System,
			Description: "Quem o agente é nesta etapa (" + s.Role + ").", Parts: partsSystem,
		})
	}
	if s.Task != "" && !s.Verifier {
		contract := strings.TrimSpace(stageInstructions(s))
		switch s.ID {
		case AnalysisStage.ID:
			contract = strings.TrimSpace(analysisInstructions)
		case InvestigatorStage.ID:
			contract = strings.TrimSpace(investigationInstructions)
		}
		items = append(items, PromptItem{
			Key: "stage." + s.ID + ".task", Group: group, Title: s.Title + ": tarefa", Default: s.Task,
			Description: "O que o agente faz nesta etapa.", Contract: contract, Parts: partsTask,
		})
	}
	return items
}

// promptTexts are the resolved texts used outside the stage list.
type promptTexts struct {
	discovery, analysis, integration, selector, investigator, cleanup Stage
	fixTask, verifyTask, auditNotice                                  string
}

func resolveTexts(overrides map[string]string) promptTexts {
	rules := resolvedRules(overrides)
	stage := func(s Stage) Stage { return resolveStage(s, overrides, rules) }
	audit := ""
	if text, ok := overrides[promptAudit]; ok {
		audit = text + "\n\n"
	}
	return promptTexts{
		discovery: stage(DiscoveryStage), analysis: stage(AnalysisStage), integration: stage(IntegrationStage),
		selector: stage(SelectorStage), investigator: stage(InvestigatorStage), cleanup: stage(CleanupStage),
		fixTask: override(overrides, promptFix, fixTask), verifyTask: override(overrides, promptVerify, verifyTask), auditNotice: audit,
	}
}

func override(overrides map[string]string, key, fallback string) string {
	if text, ok := overrides[key]; ok {
		return text
	}
	return fallback
}

func resolvedRules(overrides map[string]string) string {
	return override(overrides, promptRules, engineeringRules)
}

// resolveStages applies overrides to copies: the base stages never change.
func resolveStages(stages []Stage, overrides map[string]string) []Stage {
	rules := resolvedRules(overrides)
	out := make([]Stage, len(stages))
	for i, s := range stages {
		out[i] = resolveStage(s, overrides, rules)
	}
	return out
}

func resolveStage(s Stage, overrides map[string]string, rules string) Stage {
	s.System = override(overrides, "stage."+s.ID+".system", s.System)
	s.Task = override(overrides, "stage."+s.ID+".task", s.Task)
	if s.Rules {
		s.System += "\n\n" + rules
	}
	s.Lenses = slices.Clone(s.Lenses)
	for i := range s.Lenses {
		s.Lenses[i].Focus = override(overrides, "lens."+s.Lenses[i].ID, s.Lenses[i].Focus)
	}
	return s
}

// Prompts lists the catalog with the current values.
func (o *Orchestrator) Prompts() []PromptItem {
	o.mu.RLock()
	defer o.mu.RUnlock()
	items := promptCatalog(o.baseStages)
	for i := range items {
		items[i].Value = override(o.prompts, items[i].Key, items[i].Default)
		_, items[i].Customized = o.prompts[items[i].Key]
	}
	return items
}

// Prompt returns the current text of one catalog key.
func (o *Orchestrator) Prompt(key string) string {
	for _, item := range o.Prompts() {
		if item.Key == key {
			return item.Value
		}
	}
	return ""
}

// PromptOverrides returns a copy of what differs from the defaults.
func (o *Orchestrator) PromptOverrides() map[string]string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return maps.Clone(o.prompts)
}

// SetPrompt stores a customized text, valid from the next run. A text equal to
// the default is no customization, so it keeps following new versions.
func (o *Orchestrator) SetPrompt(key, value string) error {
	value = strings.TrimRight(value, " \t\r\n")
	if err := validatePrompt(value); err != nil {
		return err
	}
	item, ok := o.promptItem(key)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPrompt, key)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if value == item.Default {
		delete(o.prompts, key)
		return nil
	}
	if o.prompts == nil {
		o.prompts = map[string]string{}
	}
	o.prompts[key] = value
	return nil
}

// ResetPrompt goes back to the default text.
func (o *Orchestrator) ResetPrompt(key string) error {
	if _, ok := o.promptItem(key); !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPrompt, key)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.prompts, key)
	return nil
}

// LoadPrompts restores saved customizations. A stale entry, such as a stage
// that no longer exists, is reported and skipped.
func (o *Orchestrator) LoadPrompts(saved map[string]string) []error {
	var errs []error
	for _, key := range slices.Sorted(maps.Keys(saved)) {
		if err := o.SetPrompt(key, saved[key]); err != nil {
			errs = append(errs, fmt.Errorf("prompt %s: %w", key, err))
		}
	}
	return errs
}

func (o *Orchestrator) promptItem(key string) (PromptItem, bool) {
	items := promptCatalog(o.baseStages)
	i := slices.IndexFunc(items, func(item PromptItem) bool { return item.Key == key })
	if i < 0 {
		return PromptItem{}, false
	}
	return items[i], true
}

func validatePrompt(value string) error {
	switch {
	case strings.TrimSpace(value) == "":
		return fmt.Errorf("%w: use Restaurar padrão em vez de deixar o texto vazio", ErrInvalidPrompt)
	case len(value) > maxPromptBytes:
		return fmt.Errorf("%w: limite de %d KiB", ErrInvalidPrompt, maxPromptBytes>>10)
	case strings.ContainsRune(value, 0) || !utf8.ValidString(value):
		return fmt.Errorf("%w: caracteres não permitidos", ErrInvalidPrompt)
	}
	return nil
}

func (o *Orchestrator) logCustomPrompts(keys []string) {
	if len(keys) > 0 {
		o.logf(LevelInfo, "Prompts customizados nesta execução: %s.", strings.Join(keys, ", "))
	}
}

// usePrompts resolves the customizations for the run about to start and
// reports them, since they explain differences between runs. o.mu is held.
func (o *Orchestrator) usePrompts() []string {
	o.stages = resolveStages(o.baseStages, o.prompts)
	o.texts = resolveTexts(o.prompts)
	return slices.Sorted(maps.Keys(o.prompts))
}
