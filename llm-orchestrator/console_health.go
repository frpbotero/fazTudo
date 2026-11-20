//go:build !desktop

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"llm-orchestrator/internal/health"
	"llm-orchestrator/internal/pipeline"
	"llm-orchestrator/internal/settings"
)

// consoleHealth runs `health areas` (suggestion, no model) and `health run`
// (a cleanup followed in the foreground, like run).
func consoleHealth(args []string, input io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 || (args[0] != "areas" && args[0] != "run") {
		fmt.Fprintln(errOut, "use health areas ou health run --area CAMINHO")
		return 2
	}
	verb := args[0]
	fs := flag.NewFlagSet("health "+verb, flag.ContinueOnError)
	fs.SetOutput(errOut)
	repo := fs.String("repo", os.Getenv("REPO_PATH"), "repositório alvo")
	config := fs.String("config", envOr("FAZTUDO_CONFIG", settings.DefaultPath()), "arquivo de configuração")
	jsonOutput := fs.Bool("json", false, "exibe JSON")
	area := fs.String("area", "", "pasta a limpar, relativa ao repositório (\".\" = inteiro)")
	comments := fs.Bool("comments", false, "remove comentários que só repetem o código")
	tests := fs.Bool("tests", false, "remove ou reescreve testes que não pegam bug")
	rounds := fs.Int("rounds", 0, "rodadas da limpeza, de 1 a 5 (padrão 3)")
	real := fs.Bool("real", false, "execução real; o padrão é simulação")
	providerID := fs.String("provider", "", "provedor padrão desta execução")
	delay := fs.Duration("sim-delay", 1400*time.Millisecond, "duração de cada etapa simulada")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if verb == "run" {
		if strings.TrimSpace(*area) == "" {
			fmt.Fprintln(errOut, "informe --area (use \".\" para o repositório inteiro)")
			return 2
		}
		if *rounds < 0 || *rounds > 5 {
			fmt.Fprintln(errOut, "--rounds deve ficar entre 1 e 5")
			return 2
		}
	}
	c, err := newConsoleRuntime(*repo, *config, !*real, *providerID, *delay, errOut)
	if err != nil {
		fmt.Fprintln(errOut, "erro:", err)
		return 1
	}
	defer c.close()

	if verb == "areas" {
		var s health.AreaSuggestion
		if err := c.request(http.MethodGet, "/api/health/areas", nil, &s); err != nil {
			fmt.Fprintln(errOut, "erro:", err)
			return 1
		}
		for _, w := range s.Warnings {
			fmt.Fprintln(errOut, "aviso:", w)
		}
		if *jsonOutput {
			return writeJSON(out, errOut, s)
		}
		printAreas(out, s)
		return 0
	}

	body, _ := json.Marshal(pipeline.CleanupOptions{Area: *area, Comments: *comments, Tests: *tests, Rounds: *rounds, DryRun: !*real})
	if err := decodeConsoleResponse(c.requestLocal(http.MethodPost, "/api/health/start", body), nil); err != nil {
		fmt.Fprintln(errOut, "erro:", err)
		return 1
	}
	return c.waitRun(out, errOut)
}

func printAreas(out io.Writer, s health.AreaSuggestion) {
	if len(s.Areas) == 0 {
		fmt.Fprintln(out, "Nenhuma área com indicadores. Informe a área à mão em health run --area.")
		return
	}
	for i, a := range s.Areas {
		fmt.Fprintf(out, "%d. %s (pontuação %.2f)\n", i+1, a.Path, a.Score)
		for _, r := range a.Reasons {
			fmt.Fprintf(out, "   - %s\n", r)
		}
	}
	fmt.Fprintf(out, "Base: %d commit(s) recentes. Inicie com: faztudo health run --area %s\n", s.Commits, s.Areas[0].Path)
}

// printCleanup is the end of a cleanup: verdict, branch, files, reasons and
// the agent's decision log.
func printCleanup(out io.Writer, run pipeline.Run) {
	c := run.Cleanup
	if c == nil {
		return
	}
	verdict := map[string]string{pipeline.VerdictApproved: "aprovada", pipeline.VerdictRejected: "reprovada"}[c.Verdict]
	if verdict == "" {
		verdict = "sem veredito"
	}
	fmt.Fprintf(out, "Limpeza %s na área %s.\n", verdict, c.Area)
	if len(run.Stories) > 0 && run.Stories[0].Branch != "" {
		fmt.Fprintf(out, "Branch: %s (base %s).\n", run.Stories[0].Branch, c.BaseBranch)
	}
	if len(c.Changed) > 0 {
		fmt.Fprintf(out, "Arquivos alterados: %s.\n", strings.Join(c.Changed, ", "))
	}
	for _, r := range c.Reasons {
		fmt.Fprintf(out, "Motivo: %s\n", r)
	}
	if log := strings.TrimSpace(c.DecisionLog); log != "" {
		fmt.Fprintf(out, "\nRegistro de decisões:\n%s\n", log)
	}
}
