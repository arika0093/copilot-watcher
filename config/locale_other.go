//go:build !windows

package config

func platformPreferredLocale() string {
	return ""
}
