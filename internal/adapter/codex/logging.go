// Package codex contains Codex transport and runtime integration.
// package's logging surface so payload corruption on the Codex path is
// observable end-to-end. The dedicated JSONL sink lives next to
// anthropic.jsonl under $XDG_STATE_HOME/clyde/codex.jsonl.
package codex

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"goodkind.io/clyde/internal/correlation"
	"goodkind.io/clyde/internal/slogger"
)

// BodyLogConfig controls how much of the outbound Codex request bytes
// the transport layer writes to logs. Mirrors LoggingBody from
// internal/config but is duplicated here so the codex package does not
// import the config package directly. Plumbed in by callers.
type BodyLogConfig struct {
	Mode  string
	MaxKB int
}

const (
	BodyLogOff       = "off"
	BodyLogSummary   = "summary"
	BodyLogWhitelist = "whitelist"
	BodyLogRaw       = "raw"
)

// Resolve returns the effective body-log mode and byte cap. Empty mode
// defaults to summary; non-positive MaxKB defaults to 32.
func (c BodyLogConfig) Resolve() (mode string, maxBytes int) {
	mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if mode == "" {
		mode = BodyLogSummary
	}
	maxBytes = c.MaxKB * 1024
	if maxBytes <= 0 {
		maxBytes = 32 * 1024
	}
	return mode, maxBytes
}

// CodexLogPath returns the JSONL file the codex package double-writes
// its wire events to. Honors $CLYDE_CODEX_LOG_PATH for tests.
func CodexLogPath() string {
	if p := os.Getenv("CLYDE_CODEX_LOG_PATH"); p != "" {
		return p
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "clyde", "codex.jsonl")
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "clyde", "codex.jsonl")
}

var (
	codexFileLoggerOnce sync.Once
	codexFileLogger     *slog.Logger
)

// dedicatedCodexLogger returns a JSON slog handler writing to
// CodexLogPath(). Best effort: a missing log dir never blocks traffic.
// The handler is bound to the path observed at first call.
func dedicatedCodexLogger() *slog.Logger {
	codexFileLoggerOnce.Do(func() {
		path := CodexLogPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		codexFileLogger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	return codexFileLogger
}

// requestEvent is the typed payload for each codex.responses.request
// log line (HTTP, websocket, app fallback). Optional fields use the
// zero value as the omit sentinel; the slog conversion drops empty
// strings, zero ints, and nil maps.
type requestEvent struct {
	Subcomponent       string
	Transport          string
	Method             string
	RequestID          string
	CursorRequestID    string
	Correlation        correlation.Context
	Alias              string
	Model              string
	URL                string
	BodyBytes          int
	Headers            map[string]string
	Body               string
	BodyB64            string
	BodyTruncated      bool
	BodySummary        *codexBodySummary
	PreviousResponseID string
	InputCount         int
	ToolCount          int
	Warmup             bool
	Err                string
}

func (e requestEvent) toSlogAttrs() []slog.Attr {
	attrs := make([]slog.Attr, 0, 24)
	if e.Subcomponent != "" {
		attrs = append(attrs, slog.String("subcomponent", e.Subcomponent))
	}
	if e.Transport != "" {
		attrs = append(attrs, slog.String("transport", e.Transport))
	}
	if e.Method != "" {
		attrs = append(attrs, slog.String("method", e.Method))
	}
	if e.RequestID != "" {
		attrs = append(attrs, slog.String("request_id", e.RequestID))
	}
	if e.CursorRequestID != "" {
		attrs = append(attrs, slog.String("cursor_request_id", e.CursorRequestID))
	}
	attrs = append(attrs, e.Correlation.Attrs()...)
	if e.Alias != "" {
		attrs = append(attrs, slog.String("alias", e.Alias))
	}
	if e.Model != "" {
		attrs = append(attrs, slog.String("model", e.Model))
	}
	if e.URL != "" {
		attrs = append(attrs, slog.String("url", e.URL))
	}
	attrs = append(attrs, slog.Int("body_bytes", e.BodyBytes))
	if len(e.Headers) > 0 {
		attrs = append(attrs, slog.Attr{Key: "headers", Value: slog.GroupValue(stringMapAttrs(e.Headers)...)})
	}
	if e.BodySummary != nil {
		attrs = append(attrs, slog.Attr{Key: "body_summary", Value: slog.GroupValue(e.BodySummary.toSlogAttrs()...)})
	}
	if e.Body != "" {
		attrs = append(attrs, slog.String("body", e.Body))
	}
	if e.BodyB64 != "" {
		attrs = append(attrs, slog.String("body_b64", e.BodyB64))
	}
	if e.BodyTruncated {
		attrs = append(attrs, slog.Bool("body_truncated", true))
	}
	if e.PreviousResponseID != "" {
		attrs = append(attrs, slog.Bool("previous_response_id_present", true))
	}
	if e.InputCount > 0 {
		attrs = append(attrs, slog.Int("input_count", e.InputCount))
	}
	if e.ToolCount > 0 {
		attrs = append(attrs, slog.Int("tool_count", e.ToolCount))
	}
	if e.Warmup {
		attrs = append(attrs, slog.Bool("warmup", true))
	}
	if e.Err != "" {
		attrs = append(attrs, slog.String("err", e.Err))
	}
	return attrs
}

// logCodexEvent writes the event to both slog.Default() (which the
// daemon configures to fan out to clyde-daemon.jsonl) and the
// dedicated codex.jsonl sink. The dedicated file is best effort.
func logCodexEvent(ctx context.Context, level slog.Level, event string, attrs []slog.Attr) {
	logCodexEventWithConcern(ctx, level, event, slogger.ConcernAdapterProviderCodex, attrs)
}

func logCodexEventWithConcern(ctx context.Context, level slog.Level, event, concern string, attrs []slog.Attr) {
	if ctx == nil {
		ctx = context.Background()
	}
	slogger.WithConcern(slog.Default(), concern).LogAttrs(ctx, level, event, attrs...)
	if l := dedicatedCodexLogger(); l != nil {
		slogger.WithConcern(l, concern).LogAttrs(ctx, level, event, attrs...)
	}
}

// codexBodySummary is the lightweight shape captured under
// logging.body.mode = "summary" or "whitelist". It carries enough to
// diagnose corruption without dumping the full payload (e.g. how many
// inputs and tools, the resolved model and conversation key, whether
// continuation was requested).
type codexBodySummary struct {
	Model              string `json:"model,omitempty"`
	InputCount         int    `json:"input_count"`
	ToolCount          int    `json:"tool_count"`
	HasInstructions    bool   `json:"has_instructions"`
	InstructionsBytes  int    `json:"instructions_bytes"`
	PromptCacheKey     string `json:"prompt_cache_key,omitempty"`
	PreviousResponseID bool   `json:"previous_response_id,omitempty"`
	Stream             bool   `json:"stream,omitempty"`
	ServiceTier        string `json:"service_tier,omitempty"`
}

func (s codexBodySummary) toSlogAttrs() []slog.Attr {
	attrs := []slog.Attr{
		slog.Int("input_count", s.InputCount),
		slog.Int("tool_count", s.ToolCount),
		slog.Bool("has_instructions", s.HasInstructions),
		slog.Int("instructions_bytes", s.InstructionsBytes),
	}
	if s.Model != "" {
		attrs = append(attrs, slog.String("model", s.Model))
	}
	if s.PromptCacheKey != "" {
		attrs = append(attrs, slog.String("prompt_cache_key", s.PromptCacheKey))
	}
	if s.PreviousResponseID {
		attrs = append(attrs, slog.Bool("previous_response_id", true))
	}
	if s.Stream {
		attrs = append(attrs, slog.Bool("stream", true))
	}
	if s.ServiceTier != "" {
		attrs = append(attrs, slog.String("service_tier", s.ServiceTier))
	}
	return attrs
}

func stringMapAttrs(values map[string]string) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(values))
	for key, value := range values {
		attrs = append(attrs, slog.String(key, value))
	}
	return attrs
}

// summarizeWsRequest builds a body summary for a websocket frame.
func summarizeWsRequest(payload ResponseCreateWsRequest) *codexBodySummary {
	return &codexBodySummary{
		Model:              payload.Model,
		InputCount:         len(payload.Input),
		ToolCount:          len(payload.Tools),
		HasInstructions:    strings.TrimSpace(payload.Instructions) != "",
		InstructionsBytes:  len(payload.Instructions),
		PromptCacheKey:     payload.PromptCacheKey,
		PreviousResponseID: strings.TrimSpace(payload.PreviousResponseID) != "",
		Stream:             payload.Stream,
		ServiceTier:        payload.ServiceTier,
	}
}

type responseCreateFrameSummary struct {
	Subcomponent                     string
	Transport                        string
	RequestID                        string
	CursorRequestID                  string
	ConversationID                   string
	Correlation                      correlation.Context
	Alias                            string
	Model                            string
	FrameBytes                       int
	FrameSHA256                      string
	InstructionsLength               int
	InstructionsSHA256               string
	CursorSystemPromptPresent        bool
	ClydeCursorModePresent           bool
	OldClydePersonalityPromptPresent bool
	CodexBasePromptPresent           bool
	InputCount                       int
	ToolCount                        int
	InputTypeCounts                  map[string]int
	InputRoleCounts                  map[string]int
	ToolNames                        []string
	PromptCacheKey                   string
	PreviousResponseIDPresent        bool
	ServiceTier                      string
}

func (s responseCreateFrameSummary) toSlogAttrs() []slog.Attr {
	attrs := []slog.Attr{
		slog.String("subcomponent", s.Subcomponent),
		slog.String("transport", s.Transport),
		slog.Int("frame_bytes", s.FrameBytes),
		slog.String("frame_sha256", s.FrameSHA256),
		slog.Int("instructions_length", s.InstructionsLength),
		slog.Bool("cursor_system_prompt_present", s.CursorSystemPromptPresent),
		slog.Bool("clyde_cursor_mode_present", s.ClydeCursorModePresent),
		slog.Bool("old_clyde_personality_prompt_present", s.OldClydePersonalityPromptPresent),
		slog.Bool("codex_base_prompt_present", s.CodexBasePromptPresent),
		slog.Int("input_count", s.InputCount),
		slog.Int("tool_count", s.ToolCount),
	}
	if s.RequestID != "" {
		attrs = append(attrs, slog.String("request_id", s.RequestID))
	}
	if s.CursorRequestID != "" {
		attrs = append(attrs, slog.String("cursor_request_id", s.CursorRequestID))
	}
	if s.ConversationID != "" {
		attrs = append(attrs, slog.String("conversation_id", s.ConversationID))
	}
	attrs = append(attrs, s.Correlation.Attrs()...)
	if s.Alias != "" {
		attrs = append(attrs, slog.String("alias", s.Alias))
	}
	if s.Model != "" {
		attrs = append(attrs, slog.String("model", s.Model))
	}
	if s.InstructionsSHA256 != "" {
		attrs = append(attrs, slog.String("instructions_sha256", s.InstructionsSHA256))
	}
	if len(s.InputTypeCounts) > 0 {
		attrs = append(attrs, slog.Attr{Key: "input_type_counts", Value: slog.GroupValue(intMapAttrs(s.InputTypeCounts)...)})
	}
	if len(s.InputRoleCounts) > 0 {
		attrs = append(attrs, slog.Attr{Key: "input_role_counts", Value: slog.GroupValue(intMapAttrs(s.InputRoleCounts)...)})
	}
	if len(s.ToolNames) > 0 {
		attrs = append(attrs, slog.Any("tool_names", s.ToolNames))
	}
	if s.PromptCacheKey != "" {
		attrs = append(attrs, slog.String("prompt_cache_key", s.PromptCacheKey))
	}
	if s.PreviousResponseIDPresent {
		attrs = append(attrs, slog.Bool("previous_response_id_present", true))
	}
	if s.ServiceTier != "" {
		attrs = append(attrs, slog.String("service_tier", s.ServiceTier))
	}
	return attrs
}

func summarizeFinalResponseCreateFrame(cfg WebsocketTransportConfig, payload ResponseCreateWsRequest, frame []byte) responseCreateFrameSummary {
	markers := detectInstructionMarkers(payload.Instructions)
	return responseCreateFrameSummary{
		Subcomponent:                     "codex",
		Transport:                        "responses_websocket",
		RequestID:                        cfg.RequestID,
		CursorRequestID:                  cfg.CursorRequestID,
		ConversationID:                   cfg.ConversationID,
		Correlation:                      cfg.Correlation,
		Alias:                            cfg.Alias,
		Model:                            payload.Model,
		FrameBytes:                       len(frame),
		FrameSHA256:                      sha256Hex(frame),
		InstructionsLength:               len(payload.Instructions),
		InstructionsSHA256:               sha256StringHex(payload.Instructions),
		CursorSystemPromptPresent:        markers.CursorSystemPromptPresent,
		ClydeCursorModePresent:           markers.ClydeCursorModePresent,
		OldClydePersonalityPromptPresent: markers.OldClydePersonalityPromptPresent,
		CodexBasePromptPresent:           markers.CodexBasePromptPresent,
		InputCount:                       len(payload.Input),
		ToolCount:                        len(payload.Tools),
		InputTypeCounts:                  countInputStrings(payload.Input, "type"),
		InputRoleCounts:                  countInputStrings(payload.Input, "role"),
		ToolNames:                        responseCreateToolNames(payload.Tools),
		PromptCacheKey:                   payload.PromptCacheKey,
		PreviousResponseIDPresent:        strings.TrimSpace(payload.PreviousResponseID) != "",
		ServiceTier:                      payload.ServiceTier,
	}
}

type instructionMarkerSummary struct {
	CursorSystemPromptPresent        bool
	ClydeCursorModePresent           bool
	OldClydePersonalityPromptPresent bool
	CodexBasePromptPresent           bool
}

func detectInstructionMarkers(instructions string) instructionMarkerSummary {
	trimmed := strings.TrimSpace(instructions)
	lower := strings.ToLower(trimmed)
	return instructionMarkerSummary{
		CursorSystemPromptPresent: containsAny(lower,
			"cursor system",
			"cursor-provided system",
			"you are an ai coding assistant",
			"you are a powerful agentic ai coding assistant",
			"you are an agent - please keep going",
		),
		ClydeCursorModePresent: strings.Contains(trimmed, "<cursor_mode>"),
		OldClydePersonalityPromptPresent: containsAny(trimmed,
			"vivid inner life as Codex",
			"warm and upbeat",
			"another subjectivity, not a mirror",
		),
		CodexBasePromptPresent: containsAny(trimmed,
			"You are Codex, a coding agent based on GPT-5",
			"As an expert coding agent, your primary focus is writing code",
		),
	}
}

func countInputStrings(input []map[string]any, key string) map[string]int {
	counts := make(map[string]int)
	for _, item := range input {
		value := mapString(item, key)
		if value == "" {
			continue
		}
		counts[value]++
	}
	return counts
}

func responseCreateToolNames(tools []any) []string {
	seen := make(map[string]bool)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		m, _ := tool.(map[string]any)
		name := mapString(m, "name")
		if name == "" {
			name = mapString(m, "type")
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func intMapAttrs(values map[string]int) []slog.Attr {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	attrs := make([]slog.Attr, 0, len(keys))
	for _, key := range keys {
		attrs = append(attrs, slog.Int(key, values[key]))
	}
	return attrs
}

func sha256StringHex(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return sha256Hex([]byte(value))
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// applyBodyMode returns (body string, body_b64 string, truncated bool)
// according to the configured mode. Modes:
//
//	off:       both empty, body_bytes only
//	summary:   both empty (caller emits BodySummary separately)
//	whitelist: body string truncated to maxBytes, no b64
//	raw:       body string truncated to maxBytes plus b64 of full bytes
func applyBodyMode(raw []byte, mode string, maxBytes int) (body, b64 string, truncated bool) {
	switch mode {
	case BodyLogOff, BodyLogSummary:
		return "", "", false
	case BodyLogWhitelist:
		body, truncated = truncateCodexBody(raw, maxBytes)
		return body, "", truncated
	case BodyLogRaw:
		body, truncated = truncateCodexBody(raw, maxBytes)
		b64 = base64.StdEncoding.EncodeToString(raw)
		return body, b64, truncated
	default:
		body, truncated = truncateCodexBody(raw, maxBytes)
		return body, "", truncated
	}
}

func truncateCodexBody(body []byte, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", false
	}
	if len(body) <= maxBytes {
		return string(body), false
	}
	return string(body[:maxBytes]), true
}
