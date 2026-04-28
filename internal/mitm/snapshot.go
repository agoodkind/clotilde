package mitm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// SnapshotOptions configures Snapshot extraction from a JSONL
// transcript.
type SnapshotOptions struct {
	UpstreamName    string
	UpstreamVersion string
}

// ExtractSnapshot reads a JSONL transcript at path and returns the
// typed Snapshot summarizing its wire shape. Returns an error when
// the file is unreadable or contains no ws_start records.
func ExtractSnapshot(path string, opts SnapshotOptions) (Snapshot, error) {
	records, err := readCaptureRecords(path)
	if err != nil {
		return Snapshot{}, err
	}
	return buildSnapshot(records, opts)
}

// WriteSnapshotTOML persists the Snapshot as TOML under dir,
// creating dir when missing. The file is named reference.toml.
func WriteSnapshotTOML(snap Snapshot, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "reference.toml")
	raw, err := toml.Marshal(snap)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// LoadSnapshotTOML reads a reference.toml back into a Snapshot.
func LoadSnapshotTOML(path string) (Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := toml.Unmarshal(raw, &snap); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// readCaptureRecords parses the JSONL file at path into typed
// CaptureRecord values. Lines that fail to parse are skipped with a
// best-effort policy: capture files written by mitmproxy or the
// dump.py addon may carry trailing blank lines or partial records
// when interrupted. We do not error on a partial trailer.
func readCaptureRecords(path string) ([]CaptureRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var out []CaptureRecord
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec CaptureRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, err
	}
	return out, nil
}

func buildSnapshot(records []CaptureRecord, opts SnapshotOptions) (Snapshot, error) {
	var wsStart *CaptureRecord
	var wsMsgs []CaptureRecord
	for i := range records {
		switch records[i].Kind {
		case RecordWSStart:
			if wsStart == nil {
				wsStart = &records[i]
			}
		case RecordWSMessage:
			wsMsgs = append(wsMsgs, records[i])
		}
	}
	if wsStart == nil {
		return Snapshot{}, fmt.Errorf("snapshot: no ws_start record in transcript")
	}

	snap := Snapshot{
		Upstream: SnapshotUpstream{
			Name:       opts.UpstreamName,
			Version:    opts.UpstreamVersion,
			CapturedAt: time.Unix(wsStart.T, 0).UTC().Format(time.RFC3339),
		},
		Handshake: SnapshotHandshake{
			URLPattern: normalizeURLPattern(wsStart.URL),
			Headers:    canonicalHeaders(wsStart.RequestHeaders),
		},
	}

	// Inspect the outbound response.create frames.
	bodyFields := map[string]bool{}
	includeKeys := map[string]bool{}
	toolKinds := map[string]bool{}
	storeObserved := ""
	storeRequired := false
	frameType := ""
	warmupSeen := false
	chainsPrev := false
	realInputMin, realInputMax := -1, 0
	realToolsMin, realToolsMax := -1, 0
	warmupInputMin, warmupInputMax := -1, 0

	for _, msg := range wsMsgs {
		if !msg.FromClient || msg.Text == "" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(msg.Text), &body); err != nil {
			continue
		}
		if t, _ := body["type"].(string); t != "" {
			frameType = t
		}
		for key := range body {
			bodyFields[key] = true
		}
		if include, ok := body["include"].([]any); ok {
			for _, v := range include {
				if s, ok := v.(string); ok {
					includeKeys[s] = true
				}
			}
		}
		if tools, ok := body["tools"].([]any); ok {
			for _, v := range tools {
				if m, ok := v.(map[string]any); ok {
					if k, _ := m["type"].(string); k != "" {
						toolKinds[k] = true
					}
				}
			}
		}
		if v, ok := body["store"].(bool); ok {
			storeRequired = true
			storeObserved = fmt.Sprintf("%v", v)
		}

		gen, _ := body["generate"].(bool)
		hasPrev := false
		if v, ok := body["previous_response_id"].(string); ok && strings.TrimSpace(v) != "" {
			hasPrev = true
		}
		input, _ := body["input"].([]any)
		tools, _ := body["tools"].([]any)

		// generate=false marks a warmup frame; absent (default true)
		// marks a real frame.
		_, hasGenerate := body["generate"]
		isWarmup := hasGenerate && !gen

		if isWarmup {
			warmupSeen = true
			warmupInputMin = updateMin(warmupInputMin, len(input))
			warmupInputMax = updateMax(warmupInputMax, len(input))
		} else {
			realInputMin = updateMin(realInputMin, len(input))
			realInputMax = updateMax(realInputMax, len(input))
			realToolsMin = updateMin(realToolsMin, len(tools))
			realToolsMax = updateMax(realToolsMax, len(tools))
			if hasPrev {
				chainsPrev = true
			}
		}
	}

	snap.Body = SnapshotBody{
		Type:        frameType,
		FieldNames:  sortedKeys(bodyFields),
		ToolKinds:   sortedKeys(toolKinds),
		IncludeKeys: sortedKeys(includeKeys),
	}

	openingLabel := "real_only"
	if warmupSeen && chainsPrev {
		openingLabel = "warmup_then_real_chained"
	} else if warmupSeen {
		openingLabel = "warmup_then_real"
	}
	snap.FrameSequence = SnapshotFrameSequence{
		Opening:    openingLabel,
		ChainsPrev: chainsPrev,
		Warmup: SnapshotFrameDescriptor{
			Generate:           "false",
			InputCountMin:      max0(warmupInputMin),
			InputCountMax:      warmupInputMax,
			StoreFieldRequired: storeRequired,
			StoreValueObserved: storeObserved,
		},
		Real: SnapshotFrameDescriptor{
			Generate:           "absent_default_true",
			HasPrev:            chainsPrev,
			InputCountMin:      max0(realInputMin),
			InputCountMax:      realInputMax,
			ToolsCountMin:      max0(realToolsMin),
			ToolsCountMax:      realToolsMax,
			StoreFieldRequired: storeRequired,
			StoreValueObserved: storeObserved,
		},
	}

	// Constants are pulled directly from the handshake headers.
	snap.Constants = SnapshotConstants{
		Originator:              wsStart.RequestHeaders["originator"],
		OpenAIBeta:              wsStart.RequestHeaders["openai-beta"],
		UserAgent:               wsStart.RequestHeaders["user-agent"],
		BetaFeatures:            wsStart.RequestHeaders["x-codex-beta-features"],
		StainlessPackageVersion: wsStart.RequestHeaders["x-stainless-package-version"],
	}

	return snap, nil
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func updateMin(current, candidate int) int {
	if current < 0 || candidate < current {
		return candidate
	}
	return current
}

func updateMax(current, candidate int) int {
	if candidate > current {
		return candidate
	}
	return current
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// canonicalHeaders normalizes a captured handshake header map into
// a sorted list with secret values redacted and known-volatile
// values reduced to patterns.
func canonicalHeaders(in map[string]string) []SnapshotHeader {
	out := make([]SnapshotHeader, 0, len(in))
	for key, value := range in {
		lower := strings.ToLower(key)
		out = append(out, SnapshotHeader{
			Name:  lower,
			Value: canonicalHeaderValue(lower, value),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

var (
	hexPattern    = regexp.MustCompile(`(?i)^[0-9a-f-]{12,}$`)
	uuidPattern   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	bearerPattern = regexp.MustCompile(`^Bearer\s+\S+`)
)

func canonicalHeaderValue(lowerName, value string) string {
	switch lowerName {
	case "authorization":
		if bearerPattern.MatchString(value) {
			return "<bearer-redacted>"
		}
		return "<redacted>"
	case "cookie", "set-cookie", "x-api-key", "anthropic-api-key":
		return "<redacted>"
	case "x-client-request-id", "session_id":
		return "<conversation_id>"
	case "x-codex-window-id":
		return "<conversation_id>:0"
	case "x-codex-installation-id":
		return "<installation_id>"
	case "x-codex-turn-metadata":
		return "<json:turn_metadata>"
	case "cf-ray", "x-amzn-trace-id":
		return "<trace>"
	}
	if uuidPattern.MatchString(value) {
		return uuidPattern.ReplaceAllString(value, "<uuid>")
	}
	if hexPattern.MatchString(value) && len(value) > 16 {
		return "<hex>"
	}
	return value
}

// normalizeURLPattern strips conversation-specific query strings
// from the upstream URL so two captures from different sessions
// produce the same URL pattern.
func normalizeURLPattern(raw string) string {
	if i := strings.Index(raw, "?"); i >= 0 {
		return raw[:i]
	}
	return raw
}
