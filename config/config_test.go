package config

import (
	"os"
	"path/filepath"
	"testing"
)

func usePreferredLocale(t *testing.T, locale string) {
	t.Helper()
	prev := preferredLocaleFn
	preferredLocaleFn = func() string {
		return locale
	}
	t.Cleanup(func() {
		preferredLocaleFn = prev
	})
}

func useConfigPath(t *testing.T, path string) {
	t.Helper()
	prev := configPathFn
	configPathFn = func() (string, error) {
		return path, nil
	}
	t.Cleanup(func() {
		configPathFn = prev
	})
}

func TestDefaultConfig(t *testing.T) {
	usePreferredLocale(t, "")

	cfg := DefaultConfig()
	if cfg.Language != "Japanese" {
		t.Fatalf("Language = %q, want %q", cfg.Language, "Japanese")
	}
	if cfg.Format != "conversational" {
		t.Fatalf("Format = %q, want %q", cfg.Format, "conversational")
	}
}

func TestLoadReturnsDefaultWhenConfigMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	useConfigPath(t, path)
	usePreferredLocale(t, "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg != DefaultConfig() {
		t.Fatalf("Load() = %+v, want %+v", cfg, DefaultConfig())
	}
}

func TestLoadAppliesDefaultsToMissingFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	useConfigPath(t, path)
	usePreferredLocale(t, "")

	if err := os.WriteFile(path, []byte(`{"language":"English"}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Language != "English" {
		t.Fatalf("Language = %q, want %q", cfg.Language, "English")
	}
	if cfg.Format != "conversational" {
		t.Fatalf("Format = %q, want %q", cfg.Format, "conversational")
	}
}

func TestLoadReturnsDefaultOnDecodeError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	useConfigPath(t, path)
	usePreferredLocale(t, "")

	if err := os.WriteFile(path, []byte(`{"language":`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want decode error")
	}
	if cfg != DefaultConfig() {
		t.Fatalf("Load() = %+v, want %+v", cfg, DefaultConfig())
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	useConfigPath(t, path)
	usePreferredLocale(t, "")

	want := Config{Language: "English", Format: "bullets"}
	if err := Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}
