// openLLinuxM: a terminal coding assistant for local models (Quarry, Ollama, llama.cpp,
// LM Studio, vLLM, any OpenAI-compatible server on localhost).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var version = "dev"

type Config struct {
	URL          string `json:"url,omitempty"`
	Model        string `json:"model,omitempty"`
	Think        bool   `json:"think"`
	ShowThinking bool   `json:"show_thinking"`
	path         string
}

func loadConfig() *Config {
	c := &Config{}
	dir, err := os.UserConfigDir()
	if err != nil {
		return c
	}
	c.path = filepath.Join(dir, "openllinuxm", "config.json")
	if b, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(b, c)
	}
	return c
}

func (c *Config) Save() {
	if c.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(c.path), 0o755)
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(c.path, b, 0o644)
}

func expandDir(p, base string) string {
	if strings.HasPrefix(p, "~") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

func main() {
	url := flag.String("url", "", "server base URL, e.g. http://127.0.0.1:8090 (default: find one on localhost)")
	mdl := flag.String("m", "", "model id")
	dir := flag.String("C", "", "working directory (default: current)")
	think := flag.Bool("think", false, "turn reasoning on")
	yolo := flag.Bool("yolo", false, "run shell commands without asking (deletes and risky commands still ask)")
	list := flag.Bool("list", false, "list local servers and models, then exit")
	showVer := flag.Bool("version", false, "print the version")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "openLLinuxM %s: a terminal coding assistant for local models\n\nusage: openllinuxm [flags]\n\n", version)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVer {
		fmt.Println("openLLinuxM", version)
		return
	}
	cfg := loadConfig()
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
		for _, s := range Discover(ctx, extra) {
			fmt.Printf("%s  %s  %s\n", s.Name, s.URL, s.Label)
			for _, m := range s.Models {
				fmt.Println("   ", m)
			}
		}
		return
	}
	wd, _ := os.Getwd()
	if *dir != "" {
		wd = expandDir(*dir, wd)
	}
	a := NewAgent(wd)
	a.think, a.yolo = cfg.Think, *yolo
	// set colors from the environment: querying the terminal blocks startup and eats typed keys
	lipgloss.SetColorProfile(termenv.EnvColorProfile())
	lipgloss.SetHasDarkBackground(true)
	m := newModel(a, cfg)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	a.send = p.Send
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
