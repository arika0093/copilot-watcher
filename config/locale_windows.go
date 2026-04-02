//go:build windows

package config

import (
	"strings"

	"golang.org/x/sys/windows"
)

func platformPreferredLocale() string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil {
		return ""
	}
	for _, lang := range langs {
		if trimmed := strings.TrimSpace(lang); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
