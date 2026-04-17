// Tailing primitives for live transcript streaming.
//
// The Tailer opens a JSONL transcript file at a given byte offset,
// emits one TailLine per JSONL line written after that offset, and
// keeps watching until Close. The implementation uses fsnotify on the
// file plus its parent directory so renames and creates also wake the
// reader.
package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// TailLine is one parsed line streamed by Tailer. Role and Text are
// best effort: the Tailer parses the JSONL entry once and leaves the
// raw bytes available for callers that need the full payload.
type TailLine struct {
	ByteOffset int64
	RawJSONL   string
	Role       string
	Text       string
	Timestamp  time.Time
}

// Tailer streams new lines from a JSONL transcript.
type Tailer struct {
	path   string
	offset int64

	notif *fsnotify.Watcher
	out   chan TailLine

	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

// OpenTailer starts tailing path beginning at the given byte offset.
// startOffset = -1 seeks to the current end of file so only future
// lines arrive. startOffset = 0 streams the entire file from the
// start. Any positive value resumes from that exact byte position
// (typically obtained from a previous TailLine.ByteOffset + len).
func OpenTailer(path string, startOffset int64) (*Tailer, error) {
	notif, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}
	if err := notif.Add(filepath.Dir(path)); err != nil {
		notif.Close()
		return nil, fmt.Errorf("watch dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		_ = notif.Add(path)
	}

	t := &Tailer{
		path:   path,
		notif:  notif,
		out:    make(chan TailLine, 64),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	if startOffset < 0 {
		if info, err := os.Stat(path); err == nil {
			t.offset = info.Size()
		}
	} else {
		t.offset = startOffset
	}
	go t.loop()
	return t, nil
}

// Lines returns the channel of streamed lines. The channel closes
// when the Tailer stops.
func (t *Tailer) Lines() <-chan TailLine { return t.out }

// Close stops the tailer goroutine and releases the fsnotify handle.
// Safe to call more than once.
func (t *Tailer) Close() {
	t.once.Do(func() {
		close(t.stopCh)
		t.notif.Close()
	})
	<-t.doneCh
}

func (t *Tailer) loop() {
	defer close(t.doneCh)
	defer close(t.out)
	// Drain any existing content past the offset on startup so
	// subscribers immediately see the most recent lines.
	t.drain()
	for {
		select {
		case <-t.stopCh:
			return
		case ev, ok := <-t.notif.Events:
			if !ok {
				return
			}
			if ev.Name != t.path && filepath.Base(ev.Name) != filepath.Base(t.path) {
				continue
			}
			switch {
			case ev.Op&fsnotify.Create != 0:
				// Re-add in case the file was rotated.
				_ = t.notif.Add(t.path)
				t.offset = 0
				t.drain()
			case ev.Op&fsnotify.Write != 0:
				t.drain()
			case ev.Op&fsnotify.Remove != 0:
				// File rotated. Wait for the next Create event.
			}
		case _, ok := <-t.notif.Errors:
			if !ok {
				return
			}
		}
	}
}

// drain reads from t.offset to the current end of file, parses each
// complete JSONL line, and emits a TailLine. Trailing partial lines
// are left in place so the next event picks them up.
func (t *Tailer) drain() {
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			tl := parseTailLine(t.offset, line[:len(line)-1])
			t.offset += int64(len(line))
			select {
			case t.out <- tl:
			case <-t.stopCh:
				return
			}
		}
		if err != nil {
			// io.EOF is the normal exit. Partial lines without a
			// trailing newline are left for the next event.
			return
		}
	}
}

// parseTailLine extracts role, text, and timestamp from a single
// JSONL entry without failing on shape variations the Tailer does not
// recognise. The raw payload is always preserved.
func parseTailLine(offset int64, raw string) TailLine {
	tl := TailLine{ByteOffset: offset, RawJSONL: raw}
	var entry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return tl
	}
	if entry.Timestamp != "" {
		if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
			tl.Timestamp = ts
		}
	}
	if entry.Message.Role != "" {
		tl.Role = entry.Message.Role
	} else {
		tl.Role = entry.Type
	}
	tl.Text = extractText(entry.Message.Content)
	return tl
}

// extractText flattens the Message.Content into a single readable
// string. Text blocks join with newlines. Tool blocks become short
// stand ins so the sidecar shows something instead of nothing. Image
// blocks collapse to "[image]" so the renderer never tries to draw a
// raw payload.
func extractText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	// Plain string content.
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	// Array of typed blocks.
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	var out []byte
	for i, b := range blocks {
		var bt string
		_ = json.Unmarshal(b["type"], &bt)
		switch bt {
		case "text":
			var s string
			_ = json.Unmarshal(b["text"], &s)
			if i > 0 {
				out = append(out, '\n')
			}
			out = append(out, s...)
		case "image":
			if i > 0 {
				out = append(out, ' ')
			}
			out = append(out, "[image]"...)
		case "tool_use":
			var name string
			_ = json.Unmarshal(b["name"], &name)
			if i > 0 {
				out = append(out, ' ')
			}
			out = append(out, "[tool: "+name+"]"...)
		case "tool_result":
			if i > 0 {
				out = append(out, ' ')
			}
			out = append(out, "[tool result]"...)
		case "thinking":
			if i > 0 {
				out = append(out, ' ')
			}
			out = append(out, "[thinking]"...)
		}
	}
	return string(out)
}
