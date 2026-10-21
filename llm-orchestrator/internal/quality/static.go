package quality

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Finding is a problem a static check found in one file.
type Finding struct {
	Check   string `json:"check"` // readability | syntax | format
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// codeExts are the files the readability check inspects. Data, docs and
// lockfiles legitimately have long lines.
var codeExts = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".jsx": true, ".mts": true,
	".vue": true, ".svelte": true, ".html": true, ".htm": true, ".css": true, ".scss": true, ".less": true,
	".go": true, ".py": true, ".java": true, ".kt": true, ".rb": true, ".php": true, ".cs": true,
	".rs": true, ".swift": true, ".dart": true, ".sh": true,
}

// minifiedFactor marks a file as minified when a line is this many times
// longer than the limit.
const minifiedFactor = 4

// Readability flags hand-written code files with lines longer than maxLine,
// which in practice means minified or machine-packed code nobody can review.
func Readability(root string, files []string, maxLine int) []Finding {
	var out []Finding
	for _, rel := range files {
		if !codeExts[ext(rel)] || strings.Contains(filepath.Base(rel), ".min.") {
			continue
		}
		text, ok := readFile(root, rel)
		if !ok {
			continue
		}
		long, longest, at := 0, 0, 0
		for i, line := range strings.Split(text, "\n") {
			n := len([]rune(strings.TrimRight(line, "\r")))
			if n > maxLine {
				long++
				if n > longest {
					longest, at = n, i+1
				}
			}
		}
		if long == 0 {
			continue
		}
		msg := fmt.Sprintf("%d linha(s) acima de %d caracteres (maior: %d, linha %d)", long, maxLine, longest, at)
		if longest > minifiedFactor*maxLine {
			msg += "; o código parece minificado — reescreva em linhas curtas e funções legíveis"
		}
		out = append(out, Finding{Check: "readability", File: rel, Line: at, Message: msg})
	}
	return out
}

var (
	reScript     = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	reScriptType = regexp.MustCompile(`(?i)\btype\s*=\s*["']?([^"'\s>]+)`)
	reNodeLine   = regexp.MustCompile(`:(\d+)\r?\n`)
)

// Syntax parses changed JavaScript files and inline <script> blocks of
// changed HTML files with `node --check`. It is a no-op without Node.
func Syntax(ctx context.Context, root string, files []string) []Finding {
	node, err := exec.LookPath("node")
	if err != nil {
		return nil
	}
	tmp, err := os.MkdirTemp("", "faztudo-syntax-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(tmp)

	var out []Finding
	for _, rel := range files {
		switch ext(rel) {
		case ".js", ".mjs", ".cjs":
			if msg, line, ok := nodeCheck(ctx, node, filepath.Join(root, rel)); !ok {
				out = append(out, Finding{Check: "syntax", File: rel, Line: line, Message: msg})
			}
		case ".html", ".htm":
			text, ok := readFile(root, rel)
			if !ok {
				continue
			}
			for i, m := range reScript.FindAllStringSubmatchIndex(text, -1) {
				attrs, body := text[m[2]:m[3]], text[m[4]:m[5]]
				kind, ok := scriptKind(attrs)
				if !ok || strings.TrimSpace(body) == "" {
					continue
				}
				file := filepath.Join(tmp, fmt.Sprintf("inline-%d%s", i, kind))
				if os.WriteFile(file, []byte(body), 0o600) != nil {
					continue
				}
				if msg, line, ok := nodeCheck(ctx, node, file); !ok {
					offset := strings.Count(text[:m[4]], "\n")
					out = append(out, Finding{
						Check: "syntax", File: rel, Line: line + offset,
						Message: fmt.Sprintf("script inline nº %d: %s", i+1, msg),
					})
				}
			}
		}
	}
	return out
}

// scriptKind returns the temp-file extension for an inline script, or false
// for external scripts and non-JavaScript blocks (JSON, templates).
func scriptKind(attrs string) (string, bool) {
	if strings.Contains(strings.ToLower(attrs), "src=") {
		return "", false
	}
	m := reScriptType.FindStringSubmatch(attrs)
	if m == nil {
		return ".cjs", true
	}
	switch t := strings.ToLower(m[1]); t {
	case "module":
		return ".mjs", true
	case "text/javascript", "application/javascript", "text/ecmascript", "application/ecmascript":
		return ".cjs", true
	}
	return "", false
}

// nodeCheck reports the first error line and its line number when the file
// does not parse.
func nodeCheck(ctx context.Context, node, file string) (string, int, bool) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, node, "--check", file)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if cmd.Run() == nil {
		return "", 0, true
	}
	text := buf.String()
	line := 0
	if m := reNodeLine.FindStringSubmatch(text); m != nil {
		line, _ = strconv.Atoi(m[1])
	}
	msg := "erro de sintaxe"
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "Error") {
			msg = strings.TrimSpace(l)
			break
		}
	}
	return msg, line, false
}

// Format lists changed Go files that gofmt would rewrite. It is a no-op
// without gofmt.
func Format(ctx context.Context, root string, files []string) []Finding {
	gofmt, err := exec.LookPath("gofmt")
	if err != nil {
		return nil
	}
	var goFiles []string
	for _, rel := range files {
		if ext(rel) == ".go" {
			goFiles = append(goFiles, rel)
		}
	}
	if len(goFiles) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, gofmt, append([]string{"-l"}, goFiles...)...)
	cmd.Dir = root
	data, err := cmd.Output()
	if err != nil {
		return nil
	}
	var out []Finding
	for _, f := range strings.Fields(string(data)) {
		out = append(out, Finding{Check: "format", File: filepath.ToSlash(f), Message: "arquivo fora do padrão do gofmt"})
	}
	return out
}
