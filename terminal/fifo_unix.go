//go:build linux || darwin

package terminal

import "syscall"

func mkfifo(path string) error {
	return syscall.Mkfifo(path, 0600)
}
