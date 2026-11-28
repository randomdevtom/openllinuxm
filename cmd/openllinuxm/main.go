// Command openllinuxm: a terminal coding assistant for local models (Quarry, Ollama, llama.cpp,
// LM Studio, vLLM, any OpenAI-compatible server on localhost).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/randomdevtom/openllinuxm/internal/agent"
	"github.com/randomdevtom/openllinuxm/internal/config"
	"github.com/randomdevtom/openllinuxm/internal/llm"
	"github.com/randomdevtom/openllinuxm/internal/tui"
)

var version = "dev"

func main() {
	url := flag.String("url", "", "server base URL, e.g. http://127.0.0.1:8090 (default: find one on localhost)")
	mdl := flag.String("m", "", "model id")
	dir := flag.String("C", "", "working directory (default: current)")
	think := flag.Bool("think", false, "turn reasoning on")
	yolo := flag.Bool("yolo", false, "run shell commands without asking (deletes and risky commands still ask)")
	list := flag.Bool("list", false, "list local servers and models, then exit")
	showVer := flag.Bool("version", false, "print the version")
	cont := flag.Bool("c", false, "continue the last session")
	compact := flag.Bool("compact", false, "with -c or -resume: compact the session (the model summarizes it) and continue the work")
	resume := flag.String("resume", "", "continue the session saved in this file")
	importLog := flag.String("import-log", "", "rebuild a session from the log, from this time on (RFC3339), then continue it")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "openLLinuxM %s: a terminal coding assistant for local models\n\nusage: openllinuxm [flags]\n\n", version)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVer {
		fmt.Println("openLLinuxM", version)
		return
	}
	cfg := config.Load()
	if *url != "" {
		cfg.URL = *url
	}
	if *mdl != "" {
		cfg.Model = *mdl
	}
	if *think {
		cfg.Think = true
	}
	if *list {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		var extra []string
		if cfg.URL != "" {
			extra = append(extra, cfg.URL)
		}
		for _, s := range llm.Discover(ctx, extra) {
			fmt.Printf("%s  %s  %s\n", s.Name, s.URL, s.Label)
			for _, m := range s.Models {
				fmt.Println("   ", m)
			}
		}
		return
	}
	wd, _ := os.Getwd()
	if *dir != "" {
		wd = agent.ExpandDir(*dir, wd)
	}
	a := agent.New(wd)
	a.Think, a.Yolo = cfg.Think, *yolo
	if *importLog != "" {
		since, err := time.Parse(time.RFC3339, *importLog)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: -import-log wants a time like 2026-11-26T17:01:41Z:", err)
			os.Exit(2)
		}
		home, _ := os.UserHomeDir()
		path, n, err := agent.ImportLog(filepath.Join(home, ".local", "state", "openllinuxm", "log.txt"), since)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "imported %d messages into %s\n", n, path)
		*resume = path
	}
	if *cont && *resume == "" {
		p, err := agent.LatestSession()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		*resume = p
	}
	if *resume != "" {
		if err := a.Load(*resume); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}
	// set colors from the environment: querying the terminal blocks startup and eats typed keys
	lipgloss.SetColorProfile(termenv.EnvColorProfile())
	lipgloss.SetHasDarkBackground(true)
	tui.Version = version
	if *compact {
		tui.AutoSubmit = "/compact"
	}
	m := tui.New(a, cfg)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	a.Send = func(v any) { p.Send(v) }
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
