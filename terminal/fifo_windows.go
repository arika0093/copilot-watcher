//go:build windows

package terminal

import "fmt"

func mkfifo(_ string) error {
	return fmt.Errorf("mkfifo not supported on Windows")
}

// pollConsoleOutput stub is already defined in windows.go
