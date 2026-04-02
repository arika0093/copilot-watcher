//go:build !windows

package terminal

import "fmt"

// startWindows is a no-op on non-Windows platforms.
func (r *Reader) startWindows() error {
	return fmt.Errorf("Windows console reading not available on this platform")
}
