//go:build !desktop

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"llm-orchestrator/internal/pipeline"
	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/server"
	"llm-orchestrator/internal/settings"
	"llm-orchestrator/internal/workspace"
)

func consoleAction(args []string, input io.Reader, out, errOut io.Writer) int {
	command, verb, rest := args[0], "", args[1:]
	switch command {
	case "repo", "policy", "config", "providers", "prompts", "stories", "commit":
		if len(rest) == 0 {
			fmt.Fprintf(errOut, "informe a ação de %s\n", command)
			return 2
		}
		verb, rest = rest[0], rest[1:]
	case "status", "logs", "stop", "resume", "retry":
	default:
		fmt.Fprintf(errOut, "comando desconhecido: %s\n", command)
		return 2
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	repo := fs.String("repo", os.Getenv("REPO_PATH"), "repositório alvo")
	config := fs.String("config", envOr("FAZTUDO_CONFIG", settings.DefaultPath()), "arquivo de configuração")
	file := fs.String("file", "", "arquivo de entrada")
	path := fs.String("path", "", "caminho do repositório")
	id := fs.String("id", "", "ID do provedor ou prompt")
	story := fs.Int("story", -1, "índice da User Story (começa em zero)")
	step := fs.String("step", "", "ID da etapa")
	seconds := fs.Int("seconds", 45, "duração da cota simulada")
	jsonOutput := fs.Bool("json", false, "exibe JSON")
	level := fs.String("level", "", "filtra logs por nível (lista separada por vírgula)")
	model := fs.Bool("detect-model", false, "usa modelo para detectar histórias")
	real := fs.Bool("real", false, "permite chamar o modelo para detectar histórias")
	delay := fs.Duration("sim-delay", 1400*time.Millisecond, "duração de cada etapa simulada")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *model && !*real {
		fmt.Fprintln(errOut, "--detect-model requer --real")
		return 2
	}
	c, err := newConsoleRuntime(*repo, *config, !*real, "", *delay, errOut)
	if err != nil {
		fmt.Fprintln(errOut, "erro:", err)
		return 1
	}
	defer c.close()
	var result any
	switch command + "/" + verb {
	case "stop/":
		err = c.request(http.MethodPost, "/api/pipeline/stop", nil, nil)
		if err == nil {
			fmt.Fprintln(out, "Parada solicitada.")
		}
	case "status/":
		var snap server.Snapshot
		err = c.request(http.MethodGet, "/api/status", nil, &snap)
		if err == nil {
			if *jsonOutput {
				result = snap
			} else {
				printRunStatus(out, snap.Run)
			}
		}
	case "logs/":
		var snap server.Snapshot
		err = c.request(http.MethodGet, "/api/status", nil, &snap)
		if err == nil {
			if *level != "" {
				allowed := map[string]bool{}
				for _, item := range strings.Split(*level, ",") {
					allowed[strings.TrimSpace(item)] = true
				}
				filtered := snap.Run.Logs[:0]
				for _, entry := range snap.Run.Logs {
					if allowed[entry.Level] {
						filtered = append(filtered, entry)
					}
				}
				snap.Run.Logs = filtered
			}
			if *jsonOutput {
				result = snap.Run.Logs
			} else {
				printNewLogs(out, snap.Run, 0)
			}
		}
	case "resume/":
		err = c.request(http.MethodPost, "/api/pipeline/resume", nil, nil)
		if err == nil {
			return c.waitRun(out, errOut)
		}
	case "retry/":
		if *step == "" {
			fmt.Fprintln(errOut, "informe --step")
			return 2
		}
		err = c.request(http.MethodPost, "/api/pipeline/retry", map[string]any{"story": *story, "step": *step}, nil)
		if err == nil {
			return c.waitRun(out, errOut)
		}
	case "repo/show":
		err = c.request(http.MethodGet, "/api/workspace", nil, &result)
	case "repo/set":
		if *path == "" {
			fmt.Fprintln(errOut, "informe --path")
			return 2
		}
		err = c.request(http.MethodPut, "/api/workspace", map[string]string{"path": *path}, &result)
	case "repo/discovery-reset":
		err = c.request(http.MethodDelete, "/api/workspace/discovery", nil, nil)
		result = map[string]string{"status": "discovery removido"}
	case "repo/branches":
		err = c.request(http.MethodGet, "/api/workspace/repos", nil, &result)
	case "repo/base":
		if *path == "" || *id == "" {
			fmt.Fprintln(errOut, "informe --path ('.' para o repositório principal) e --id (branch)")
			return 2
		}
		var state workspace.State
		err = c.request(http.MethodGet, "/api/workspace", nil, &state)
		if err == nil {
			policy := state.Policy
			if policy.BaseBranches == nil {
				policy.BaseBranches = map[string]string{}
			}
			policy.BaseBranches[*path] = *id
			err = c.request(http.MethodPut, "/api/workspace/policy", policy, &result)
		}
	case "policy/show":
		var state workspace.State
		err = c.request(http.MethodGet, "/api/workspace", nil, &state)
		result = state.Policy
	case "policy/apply":
		var policy workspace.Policy
		err = readJSONFile(*file, input, &policy)
		if err == nil {
			err = c.request(http.MethodPut, "/api/workspace/policy", policy, &result)
		}
	case "config/show":
		err = c.request(http.MethodGet, "/api/config", nil, &result)
	case "config/apply":
		var update provider.ConfigUpdate
		err = readJSONFile(*file, input, &update)
		if err == nil {
			err = c.request(http.MethodPut, "/api/config", update, &result)
		}
	case "providers/list":
		var snap server.Snapshot
		err = c.request(http.MethodGet, "/api/status", nil, &snap)
		result = snap.Providers
	case "providers/exhaust":
		if *id == "" {
			fmt.Fprintln(errOut, "informe --id")
			return 2
		}
		if c.control == nil || !c.control.hasActive() {
			fmt.Fprintln(errOut, "não há execução de console ativa para simular cota")
			return 1
		}
		body, _ := json.Marshal(map[string]int{"seconds": *seconds})
		var response consoleResponse
		response, err = c.control.remote(http.MethodPost, "/api/providers/"+url.PathEscape(*id)+"/exhaust", body)
		if err == nil && response.Status == 0 {
			err = fmt.Errorf("a execução de console terminou")
		}
		if err == nil {
			err = decodeConsoleResponse(response, nil)
		}
		result = map[string]string{"status": "cota simulada"}
	case "providers/restore":
		if c.control == nil || !c.control.hasActive() {
			fmt.Fprintln(errOut, "não há execução de console ativa para restaurar cota")
			return 1
		}
		endpoint := "/api/providers/restore"
		if *id != "" {
			endpoint = "/api/providers/" + url.PathEscape(*id) + "/restore"
		}
		var response consoleResponse
		response, err = c.control.remote(http.MethodPost, endpoint, nil)
		if err == nil && response.Status == 0 {
			err = fmt.Errorf("a execução de console terminou")
		}
		if err == nil {
			err = decodeConsoleResponse(response, nil)
		}
		result = map[string]string{"status": "cota restaurada"}
	case "prompts/list":
		err = c.request(http.MethodGet, "/api/prompts", nil, &result)
	case "prompts/show":
		var prompts []pipeline.PromptItem
		err = c.request(http.MethodGet, "/api/prompts", nil, &prompts)
		if err == nil {
			for _, item := range prompts {
				if item.Key == *id {
					result = item
					break
				}
			}
			if result == nil {
				err = fmt.Errorf("prompt %q não encontrado", *id)
			}
		}
	case "prompts/set":
		if *id == "" {
			fmt.Fprintln(errOut, "informe --id")
			return 2
		}
		var data []byte
		data, err = readConsoleFile(*file, input)
		if err == nil {
			err = c.request(http.MethodPut, "/api/prompts/"+url.PathEscape(*id), map[string]string{"value": string(data)}, nil)
		}
		result = map[string]string{"status": "prompt salvo"}
	case "prompts/reset":
		if *id == "" {
			fmt.Fprintln(errOut, "informe --id")
			return 2
		}
		err = c.request(http.MethodDelete, "/api/prompts/"+url.PathEscape(*id), nil, nil)
		result = map[string]string{"status": "padrão restaurado"}
	case "stories/parse":
		var data []byte
		data, err = readConsoleFile(*file, input)
		if err == nil {
			body, _ := json.Marshal(map[string]any{"content": string(data), "use_model": *model})
			err = decodeConsoleResponse(c.requestLocal(http.MethodPost, "/api/stories/parse", body), &result)
		}
	case "commit/show":
		if *id == "" {
			fmt.Fprintln(errOut, "informe --id (hash do commit)")
			return 2
		}
		var patch struct {
			Patch string `json:"patch"`
		}
		err = c.request(http.MethodGet, "/api/git/commits/"+url.PathEscape(*id), nil, &patch)
		if err == nil {
			if *jsonOutput {
				result = patch
			} else {
				fmt.Fprint(out, patch.Patch)
			}
		}
	default:
		fmt.Fprintf(errOut, "ação desconhecida: %s %s\n", command, verb)
		return 2
	}
	if err != nil {
		fmt.Fprintln(errOut, "erro:", err)
		return 1
	}
	if result != nil {
		return writeJSON(out, errOut, result)
	}
	return 0
}

func readConsoleFile(path string, input io.Reader) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("informe --file")
	}
	var reader io.Reader
	if path == "-" {
		reader = input
	} else {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("arquivo excede 1 MiB")
	}
	return data, nil
}

func readJSONFile(path string, input io.Reader, dst any) error {
	data, err := readConsoleFile(path, input)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("arquivo JSON excede 1 MiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("arquivo contém dados após o JSON")
	}
	return nil
}

func writeJSON(out, errOut io.Writer, value any) int {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(errOut, "erro:", err)
		return 1
	}
	return 0
}

// printStoryLine shows a story's execution status and, once it closed, its
// validation, so a finished story is not read as an approved one.
func printStoryLine(out io.Writer, story pipeline.StoryRun) {
	if story.Validation == "" {
		fmt.Fprintf(out, "%s: %s (%s)\n", story.ID, story.Title, story.Status)
		return
	}
	fmt.Fprintf(out, "%s: %s (%s, validação %s)\n", story.ID, story.Title, story.Status, story.Validation.Label())
}

func printRunStatus(out io.Writer, run pipeline.Run) {
	if run.Repository == "" {
		fmt.Fprintln(out, "Nenhuma execução registrada.")
		return
	}
	fmt.Fprintf(out, "Repositório: %s\nResultado: %s\nEm execução: %t\n", run.Repository, run.Result, run.Running)
	if run.Validation != "" {
		fmt.Fprintf(out, "Validação: %s\n", run.Validation.Label())
	}
	for _, story := range run.Stories {
		printStoryLine(out, story)
		for _, step := range story.Steps {
			if step.Status != pipeline.StepPending {
				fmt.Fprintf(out, "  %s: %s\n", step.Title, step.Status)
			}
		}
	}
	if run.Resume != nil {
		fmt.Fprintf(out, "Retomar: %s\n", run.Resume.Title)
	}
	if run.Kind == pipeline.RunCleanup {
		printCleanup(out, run)
	}
}
