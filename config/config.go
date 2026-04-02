package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	Language string `json:"language"`
	Format   string `json:"format"`
}

func DefaultConfig() Config {
	return Config{Language: defaultLanguage(), Format: "conversational"}
}

var configPathFn = configPath

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	cfgdir := filepath.Join(home, ".config", "copilot-watcher")
	if err := os.MkdirAll(cfgdir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(cfgdir, "config.json"), nil
}

func Load() (Config, error) {
	defaults := DefaultConfig()
	p, err := configPathFn()
	if err != nil {
		return defaults, err
	}
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return defaults, nil
		}
		return defaults, err
	}
	defer f.Close()
	var c Config
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		return defaults, err
	}
	if c.Language == "" {
		c.Language = defaults.Language
	}
	if c.Format == "" {
		c.Format = defaults.Format
	}
	return c, nil
}

func Save(c Config) error {
	p, err := configPathFn()
	if err != nil {
		return err
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}
