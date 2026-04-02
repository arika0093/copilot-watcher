package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

const sessionWatchDebounce = 250 * time.Millisecond

// StateWatcher monitors the Copilot session-state tree and emits a signal when
// the session list should be refreshed.
type StateWatcher struct {
	rootDir   string
	stateDir  string
	fw        *fsnotify.Watcher
	changesCh chan struct{}
	errorsCh  chan error
	done      chan struct{}
	watched   map[string]struct{}
}

// NewStateWatcher creates a watcher for ~/.copilot/session-state.
func NewStateWatcher() (*StateWatcher, error) {
	rootDir, err := copilotDirPath()
	if err != nil {
		return nil, err
	}
	return &StateWatcher{
		rootDir:   rootDir,
		stateDir:  filepath.Join(rootDir, sessionStateDirName),
		changesCh: make(chan struct{}, 1),
		errorsCh:  make(chan error, 1),
		done:      make(chan struct{}),
		watched:   make(map[string]struct{}),
	}, nil
}

// Changes returns the refresh signal channel.
func (w *StateWatcher) Changes() <-chan struct{} { return w.changesCh }

// Errors returns watcher errors.
func (w *StateWatcher) Errors() <-chan error { return w.errorsCh }

// Start begins monitoring the Copilot session-state tree.
func (w *StateWatcher) Start() error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.fw = fw

	if err := w.addRootWatch(); err != nil {
		fw.Close()
		w.fw = nil
		return err
	}
	if err := w.syncSessionWatches(); err != nil {
		fw.Close()
		w.fw = nil
		return err
	}

	go w.loop()
	return nil
}

// Stop shuts down the watcher.
func (w *StateWatcher) Stop() {
	select {
	case <-w.done:
		return
	default:
		close(w.done)
	}
	if w.fw != nil {
		_ = w.fw.Close()
	}
}

func (w *StateWatcher) addRootWatch() error {
	info, err := os.Stat(w.rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", w.rootDir)
	}
	return w.fw.Add(w.rootDir)
}

func (w *StateWatcher) syncSessionWatches() error {
	desired := make(map[string]struct{})

	info, err := os.Stat(w.stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			for path := range w.watched {
				_ = w.fw.Remove(path)
				delete(w.watched, path)
			}
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", w.stateDir)
	}

	desired[w.stateDir] = struct{}{}
	entries, err := os.ReadDir(w.stateDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		desired[filepath.Join(w.stateDir, entry.Name())] = struct{}{}
	}

	for path := range desired {
		if _, ok := w.watched[path]; ok {
			continue
		}
		if err := w.fw.Add(path); err != nil {
			return err
		}
		w.watched[path] = struct{}{}
	}

	for path := range w.watched {
		if _, ok := desired[path]; ok {
			continue
		}
		_ = w.fw.Remove(path)
		delete(w.watched, path)
	}

	return nil
}

func (w *StateWatcher) loop() {
	flushCh := make(chan struct{}, 1)
	var debounceTimer *time.Timer

	resetDebounce := func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.AfterFunc(sessionWatchDebounce, func() {
			select {
			case flushCh <- struct{}{}:
			default:
			}
		})
	}

	sendError := func(err error) {
		select {
		case w.errorsCh <- err:
		default:
		}
	}

	sendChange := func() {
		select {
		case w.changesCh <- struct{}{}:
		default:
		}
	}

	for {
		select {
		case <-w.done:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case evt, ok := <-w.fw.Events:
			if !ok {
				return
			}
			if evt.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if !w.isRelevantPath(evt.Name) {
				continue
			}
			if err := w.syncSessionWatches(); err != nil {
				sendError(err)
				continue
			}
			resetDebounce()

		case <-flushCh:
			sendChange()

		case err, ok := <-w.fw.Errors:
			if !ok || err == nil {
				continue
			}
			sendError(err)
		}
	}
}

func (w *StateWatcher) isRelevantPath(path string) bool {
	cleaned := filepath.Clean(path)
	if cleaned == w.stateDir {
		return true
	}
	statePrefix := w.stateDir + string(os.PathSeparator)
	if strings.HasPrefix(cleaned, statePrefix) {
		return true
	}
	return filepath.Dir(cleaned) == w.rootDir && filepath.Base(cleaned) == sessionStateDirName
}
