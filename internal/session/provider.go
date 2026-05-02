package session

import (
	"context"
	"strings"
)

// ProviderID identifies the upstream that owns a session id namespace.
type ProviderID string

const (
	ProviderUnknown ProviderID = ""
	ProviderClaude  ProviderID = "claude"
	ProviderCodex   ProviderID = "codex"
)

// ProviderCapabilities describes the session lifecycle features a provider supports.
type ProviderCapabilities struct {
	ResumeByID          bool
	ForkByID            bool
	SessionIDRotation   bool
	CustomTitles        bool
	PerSessionSettings  bool
	RemoteControl       bool
	TranscriptTail      bool
	TranscriptExport    bool
	Compaction          bool
	ProviderArtifactGC  bool
	ContextUsageInspect bool
}

// ProviderInfoRecord is a typed provider descriptor used by session metadata.
type ProviderInfoRecord struct {
	ID           ProviderID
	Capabilities ProviderCapabilities
}

var defaultProviderInfo = ProviderInfoRecord{
	ID: ProviderClaude,
	Capabilities: ProviderCapabilities{
		ResumeByID:          true,
		ForkByID:            true,
		SessionIDRotation:   true,
		CustomTitles:        true,
		PerSessionSettings:  true,
		RemoteControl:       true,
		TranscriptTail:      true,
		TranscriptExport:    true,
		Compaction:          true,
		ProviderArtifactGC:  true,
		ContextUsageInspect: true,
	},
}

var codexProviderInfo = ProviderInfoRecord{
	ID: ProviderCodex,
	Capabilities: ProviderCapabilities{
		ResumeByID: true,
	},
}

// SessionProvider exposes the provider assigned to a session row.
type SessionProvider interface {
	ProviderID() ProviderID
}

// CapabilityProvider exposes provider capability metadata to callers that need
// provider-aware branching without knowing a concrete implementation.
type CapabilityProvider interface {
	SessionProviderCapabilities() ProviderCapabilities
}

// LaunchIntent describes the generic session action the caller wants a provider
// lifecycle implementation to perform.
type LaunchIntent string

const (
	LaunchIntentNewSession LaunchIntent = "new_session"
)

// LaunchOptions carries provider-neutral launch intent from cmd into the
// provider-owned lifecycle implementation.
type LaunchOptions struct {
	WorkDir             string
	Intent              LaunchIntent
	EnableRemoteControl bool
}

// StartRequest is the generic start-session request above provider code.
type StartRequest struct {
	SessionName string
	Launch      LaunchOptions
}

// ResumeOptions carries provider-neutral resume intent from cmd into the
// provider-owned lifecycle implementation.
type ResumeOptions struct {
	CurrentWorkDir   string
	EnableSelfReload bool
}

// ResumeRequest is the generic resume-session request above provider code.
type ResumeRequest struct {
	Session *Session
	Options ResumeOptions
}

// OpaqueResumeRequest carries a provider-native resume query when clyde does
// not have a registered session row for the target yet.
type OpaqueResumeRequest struct {
	Query          string
	AdditionalArgs []string
}

// ContextMessage is provider-neutral recent conversation text used for generic
// context refresh flows.
type ContextMessage struct {
	Role string
	Text string
}

// DeleteArtifactsRequest carries the provider-owned artifact cleanup request for
// a logical session row.
type DeleteArtifactsRequest struct {
	Session   *Session
	ClydeRoot string
}

// DeletedArtifacts summarizes provider-owned artifacts removed for a session.
type DeletedArtifacts struct {
	Transcripts []string
	AgentLogs   []string
}

// SessionLauncher is the narrow lifecycle contract cmd should use for new
// interactive session startup.
type SessionLauncher interface {
	StartInteractive(ctx context.Context, req StartRequest) error
}

// SessionResumer is the narrow lifecycle contract cmd should use for resuming
// an already-registered interactive session.
type SessionResumer interface {
	ResumeInteractive(ctx context.Context, req ResumeRequest) error
}

// OpaqueSessionResumer is the narrow lifecycle contract cmd should use when it
// needs the provider to resolve an unmanaged upstream session reference.
type OpaqueSessionResumer interface {
	ResumeOpaqueInteractive(ctx context.Context, req OpaqueResumeRequest) error
}

// ResumeInstructionProvider exposes provider-native resume hints without making
// cmd know the exact upstream CLI or session-id shape.
type ResumeInstructionProvider interface {
	ResumeInstructions(sess *Session) []string
}

// ContextMessageProvider exposes provider-owned transcript shaping so generic
// layers do not parse provider transcript files directly.
type ContextMessageProvider interface {
	RecentContextMessages(sess *Session, limit, maxLen int) []ContextMessage
}

// ArtifactCleaner deletes provider-owned files associated with a session row.
type ArtifactCleaner interface {
	DeleteArtifacts(ctx context.Context, req DeleteArtifactsRequest) (*DeletedArtifacts, error)
}

// NormalizeProviderID resolves empty legacy metadata to the current default provider.
func NormalizeProviderID(provider ProviderID) ProviderID {
	trimmed := ProviderID(strings.TrimSpace(string(provider)))
	if trimmed == ProviderUnknown {
		return defaultProviderInfo.ID
	}
	return trimmed
}

// ProviderInfo returns the typed descriptor for a provider id.
func ProviderInfo(provider ProviderID) ProviderInfoRecord {
	normalized := NormalizeProviderID(provider)
	if normalized == defaultProviderInfo.ID {
		return defaultProviderInfo
	}
	if normalized == codexProviderInfo.ID {
		return codexProviderInfo
	}
	return ProviderInfoRecord{ID: normalized}
}
