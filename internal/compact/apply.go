package compact

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
)

// ApplyInput is the bundle the orchestrator hands to Apply once the
// preview has been rendered and the user opted in with --apply.
type ApplyInput struct {
	Slice         *Slice
	SessionID     string
	Cwd           string
	Version       string
	Strippers     Strippers
	Target        int
	BoundaryTail  []OutputBlock
	PreCompactTok int
	Force         bool // bypass the fresh-mtime concurrency guard
}

// ApplyResult summarises what apply did. Returned for preview and
// recorded in the ledger.
type ApplyResult struct {
	BoundaryUUID    string
	SyntheticUUID   string
	BoundaryLine    int
	SyntheticLine   int
	PreApplyOffset  int64
	PostApplyOffset int64
	SnapshotPath    string
	LedgerPath      string
}

// Apply appends one compact_boundary system entry and one synthetic
// user entry to the JSONL transcript. Pre-apply byte offset and a
// gzip snapshot are recorded in the ledger so --undo can either
// truncate (fast path) or restore from snapshot (safety path).
func Apply(in ApplyInput) (*ApplyResult, error) {
	if in.Slice == nil {
		return nil, fmt.Errorf("apply: nil slice")
	}
	if in.SessionID == "" {
		return nil, fmt.Errorf("apply: empty session id")
	}
	path := in.Slice.Path

	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat transcript: %w", err)
	}
	if !in.Force && time.Since(stat.ModTime()) < 60*time.Second {
		return nil, fmt.Errorf("transcript modified within last 60s; Claude Code may be running this session, exit it first or pass --force")
	}
	preOffset := stat.Size()

	snapPath, err := snapshotGzip(path, in.SessionID)
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}

	parentUUID := lastChainUUID(in.Slice)
	now := time.Now().UTC()
	boundaryUUID := uuid.NewString()
	syntheticUUID := uuid.NewString()

	boundaryLine, err := buildBoundaryEntry(boundaryEntryArgs{
		UUID:          boundaryUUID,
		ParentUUID:    parentUUID,
		SessionID:     in.SessionID,
		Cwd:           in.Cwd,
		Version:       in.Version,
		Timestamp:     now,
		PreCompactTok: in.PreCompactTok,
	})
	if err != nil {
		return nil, fmt.Errorf("build boundary: %w", err)
	}
	syntheticLine, err := buildSyntheticUserEntry(syntheticEntryArgs{
		UUID:       syntheticUUID,
		ParentUUID: boundaryUUID,
		SessionID:  in.SessionID,
		Cwd:        in.Cwd,
		Version:    in.Version,
		Timestamp:  now.Add(time.Millisecond),
		Content:    in.BoundaryTail,
	})
	if err != nil {
		return nil, fmt.Errorf("build synthetic user: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open transcript for append: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(boundaryLine, '\n')); err != nil {
		return nil, fmt.Errorf("append boundary: %w", err)
	}
	if _, err := f.Write(append(syntheticLine, '\n')); err != nil {
		return nil, fmt.Errorf("append synthetic user: %w", err)
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("fsync: %w", err)
	}

	postStat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("post-apply stat: %w", err)
	}
	if err := validateAppendedJSONL(path, preOffset); err != nil {
		return nil, fmt.Errorf("validate appended jsonl: %w", err)
	}

	res := &ApplyResult{
		BoundaryUUID:    boundaryUUID,
		SyntheticUUID:   syntheticUUID,
		BoundaryLine:    len(in.Slice.AllEntries),
		SyntheticLine:   len(in.Slice.AllEntries) + 1,
		PreApplyOffset:  preOffset,
		PostApplyOffset: postStat.Size(),
		SnapshotPath:    snapPath,
	}
	ledgerPath, err := appendLedger(in.SessionID, LedgerEntry{
		Timestamp:      now,
		Op:             "apply",
		Target:         in.Target,
		Strips:         strippersList(in.Strippers),
		PreApplyOffset: preOffset,
		SnapshotPath:   snapPath,
		BoundaryUUID:   boundaryUUID,
		SyntheticUUID:  syntheticUUID,
	})
	if err != nil {
		return nil, fmt.Errorf("append ledger: %w", err)
	}
	res.LedgerPath = ledgerPath
	return res, nil
}

func validateAppendedJSONL(path string, preOffset int64) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(preOffset, io.SeekStart); err != nil {
		return fmt.Errorf("seek transcript: %w", err)
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024), 5*1024*1024)
	lines := make([]string, 0, 2)
	for len(lines) < 2 && scanner.Scan() {
		line := string(scanner.Bytes())
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read transcript tail: %w", err)
	}
	if len(lines) < 2 {
		return fmt.Errorf("expected 2 appended lines, found %d", len(lines))
	}
	var boundary struct {
		Type           string `json:"type"`
		Subtype        string `json:"subtype"`
		CompactPayload struct {
			PreCompactTokenCount int `json:"preCompactTokenCount"`
		} `json:"compactMetadata"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &boundary); err != nil {
		return fmt.Errorf("unmarshal boundary line: %w", err)
	}
	if boundary.Type != "system" || boundary.Subtype != "compact_boundary" {
		return fmt.Errorf("boundary line has unexpected type/subtype: %s/%s", boundary.Type, boundary.Subtype)
	}
	var synthetic struct {
		Type           string `json:"type"`
		CompactSummary bool   `json:"isCompactSummary"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &synthetic); err != nil {
		return fmt.Errorf("unmarshal synthetic line: %w", err)
	}
	if synthetic.Type != "user" || !synthetic.CompactSummary {
		return fmt.Errorf("synthetic line missing compact summary marker")
	}
	if boundary.CompactPayload.PreCompactTokenCount < 0 {
		return fmt.Errorf("boundary pre-compact token count is invalid: %d", boundary.CompactPayload.PreCompactTokenCount)
	}
	return nil
}

func lastChainUUID(slice *Slice) string {
	for i := len(slice.AllEntries) - 1; i >= 0; i-- {
		if slice.AllEntries[i].UUID != "" {
			return slice.AllEntries[i].UUID
		}
	}
	return ""
}

func strippersList(s Strippers) []string {
	var out []string
	if s.Thinking {
		out = append(out, "thinking")
	}
	if s.Images {
		out = append(out, "images")
	}
	if s.Tools {
		out = append(out, "tools")
	}
	if s.Chat {
		out = append(out, "chat")
	}
	return out
}

type boundaryEntryArgs struct {
	UUID          string
	ParentUUID    string
	SessionID     string
	Cwd           string
	Version       string
	Timestamp     time.Time
	PreCompactTok int
}

// compactMetadata is the inner payload Claude Code's writer attaches
// to every compact_boundary entry. clyde always emits trigger="manual"
// and surfaces the pre-compact token count for downstream tooling.
type compactMetadata struct {
	Trigger              string `json:"trigger"`
	PreCompactTokenCount int    `json:"preCompactTokenCount"`
}

// syntheticMessage is the inner `message` object on the synthetic
// user entry. Content holds the typed Anthropic content array
// produced by synth.go.
type syntheticMessage struct {
	Role    string        `json:"role"`
	Content []OutputBlock `json:"content"`
}

// buildBoundaryEntry emits a system compact_boundary line. Field order
// places "compact_boundary" within the first 256 bytes so Claude
// Code's large-file boundary scanner (BOUNDARY_SEARCH_BOUND=256 in
// sessionStoragePortable.ts) detects it.
func buildBoundaryEntry(a boundaryEntryArgs) ([]byte, error) {
	meta := compactMetadata{Trigger: "manual", PreCompactTokenCount: a.PreCompactTok}
	fields := []orderedJSONField{
		mustField("parentUuid", optionalString(a.ParentUUID)),
		mustField("isSidechain", false),
		mustField("type", "system"),
		mustField("subtype", "compact_boundary"),
		mustField("content", "Conversation compacted by clyde."),
		mustField("isMeta", true),
		mustField("timestamp", a.Timestamp.Format(time.RFC3339Nano)),
		mustField("uuid", a.UUID),
		mustField("compactMetadata", meta),
		mustField("cwd", a.Cwd),
		mustField("sessionId", a.SessionID),
		mustField("version", a.Version),
	}
	return orderedJSON(fields).Marshal()
}

type syntheticEntryArgs struct {
	UUID       string
	ParentUUID string
	SessionID  string
	Cwd        string
	Version    string
	Timestamp  time.Time
	Content    []OutputBlock
}

func buildSyntheticUserEntry(a syntheticEntryArgs) ([]byte, error) {
	message := syntheticMessage{Role: "user", Content: a.Content}
	fields := []orderedJSONField{
		mustField("parentUuid", optionalString(a.ParentUUID)),
		mustField("isSidechain", false),
		mustField("type", "user"),
		mustField("isCompactSummary", true),
		mustField("timestamp", a.Timestamp.Format(time.RFC3339Nano)),
		mustField("uuid", a.UUID),
		mustField("message", message),
		mustField("cwd", a.Cwd),
		mustField("sessionId", a.SessionID),
		mustField("version", a.Version),
	}
	return orderedJSON(fields).Marshal()
}

// orderedJSON is a minimal "ordered map" for JSON emission so we can
// control key order on disk. Each field carries a pre-encoded
// json.RawMessage, so the ordered emitter never sees raw `any`: the
// type system enforces that callers serialize their values up front
// via mustField.
type orderedJSON []orderedJSONField

// orderedJSONField pairs a JSON object key with an already-encoded
// value. RawValue is JSON bytes ready to splice in verbatim, so the
// emitter is fully typed at the boundary.
type orderedJSONField struct {
	Key      string
	RawValue json.RawMessage
}

// jsonEncodable is the closed set of value types orderedJSONField
// accepts. Constrained to the concrete shapes apply.go actually
// emits so we never widen the surface to bare interface{}.
type jsonEncodable interface {
	string | bool | int | compactMetadata | syntheticMessage | optionalString
}

// optionalString models a value that may be either a JSON string or
// JSON null. Empty string serializes as null; non-empty as the
// string. Used for parentUuid on the very first chain entry.
type optionalString string

// MarshalJSON makes optionalString satisfy json.Marshaler so it goes
// through the standard library encoder without an `any` detour.
func (o optionalString) MarshalJSON() ([]byte, error) {
	if o == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(o))
}

// mustField pre-encodes a typed value and pairs it with key. The
// generic constraint means only the listed concrete types compile;
// any attempt to pass an interface or unknown struct is a build
// error rather than a runtime surprise.
func mustField[T jsonEncodable](key string, value T) orderedJSONField {
	encoded, err := json.Marshal(value)
	if err != nil {
		// json.Marshal on the constrained set above cannot fail in
		// practice; panic surfaces a programmer error rather than
		// hiding it behind an error return that complicates every
		// call site.
		panic(fmt.Sprintf("orderedJSON: marshal %q: %v", key, err))
	}
	return orderedJSONField{Key: key, RawValue: encoded}
}

// Marshal concatenates the pre-encoded fields into one JSON object,
// preserving field order.
func (o orderedJSON) Marshal() ([]byte, error) {
	out := []byte{'{'}
	for i, f := range o {
		if i > 0 {
			out = append(out, ',')
		}
		key, err := json.Marshal(f.Key)
		if err != nil {
			return nil, fmt.Errorf("orderedJSON: marshal key %q: %w", f.Key, err)
		}
		out = append(out, key...)
		out = append(out, ':')
		out = append(out, f.RawValue...)
	}
	out = append(out, '}')
	return out, nil
}
