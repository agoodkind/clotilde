// Package codex contains Codex transport and runtime integration.
// Clyde uses for its app-fallback path. The canonical definitions live
// in research/codex/codex-rs/app-server-protocol/src/protocol/{v1,v2}.rs
// and the generated TypeScript schema under
// research/codex/codex-rs/app-server-protocol/schema/typescript/v2/.
//
// Field names use the same camelCase serialization Rust produces via
// serde(rename_all = "camelCase"). Optional fields use `,omitempty`
// so the wire shape matches Rust's `Option<T>` skip-if-none behavior.
package codex

// ReasoningEffort mirrors codex_protocol::openai_models::ReasoningEffort
// at research/codex/codex-rs/protocol/src/openai_models.rs:43. The
// lowercase string values match the OpenAI Responses API.
type ReasoningEffort string

const (
	ReasoningEffortUnset   ReasoningEffort = ""
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
)

// ReasoningSummary mirrors codex_protocol::config_types::ReasoningSummary
// at research/codex/codex-rs/protocol/src/config_types.rs:27.
type ReasoningSummary string

const (
	ReasoningSummaryUnset    ReasoningSummary = ""
	ReasoningSummaryAuto     ReasoningSummary = "auto"
	ReasoningSummaryConcise  ReasoningSummary = "concise"
	ReasoningSummaryDetailed ReasoningSummary = "detailed"
	ReasoningSummaryNone     ReasoningSummary = "none"
)

// AskForApproval mirrors the v2 enum at
// research/codex/codex-rs/app-server-protocol/src/protocol/v2.rs:258.
// The experimental Granular variant is intentionally omitted here
// because Clyde does not opt into experimentalApi capabilities.
type AskForApproval string

const (
	AskForApprovalUnset         AskForApproval = ""
	AskForApprovalUnlessTrusted AskForApproval = "untrusted"
	AskForApprovalOnFailure     AskForApproval = "on_failure"
	AskForApprovalOnRequest     AskForApproval = "on_request"
	AskForApprovalNever         AskForApproval = "never"
)

// RPCClientInfo mirrors v1::ClientInfo at
// research/codex/codex-rs/app-server-protocol/src/protocol/v1.rs:36.
type RPCClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// RPCInitializeCapabilities mirrors v1::InitializeCapabilities at
// research/codex/codex-rs/app-server-protocol/src/protocol/v1.rs:45.
type RPCInitializeCapabilities struct {
	ExperimentalAPI           bool     `json:"experimentalApi,omitempty"`
	OptOutNotificationMethods []string `json:"optOutNotificationMethods,omitempty"`
}

// RPCInitializeParams mirrors v1::InitializeParams at
// research/codex/codex-rs/app-server-protocol/src/protocol/v1.rs:28.
type RPCInitializeParams struct {
	ClientInfo   RPCClientInfo              `json:"clientInfo"`
	Capabilities *RPCInitializeCapabilities `json:"capabilities,omitempty"`
}

// RPCThreadStartParams mirrors the v2::ThreadStartParams subset Clyde
// actually emits today. Source:
// research/codex/codex-rs/app-server-protocol/src/protocol/v2.rs:3343.
type RPCThreadStartParams struct {
	Model                  string         `json:"model,omitempty"`
	Cwd                    string         `json:"cwd,omitempty"`
	ApprovalPolicy         AskForApproval `json:"approvalPolicy,omitempty"`
	BaseInstructions       string         `json:"baseInstructions,omitempty"`
	DeveloperInstructions  string         `json:"developerInstructions,omitempty"`
	Ephemeral              bool           `json:"ephemeral,omitempty"`
	ServiceName            string         `json:"serviceName,omitempty"`
	ExperimentalRawEvents  bool           `json:"experimentalRawEvents"`
	PersistExtendedHistory bool           `json:"persistExtendedHistory"`
}

type RPCThreadStartResponse struct {
	Thread RPCThread `json:"thread"`
	Model  string    `json:"model,omitempty"`
}

type RPCThread struct {
	ID string `json:"id"`
}

type RPCByteRange struct {
	Start        int `json:"start"`
	EndExclusive int `json:"endExclusive"`
}

type RPCTextElement struct {
	ByteRange   RPCByteRange `json:"byteRange"`
	Placeholder *string      `json:"placeholder"`
}

// RPCTurnInputItem mirrors the Text variant of v2::UserInput at
// research/codex/codex-rs/app-server-protocol/src/protocol/v2.rs:5411.
// Clyde's app-fallback path only emits text input items today; image,
// local-image, skill, and mention variants are not used.
type RPCTurnInputItem struct {
	Type         string           `json:"type"`
	Text         string           `json:"text"`
	TextElements []RPCTextElement `json:"text_elements"`
}

// NewTextInput is the canonical constructor for the Text variant of
// v2::UserInput.
func NewTextInput(text string) RPCTurnInputItem {
	return RPCTurnInputItem{Type: "text", Text: text, TextElements: []RPCTextElement{}}
}

// RPCTurnStartParams mirrors the v2::TurnStartParams subset Clyde
// emits. Source:
// research/codex/codex-rs/app-server-protocol/src/protocol/v2.rs:5158.
//
// Both `effort` and `summary` are canonical turn-scoped overrides per
// the Rust schema; they are not Clyde extensions.
type RPCTurnStartParams struct {
	ThreadID       string             `json:"threadId"`
	Input          []RPCTurnInputItem `json:"input"`
	ApprovalPolicy AskForApproval     `json:"approvalPolicy,omitempty"`
	Model          string             `json:"model,omitempty"`
	Effort         ReasoningEffort    `json:"effort,omitempty"`
	Summary        ReasoningSummary   `json:"summary,omitempty"`
}

// RPCThreadArchiveParams mirrors v2::ThreadArchiveParams at
// research/codex/codex-rs/app-server-protocol/src/protocol/v2.rs:3659.
type RPCThreadArchiveParams struct {
	ThreadID string `json:"threadId"`
}

func (RPCInitializeParams) rpcMethod() string    { return "initialize" }
func (RPCThreadStartParams) rpcMethod() string   { return "thread/start" }
func (RPCTurnStartParams) rpcMethod() string     { return "turn/start" }
func (RPCThreadArchiveParams) rpcMethod() string { return "thread/archive" }

type RPCAgentMessageDeltaNotification struct {
	Delta string `json:"delta"`
}

type RPCTurnPlanStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

type RPCTurnPlanUpdatedNotification struct {
	Explanation string            `json:"explanation"`
	Plan        []RPCTurnPlanStep `json:"plan"`
}

type RPCItemNotification struct {
	Item     RPCThreadItem `json:"item"`
	ThreadID string        `json:"threadId,omitempty"`
	TurnID   string        `json:"turnId,omitempty"`
}

type RPCThreadItem struct {
	Type    string                `json:"type"`
	ID      string                `json:"id"`
	Status  string                `json:"status,omitempty"`
	Command string                `json:"command,omitempty"`
	Server  string                `json:"server,omitempty"`
	Tool    string                `json:"tool,omitempty"`
	Changes []RPCFileUpdateChange `json:"changes,omitempty"`
}

type RPCFileUpdateChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Diff string `json:"diff"`
}

type RPCOutputDeltaNotification struct {
	Delta  string `json:"delta"`
	ItemID string `json:"itemId"`
}

type RPCMcpToolCallProgressNotification struct {
	Message string `json:"message"`
	ItemID  string `json:"itemId"`
}

type RPCFileChangePatchUpdatedNotification struct {
	ItemID  string                `json:"itemId"`
	Changes []RPCFileUpdateChange `json:"changes"`
}

type RPCReasoningSummaryPartAddedNotification struct {
	SummaryIndex int `json:"summaryIndex"`
}

type RPCReasoningTextDeltaNotification struct {
	Delta        string `json:"delta"`
	SummaryIndex int    `json:"summaryIndex"`
}

type RPCThreadTokenUsageUpdatedNotification struct {
	TokenUsage RPCThreadTokenUsage `json:"tokenUsage"`
}

type RPCThreadTokenUsage struct {
	Last RPCTokenUsage `json:"last"`
}

type RPCTokenUsage struct {
	TotalTokens           int `json:"totalTokens"`
	InputTokens           int `json:"inputTokens"`
	CachedInputTokens     int `json:"cachedInputTokens"`
	OutputTokens          int `json:"outputTokens"`
	ReasoningOutputTokens int `json:"reasoningOutputTokens"`
}
