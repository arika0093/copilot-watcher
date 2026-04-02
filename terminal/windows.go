//go:build windows

package terminal

import (
	"fmt"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FindPTYMasterPath on Windows returns a special pseudo-path.
// Windows uses DuplicateHandle + ReadConsoleOutput polling instead of PTY.
func FindPTYMasterPath(pid int) (string, error) {
	// Verify process is accessible
	handle, err := windows.OpenProcess(windows.PROCESS_DUP_HANDLE|windows.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", fmt.Errorf("OpenProcess failed: %w", err)
	}
	windows.CloseHandle(handle)
	// Return a special indicator; actual reading in Reader uses Windows Console APIs
	return fmt.Sprintf("windows-console://%d", pid), nil
}

// consoleReader implements Windows console screen buffer polling
type consoleReader struct {
	pid        int
	prevBuffer []uint16
}

// readConsoleOutput reads current console screen buffer via DuplicateHandle approach.
// Returns new characters since last call (diff-based).
func (cr *consoleReader) readConsoleOutput() (string, error) {
	// Open target process
	srcProcess, err := windows.OpenProcess(
		windows.PROCESS_DUP_HANDLE, false, uint32(cr.pid))
	if err != nil {
		return "", fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(srcProcess)

	// Get stdout handle of target process (handle value 7 = STD_OUTPUT_HANDLE for typical console)
	// We use the well-known handle value approach
	const STD_OUTPUT_HANDLE = ^uintptr(10) // (DWORD)-11

	// Duplicate stdout handle from target process
	var dupHandle windows.Handle
	err = windows.DuplicateHandle(
		srcProcess,
		windows.Handle(STD_OUTPUT_HANDLE),
		windows.CurrentProcess(),
		&dupHandle,
		0, false,
		windows.DUPLICATE_SAME_ACCESS,
	)
	if err != nil {
		return "", fmt.Errorf("DuplicateHandle: %w", err)
	}
	defer windows.CloseHandle(dupHandle)

	// Get console screen buffer info
	var csbi windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(dupHandle, &csbi); err != nil {
		return "", fmt.Errorf("GetConsoleScreenBufferInfo: %w", err)
	}

	// Read the visible area of the console
	width := int(csbi.Window.Right - csbi.Window.Left + 1)
	height := int(csbi.Window.Bottom - csbi.Window.Top + 1)
	bufSize := width * height

	charBuf := make([]uint16, bufSize)
	var charsRead uint32

	readRegion := windows.SmallRect{
		Left:   csbi.Window.Left,
		Top:    csbi.Window.Top,
		Right:  csbi.Window.Right,
		Bottom: csbi.Window.Bottom,
	}

	coord := windows.Coord{X: csbi.Window.Left, Y: csbi.Window.Top}
	err = readConsoleOutputCharacterW(dupHandle, &charBuf[0], uint32(bufSize), coord, &charsRead, &readRegion)
	if err != nil {
		return "", fmt.Errorf("ReadConsoleOutputCharacter: %w", err)
	}

	current := charBuf[:charsRead]

	// Diff against previous snapshot
	var newChars []uint16
	minLen := len(cr.prevBuffer)
	if minLen > int(charsRead) {
		minLen = int(charsRead)
	}
	if len(cr.prevBuffer) == 0 {
		newChars = current
	} else if int(charsRead) > len(cr.prevBuffer) {
		newChars = current[len(cr.prevBuffer):]
	}
	cr.prevBuffer = make([]uint16, len(current))
	copy(cr.prevBuffer, current)

	if len(newChars) == 0 {
		return "", nil
	}
	// Convert UTF-16 to string
	runes := utf16.Decode(newChars)
	return string(runes), nil
}

// Polling loop - exported for use by Reader
func pollConsoleOutput(pid int, ch chan<- string, done <-chan struct{}) {
	cr := &consoleReader{pid: pid}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			text, err := cr.readConsoleOutput()
			if err != nil || text == "" {
				continue
			}
			select {
			case ch <- text:
			default:
			}
		}
	}
}

// Windows API shim
var (
	kernel32                        = windows.NewLazySystemDLL("kernel32.dll")
	procReadConsoleOutputCharacterW = kernel32.NewProc("ReadConsoleOutputCharacterW")
)

func readConsoleOutputCharacterW(console windows.Handle, buf *uint16, nLen uint32,
	coord windows.Coord, charsRead *uint32, region *windows.SmallRect) error {
	r1, _, err := procReadConsoleOutputCharacterW.Call(
		uintptr(console),
		uintptr(unsafe.Pointer(buf)),
		uintptr(nLen),
		uintptr(*(*uint32)(unsafe.Pointer(&coord))),
		uintptr(unsafe.Pointer(charsRead)),
		uintptr(unsafe.Pointer(region)),
	)
	if r1 == 0 {
		return err
	}
	return nil
}
