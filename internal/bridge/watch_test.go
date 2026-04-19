package bridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// drainEvents pulls every event currently in the channel without
// blocking past timeout. Used to assert the watcher's emitted set.
func drainEvents(ch <-chan Event, timeout time.Duration) []Event {
	var out []Event
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
}

func TestWatcherInitialScanPicksUpExistingBridges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "12345.json")
	if err := os.WriteFile(path, []byte(`{"pid":12345,"sessionId":"abc","bridgeSessionId":"session_xyz"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer Close(w)

	got := drainEvents(w.Events(), 200*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].Kind != EventOpened {
		t.Fatalf("expected EventOpened, got %v", got[0].Kind)
	}
	if got[0].Bridge.SessionID != "abc" || got[0].Bridge.BridgeSessionID != "session_xyz" {
		t.Fatalf("unexpected bridge: %+v", got[0].Bridge)
	}
	if got[0].Bridge.URL != "https://claude.ai/code/session_xyz" {
		t.Fatalf("URL not derived: %s", got[0].Bridge.URL)
	}
}

func TestWatcherEmitsCloseOnRemove(t *testing.T) {
	dir := t.TempDir()
	w, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer Close(w)

	path := filepath.Join(dir, "555.json")
	if err := os.WriteFile(path, []byte(`{"pid":555,"sessionId":"sid","bridgeSessionId":"session_aaa"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait for the open event before removing so the test does not
	// race the initial scan with the create event.
	time.Sleep(150 * time.Millisecond)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	events := drainEvents(w.Events(), 300*time.Millisecond)
	var sawOpen, sawClose bool
	for _, ev := range events {
		switch ev.Kind {
		case EventOpened:
			sawOpen = true
		case EventClosed:
			sawClose = true
		}
	}
	if !sawOpen || !sawClose {
		t.Fatalf("expected open and close, got %+v", events)
	}
}

func TestWatcherIgnoresFilesWithoutBridgeID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.json"), []byte(`{"pid":1,"sessionId":"sid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer Close(w)

	got := drainEvents(w.Events(), 200*time.Millisecond)
	if len(got) != 0 {
		t.Fatalf("expected no events, got %+v", got)
	}
}

func TestSnapshotReflectsCurrentBridges(t *testing.T) {
	dir := t.TempDir()
	w, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer Close(w)

	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(`{"pid":1,"sessionId":"a","bridgeSessionId":"session_a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2.json"), []byte(`{"pid":2,"sessionId":"b","bridgeSessionId":"session_b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	snap := w.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 bridges in snapshot, got %d (%v)", len(snap), snap)
	}
}
