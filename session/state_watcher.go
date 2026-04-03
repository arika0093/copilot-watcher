package session

import (
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const sessionWatchDebounce = 250 * time.Millisecond

// StateWatcher monitors supported local session stores and emits a signal when
// the session list should be refreshed.
type StateWatcher struct {
	fw        *fsnotify.Watcher
	changesCh chan struct{}
	errorsCh  chan error
	done      chan struct{}
	watched   map[string]struct{}
}

// NewStateWatcher creates a watcher for all supported local session stores.
func NewStateWatcher() (*StateWatcher, error) {
	return &StateWatcher{
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

// Start begins monitoring all supported session stores.
func (w *StateWatcher) Start() error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.fw = fw

	if err := w.syncWatches(); err != nil {
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

func (w *StateWatcher) syncWatches() error {
	desired, err := desiredStateWatchPaths()
	if err != nil {
		return err
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

func desiredStateWatchPaths() (map[string]struct{}, error) {
	paths := make(map[string]struct{})
	addDir := func(path string) {
		if path == "" {
			return
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return
		}
		paths[filepath.Clean(path)] = struct{}{}
	}

	rootDir, err := copilotDirPath()
	if err == nil {
		addDir(rootDir)
		stateDir := filepath.Join(rootDir, sessionStateDirName)
		addDir(stateDir)
		entries, readErr := os.ReadDir(stateDir)
		if readErr == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					addDir(filepath.Join(stateDir, entry.Name()))
				}
			}
		}
	}

	editorRoots, err := editorStorageRootsFn()
	if err != nil {
		return nil, err
	}

	for _, root := range editorRoots {
		addDir(root.WorkspaceStoragePath)
		workspaceEntries, readErr := os.ReadDir(root.WorkspaceStoragePath)
		if readErr == nil {
			for _, entry := range workspaceEntries {
				if !entry.IsDir() {
					continue
				}
				workspaceDir := filepath.Join(root.WorkspaceStoragePath, entry.Name())
				addDir(workspaceDir)
				addDir(filepath.Join(workspaceDir, "chatSessions"))
			}
		}

		addDir(root.GlobalStoragePath)
		addDir(filepath.Join(root.GlobalStoragePath, "emptyWindowChatSessions"))
		addDir(filepath.Join(root.GlobalStoragePath, "github.copilot-chat"))
		addDir(filepath.Join(root.GlobalStoragePath, "github.copilot-chat", "sessions"))
		addDir(filepath.Join(root.GlobalStoragePath, "github.copilot-chat", "history"))
	}

	return paths, nil
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
			if err := w.syncWatches(); err != nil {
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
