// Per session transcript tail aggregator. The daemon owns one
// transcript.Tailer per active session id regardless of how many
// gRPC subscribers are connected. Each subscriber gets its own
// buffered channel; a slow subscriber drops events for itself
// without affecting the others.
package daemon

import (
	"sync"

	"github.com/fgrehm/clotilde/api/daemonpb"
	"github.com/fgrehm/clotilde/internal/transcript"
)

// transcriptHub fans transcript tail lines out to multiple
// subscribers. Reference counted: the first subscriber starts the
// tailer; the last one to leave stops it.
type transcriptHub struct {
	mu       sync.Mutex
	entries  map[string]*hubEntry
}

type hubEntry struct {
	tailer       *transcript.Tailer
	subscribers  map[chan *daemonpb.TranscriptLine]struct{}
	transcriptPath string
	stop         chan struct{}
}

func newTranscriptHub() *transcriptHub {
	return &transcriptHub{entries: make(map[string]*hubEntry)}
}

// Subscribe attaches a new subscriber channel to the tailer for the
// given session id. The first subscriber for a session triggers the
// tailer to open. The returned cleanup function unsubscribes and
// stops the tailer when the last subscriber leaves.
func (h *transcriptHub) Subscribe(sessionID, path string, startOffset int64) (<-chan *daemonpb.TranscriptLine, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	entry, ok := h.entries[sessionID]
	if !ok {
		t, err := transcript.OpenTailer(path, startOffset)
		if err != nil {
			return nil, nil, err
		}
		entry = &hubEntry{
			tailer:         t,
			subscribers:    make(map[chan *daemonpb.TranscriptLine]struct{}),
			transcriptPath: path,
			stop:           make(chan struct{}),
		}
		h.entries[sessionID] = entry
		go h.fanOut(sessionID, entry)
	}

	ch := make(chan *daemonpb.TranscriptLine, 64)
	entry.subscribers[ch] = struct{}{}

	cleanup := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		current, ok := h.entries[sessionID]
		if !ok || current != entry {
			return
		}
		if _, present := entry.subscribers[ch]; !present {
			return
		}
		delete(entry.subscribers, ch)
		close(ch)
		if len(entry.subscribers) == 0 {
			close(entry.stop)
			entry.tailer.Close()
			delete(h.entries, sessionID)
		}
	}
	return ch, cleanup, nil
}

// fanOut copies every line from the tailer to every active
// subscriber's channel. Slow subscribers drop messages.
func (h *transcriptHub) fanOut(sessionID string, entry *hubEntry) {
	for {
		select {
		case line, ok := <-entry.tailer.Lines():
			if !ok {
				h.closeAll(sessionID, entry)
				return
			}
			pb := &daemonpb.TranscriptLine{
				ByteOffset: line.ByteOffset,
				RawJsonl:   line.RawJSONL,
				Role:       line.Role,
				Text:       line.Text,
			}
			if !line.Timestamp.IsZero() {
				pb.TimestampNanos = line.Timestamp.UnixNano()
			}
			h.broadcast(entry, pb)
		case <-entry.stop:
			return
		}
	}
}

func (h *transcriptHub) broadcast(entry *hubEntry, pb *daemonpb.TranscriptLine) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range entry.subscribers {
		select {
		case ch <- pb:
		default:
			// Slow subscriber. Drop this one.
		}
	}
}

func (h *transcriptHub) closeAll(sessionID string, entry *hubEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entries[sessionID] == entry {
		delete(h.entries, sessionID)
	}
	for ch := range entry.subscribers {
		close(ch)
	}
	entry.subscribers = nil
}
