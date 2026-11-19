//go:build !desktop

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"llm-orchestrator/internal/settings"
)

var tuiTabs = []struct {
	name           string
	help           string
	defaultCommand string
}{
	{"Execução", "status | resume | retry --story 0 --step review | stop | commit show --id HASH", "status"},
	{"Nova execução", "stories parse --file US.md | run --file US.md [--story US-1] [--real]", ""},
	{"Repositório", "repo show | repo branches | repo base --path . --id main | policy show/apply --file politica.json", "repo show"},
	{"Provedores", "providers list | config show/apply --file config.json | providers exhaust/restore --id claude", "providers list"},
	{"Prompts", "prompts list/show --id CHAVE | prompts set --id CHAVE --file prompt.txt | prompts reset --id CHAVE", "prompts list"},
	{"Logs", "logs | status --json", "logs"},
}

type tuiOutput struct{ line string }
type tuiDone struct {
	code int
	repo string
}

type tuiModel struct {
	repo, config string
	tab          int
	input        string
	lines        []string
	width        int
	height       int
	busy         int
	scroll       int
	events       chan tea.Msg
}

func newTUIModel(repo, config string) tuiModel {
	return tuiModel{repo: repo, config: config, width: 80, height: 24, events: make(chan tea.Msg, 256),
		lines: []string{"FazTudo pronto. Digite um comando abaixo."}}
}

func (m tuiModel) Init() tea.Cmd { return m.listen() }

func (m tuiModel) listen() tea.Cmd {
	return func() tea.Msg { return <-m.events }
}

func (m tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tuiOutput:
		m.lines = append(m.lines, msg.line)
		if len(m.lines) > 5000 {
			m.lines = m.lines[len(m.lines)-5000:]
		}
		if m.scroll > 0 {
			m.scroll++
		}
		return m, m.listen()
	case tuiDone:
		if m.busy > 0 {
			m.busy--
		}
		if msg.code == 0 && msg.repo != "" {
			m.repo = msg.repo
		}
		m.lines = append(m.lines, fmt.Sprintf("Comando finalizado (código %d).", msg.code))
		return m, m.listen()
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.busy > 0 {
				m.input = "stop"
				return m.launch()
			}
			return m, tea.Quit
		case tea.KeyCtrlL:
			m.lines = nil
			m.scroll = 0
		case tea.KeyTab:
			m.tab = (m.tab + 1) % len(tuiTabs)
		case tea.KeyUp, tea.KeyPgUp:
			amount := 1
			if msg.Type == tea.KeyPgUp {
				amount = max(3, m.height-10)
			}
			m.scroll = min(len(m.lines), m.scroll+amount)
		case tea.KeyDown, tea.KeyPgDown:
			amount := 1
			if msg.Type == tea.KeyPgDown {
				amount = max(3, m.height-10)
			}
			m.scroll = max(0, m.scroll-amount)
		case tea.KeyEnter:
			if m.input == "" {
				m.input = tuiTabs[m.tab].defaultCommand
			}
			return m.launch()
		case tea.KeyBackspace, tea.KeyDelete:
			if m.input != "" {
				_, size := utf8.DecodeLastRuneInString(m.input)
				m.input = m.input[:len(m.input)-size]
			}
		case tea.KeyRunes:
			m.input += string(msg.Runes)
		}
	}
	return m, nil
}

func (m tuiModel) launch() (tea.Model, tea.Cmd) {
	words, err := splitTUIWords(m.input)
	if err != nil {
		m.lines = append(m.lines, "Erro: "+err.Error())
		return m, nil
	}
	if len(words) == 0 {
		return m, nil
	}
	if words[0] == "tui" {
		m.lines = append(m.lines, "A interface interativa já está aberta.")
		return m, nil
	}
	if m.repo != "" && !containsTUIArg(words, "--repo") && words[0] != "help" {
		words = append(words, "--repo", m.repo)
	}
	if !containsTUIArg(words, "--config") && words[0] != "help" {
		words = append(words, "--config", m.config)
	}
	m.lines = append(m.lines, "> "+m.input)
	m.input = ""
	m.busy++
	nextRepo := ""
	if len(words) >= 2 && words[0] == "repo" && words[1] == "set" {
		for i := 2; i < len(words); i++ {
			if words[i] == "--path" {
				if i+1 < len(words) {
					nextRepo = words[i+1]
				}
			} else if strings.HasPrefix(words[i], "--path=") {
				nextRepo = strings.TrimPrefix(words[i], "--path=")
			}
		}
	}
	return m, func() tea.Msg {
		writer := &tuiStreamWriter{events: m.events}
		code := runConsole(words, strings.NewReader(""), writer, writer)
		writer.flush()
		m.events <- tuiDone{code: code, repo: nextRepo}
		return nil
	}
}

func (m tuiModel) View() string {
	active := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	var tabs []string
	for i, tab := range tuiTabs {
		name := tab.name
		if i == m.tab {
			name = active.Render("[" + name + "]")
		}
		tabs = append(tabs, name)
	}
	maxLines := m.height - 8
	if maxLines < 3 {
		maxLines = 3
	}
	end := max(0, len(m.lines)-m.scroll)
	start := max(0, end-maxLines)
	lines := m.lines[start:end]
	status := "pronto"
	if m.busy > 0 {
		status = fmt.Sprintf("%d comando(s) em andamento", m.busy)
	}
	return strings.Join([]string{
		active.Render("FazTudo no console") + "  " + status,
		strings.Join(tabs, "  "),
		strings.Repeat("─", max(1, min(m.width, 80))),
		tuiTabs[m.tab].help,
		"",
		strings.Join(lines, "\n"),
		"",
		"> " + m.input + "█",
		"Tab: seção · ↑/↓: rolar · Ctrl+L: limpar · Enter: executar · Ctrl+C: parar/fechar",
	}, "\n")
}

type tuiStreamWriter struct {
	mu     sync.Mutex
	events chan tea.Msg
	buffer string
}

func (w *tuiStreamWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer += string(data)
	for index := strings.IndexByte(w.buffer, '\n'); index >= 0; index = strings.IndexByte(w.buffer, '\n') {
		w.events <- tuiOutput{line: w.buffer[:index]}
		w.buffer = w.buffer[index+1:]
	}
	return len(data), nil
}

func (w *tuiStreamWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buffer != "" {
		w.events <- tuiOutput{line: w.buffer}
		w.buffer = ""
	}
}

func containsTUIArg(words []string, name string) bool {
	for _, word := range words {
		if word == name || strings.HasPrefix(word, name+"=") {
			return true
		}
	}
	return false
}

func splitTUIWords(s string) ([]string, error) {
	var words []string
	var word strings.Builder
	quote := rune(0)
	escaped := false
	started := false
	for _, r := range s {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
			started = true
		case r == '\\':
			escaped = true
			started = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t':
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("aspas ou escape sem fechamento")
	}
	if started {
		words = append(words, word.String())
	}
	return words, nil
}

func consoleTUI(args []string, input io.Reader, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(errOut)
	repo := fs.String("repo", os.Getenv("REPO_PATH"), "repositório alvo")
	config := fs.String("config", envOr("FAZTUDO_CONFIG", settings.DefaultPath()), "arquivo de configuração")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	program := tea.NewProgram(newTUIModel(*repo, *config), tea.WithInput(input), tea.WithOutput(out), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(errOut, "erro na interface de terminal:", err)
		return 1
	}
	return 0
}
