// Package bridge watches the claude session env directory for
// remote control bridge sessions. Claude writes one JSON file per
// active CLI session at ~/.claude/sessions/<pid>.json. When the user
// runs --remote-control or types /remote-control, claude adds a
// bridgeSessionId field to that file. The bridge URL on claude.ai is
// derived from that id.
//
// The watcher emits structured events whenever a bridge appears or
// disappears so the daemon can broadcast over its registry stream.
package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Bridge identifies one active claude --remote-control bridge.
type Bridge struct {
	SessionID       string
	PID             int64
	BridgeSessionID string
	URL             string
}

// EventKind tags the lifecycle stage of a bridge event.
type EventKind int

const (
	EventOpened EventKind = iota
	EventClosed
)

// Event carries one watcher notification.
type Event struct {
	Kind   EventKind
	Bridge Bridge
}

type watchSignal struct {
	Requested bool
}

// Watcher tracks the set of active bridges and emits Events on the
// returned channel for every change. Callers should drain the channel
// to keep the watcher responsive. Close() releases all resources.
type Watcher struct {
	dir   string
	notif *fsnotify.Watcher

	mu      sync.RWMutex
	current map[string]Bridge // pid file name to Bridge

	out  chan Event
	stop chan watchSignal
	done chan watchSignal
}

// Start opens the watcher rooted at dir (typically
// $HOME/.claude/sessions). A first scan seeds the current map and
// emits EventOpened for every existing bridge so the daemon picks up
// state created before the daemon started.
func Start(dir string) (*Watcher, error) {
	notif, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		notif.Close()
		return nil, fmt.Errorf("ensure session dir: %w", err)
	}
	if err := notif.Add(dir); err != nil {
		notif.Close()
		return nil, fmt.Errorf("watch %s: %w", dir, err)
	}
	w := &Watcher{
		dir:     dir,
		notif:   notif,
		current: make(map[string]Bridge),
		out:     make(chan Event, 64),
		stop:    make(chan watchSignal),
		done:    make(chan watchSignal),
	}
	go w.loop()
	go w.initialScan()
	return w, nil
}

// Events returns the channel of bridge events. The channel closes
// when the watcher stops.
func (w *Watcher) Events() <-chan Event { return w.out }

// Snapshot returns a copy of the current bridge map.
func (w *Watcher) Snapshot() []Bridge {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]Bridge, 0, len(w.current))
	for _, b := range w.current {
		out = append(out, b)
	}
	return out
}

// Close releases the fsnotify handle and waits for the watch loop to finish.
func Close(w *Watcher) {
	if w == nil {
		return
	}
	select {
	case <-w.stop:
		return
	default:
		close(w.stop)
	}
	w.notif.Close()
	<-w.done
}

func (w *Watcher) initialScan() {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		w.handleWrite(filepath.Join(w.dir, e.Name()))
	}
}

func (w *Watcher) loop() {
	defer close(w.done)
	defer close(w.out)
	for {
		select {
		case <-w.stop:
			return
		case ev, ok := <-w.notif.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(ev.Name, ".json") {
				continue
			}
			switch {
			case ev.Op&(fsnotify.Create|fsnotify.Write) != 0:
				w.handleWrite(ev.Name)
			case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
				w.handleRemove(ev.Name)
			}
		case _, ok := <-w.notif.Errors:
			if !ok {
				return
			}
		}
	}
}

// handleWrite parses the file and emits EventOpened when the file now
// carries a bridgeSessionId not previously seen, or when the bridge id
// changed under the same pid file. Files without a bridge id update
// the cache (so a later append can change them) but do not emit.
func (w *Watcher) handleWrite(path string) {
	bridge, ok := readBridge(path)
	key := filepath.Base(path)
	if !ok {
		// File exists but no bridge id; remove any cached entry so we
		// emit EventClosed if there was one previously.
		w.mu.Lock()
		prev, hadPrev := w.current[key]
		delete(w.current, key)
		w.mu.Unlock()
		if hadPrev && prev.BridgeSessionID != "" {
			w.out <- Event{Kind: EventClosed, Bridge: prev}
		}
		return
	}
	w.mu.Lock()
	prev, hadPrev := w.current[key]
	w.current[key] = bridge
	w.mu.Unlock()
	if hadPrev && prev.BridgeSessionID == bridge.BridgeSessionID {
		return
	}
	if hadPrev && prev.BridgeSessionID != "" && prev.BridgeSessionID != bridge.BridgeSessionID {
		w.out <- Event{Kind: EventClosed, Bridge: prev}
	}
	w.out <- Event{Kind: EventOpened, Bridge: bridge}
}

func (w *Watcher) handleRemove(path string) {
	key := filepath.Base(path)
	w.mu.Lock()
	prev, ok := w.current[key]
	delete(w.current, key)
	w.mu.Unlock()
	if !ok || prev.BridgeSessionID == "" {
		return
	}
	w.out <- Event{Kind: EventClosed, Bridge: prev}
}

// readBridge parses a session env file and returns a Bridge when the
// file contains a non empty bridgeSessionId. Returns ok=false for any
// other case (parse error, missing field, empty value).
func readBridge(path string) (Bridge, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Bridge{}, false
	}
	var raw struct {
		PID             int64  `json:"pid"`
		SessionID       string `json:"sessionId"`
		BridgeSessionID string `json:"bridgeSessionId"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Bridge{}, false
	}
	if raw.BridgeSessionID == "" || raw.SessionID == "" {
		return Bridge{}, false
	}
	return Bridge{
		SessionID:       raw.SessionID,
		PID:             raw.PID,
		BridgeSessionID: raw.BridgeSessionID,
		URL:             "https://claude.ai/code/" + raw.BridgeSessionID,
	}, true
}
