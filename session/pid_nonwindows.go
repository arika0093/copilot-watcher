//go:build !windows

package session

func isPIDAliveWindows(_ int) bool {
	return false
}
