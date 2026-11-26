// Package config loads and saves ~/.config/openllinuxm/config.json.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is what openLLinuxM remembers between runs.
type Config struct {
	URL          string `json:"url,omitempty"`
	Model        string `json:"model,omitempty"`
	Think        bool   `json:"think"`
	ShowThinking bool   `json:"show_thinking"`
	path         string
}

// Load reads the config file; a missing or broken file gives an empty config.
func Load() *Config {
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

// Save writes the config file (errors are ignored: the config is only a convenience).
func (c *Config) Save() {
	if c.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(c.path), 0o755)
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(c.path, b, 0o644)
}
