package config

import (
	"os"
	"strings"

	"golang.org/x/text/language"
)

var (
	envLookupFn       = os.Getenv
	preferredLocaleFn = preferredLocale
	localeEnvKeys     = []string{"LC_ALL", "LC_MESSAGES", "LANGUAGE", "LANG"}
)

func defaultLanguage() string {
	lang := languageFromLocale(preferredLocaleFn())
	if lang == "" || lang == "English" {
		return "Japanese"
	}
	return lang
}

func preferredLocale() string {
	for _, key := range localeEnvKeys {
		if value := strings.TrimSpace(envLookupFn(key)); value != "" {
			return value
		}
	}
	return strings.TrimSpace(platformPreferredLocale())
}

func languageFromLocale(raw string) string {
	normalized := normalizeLocale(raw)
	if normalized == "" {
		return ""
	}

	tag, err := language.Parse(normalized)
	if err == nil {
		if base, conf := tag.Base(); conf != language.No {
			switch base.String() {
			case "ja":
				return "Japanese"
			case "zh":
				return "Chinese"
			case "ko":
				return "Korean"
			case "es":
				return "Spanish"
			case "fr":
				return "French"
			case "de":
				return "German"
			case "en":
				return "English"
			}
		}
	}

	lower := strings.ToLower(normalized)
	switch {
	case strings.HasPrefix(lower, "ja"):
		return "Japanese"
	case strings.HasPrefix(lower, "zh"):
		return "Chinese"
	case strings.HasPrefix(lower, "ko"):
		return "Korean"
	case strings.HasPrefix(lower, "es"):
		return "Spanish"
	case strings.HasPrefix(lower, "fr"):
		return "French"
	case strings.HasPrefix(lower, "de"):
		return "German"
	case strings.HasPrefix(lower, "en"):
		return "English"
	default:
		return ""
	}
}

func normalizeLocale(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.SplitN(trimmed, ".", 2)[0]
	trimmed = strings.SplitN(trimmed, "@", 2)[0]
	trimmed = strings.ReplaceAll(trimmed, "_", "-")
	return trimmed
}
