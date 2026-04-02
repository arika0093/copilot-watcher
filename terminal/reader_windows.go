//go:build windows

package terminal

// startWindows starts the Windows console polling reader.
func (r *Reader) startWindows() error {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		pollConsoleOutput(r.pid, r.ch, r.done)
	}()
	return nil
}
