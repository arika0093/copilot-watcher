package config

import "testing"

func TestLanguageFromLocale(t *testing.T) {
	tests := []struct {
		locale string
		want   string
	}{
		{locale: "ja_JP.UTF-8", want: "Japanese"},
		{locale: "es-ES", want: "Spanish"},
		{locale: "fr_FR", want: "French"},
		{locale: "en-US", want: "English"},
		{locale: "unknown", want: ""},
	}

	for _, tt := range tests {
		got := languageFromLocale(tt.locale)
		if got != tt.want {
			t.Fatalf("languageFromLocale(%q) = %q, want %q", tt.locale, got, tt.want)
		}
	}
}

func TestDefaultLanguageFallsBackToJapaneseForUnknownOrEnglish(t *testing.T) {
	usePreferredLocale(t, "en-US")
	if got := defaultLanguage(); got != "Japanese" {
		t.Fatalf("defaultLanguage() with English locale = %q, want %q", got, "Japanese")
	}

	usePreferredLocale(t, "")
	if got := defaultLanguage(); got != "Japanese" {
		t.Fatalf("defaultLanguage() with empty locale = %q, want %q", got, "Japanese")
	}
}

func TestDefaultLanguageUsesDetectedLocaleWhenSupported(t *testing.T) {
	usePreferredLocale(t, "es-ES")
	if got := defaultLanguage(); got != "Spanish" {
		t.Fatalf("defaultLanguage() = %q, want %q", got, "Spanish")
	}
}
