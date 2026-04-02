//go:build !linux && !darwin && !windows

package terminal

import "fmt"

func FindPTYMasterPath(_ int) (string, error) {
	return "", fmt.Errorf("PTY master reading not supported on this platform")
}

func pollConsoleOutput(_ int, _ chan<- TerminalMsg, _ <-chan struct{}) {}
