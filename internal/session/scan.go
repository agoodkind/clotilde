package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type DiscoveryScanner interface {
	Provider() ProviderID
	Scan() ([]DiscoveryResult, error)
}

// ClaudeDiscoveryState captures discovery details that only the Claude scanner
// understands. Generic adoption code should use DiscoveryResult methods instead
// of reaching into this state directly.
type ClaudeDiscoveryState struct {
	TranscriptPath string
}

// DiscoveryResult captures the outcome of a single provider discovery scan.
type DiscoveryResult struct {
	Provider       ProviderID
	Identity       ProviderSessionID
	WorkspaceRoot  string
	Entrypoint     string
	FirstEntryTime time.Time
	CustomTitle    string // user-given chat name from provider metadata
	ForkParent     ProviderSessionID
	IsAutoName     bool // provider invocation that looks like a clyde auto-name call
	IsForked       bool // provider metadata carries fork lineage
	IsSubagent     bool // provider artifact belongs to a subagent/background worker
	Claude         ClaudeDiscoveryState
}

// AdoptedSession is the registry entry created for a previously-unknown
// transcript. It includes the auto-generated name so callers can report.
type AdoptedSession struct {
	Name     string
	Metadata Metadata
}

// scratchDirSuffixes lists workspace-root path fragments produced by
// clyde-internal subprocess invocations. Discovery skips any
// transcript whose cwd matches one of these so the user's session
// list never fills with adapter or context-summary noise.
var scratchDirSuffixes = []string{
	"/Library/Caches/clotilde/context-scratch",
	"/.cache/clotilde/context-scratch",
	"/Library/Caches/clotilde/adapter-scratch",
	"/.cache/clotilde/adapter-scratch",
	"/Library/Caches/clyde/context-scratch",
	"/.cache/clyde/context-scratch",
	"/Library/Caches/clyde/adapter-scratch",
	"/.cache/clyde/adapter-scratch",
}

// isClydeScratch reports whether path looks like a clyde owned
// scratch directory used to anchor internal claude -p calls. The
// match is suffix based so it works whether the user's home is at
// /Users/foo or /home/foo or anywhere else.
func isClydeScratch(path string) bool {
	if path == "" {
		return false
	}
	for _, s := range scratchDirSuffixes {
		if strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}

// looksLikeAutoNamePrompt heuristically detects the prompts clyde
// dispatches to haiku for session naming. The prompt always asks for a
// kebab-case label and includes the words "kebab-case" and "Output ONLY".
func looksLikeAutoNamePrompt(content string) bool {
	if content == "" {
		return false
	}
	c := strings.ToLower(content)
	return strings.Contains(c, "kebab-case") && strings.Contains(c, "output only")
}

// AdoptUnknown creates registry stubs for transcripts that no existing
// session knows about. Sessions that are tagged as auto-name or subagent
// are skipped so the dashboard does not fill with noise. The function
// returns the list of adopted sessions.
func AdoptUnknown(store *FileStore, results []DiscoveryResult) ([]AdoptedSession, error) {
	known, err := buildKnownIdentitySet(store)
	if err != nil {
		sessionAdoptLog.Logger().Warn("session.adopt.known_identities_failed",
			"component", "session",
			"subcomponent", "adopt",
			"err", err,
		)
		return nil, err
	}
	existingNames, err := buildExistingNameSet(store)
	if err != nil {
		sessionAdoptLog.Logger().Warn("session.adopt.existing_names_failed",
			"component", "session",
			"subcomponent", "adopt",
			"err", err,
		)
		return nil, err
	}

	ordered := append([]DiscoveryResult(nil), results...)
	sort.SliceStable(ordered, func(i int, j int) bool {
		left := ordered[i]
		right := ordered[j]
		if left.ProviderSessionKey() == right.ProviderSessionKey() {
			if left.CustomTitle != "" && right.CustomTitle == "" {
				return true
			}
			if right.CustomTitle != "" && left.CustomTitle == "" {
				return false
			}
			if !left.FirstEntryTime.Equal(right.FirstEntryTime) {
				return left.FirstEntryTime.After(right.FirstEntryTime)
			}
			if left.WorkspaceRoot != "" && right.WorkspaceRoot == "" {
				return true
			}
			if right.WorkspaceRoot != "" && left.WorkspaceRoot == "" {
				return false
			}
		}
		if left.ProviderSessionKey() != right.ProviderSessionKey() {
			return left.ProviderSessionKey() < right.ProviderSessionKey()
		}
		return left.PrimaryArtifactPath() < right.PrimaryArtifactPath()
	})

	sessionAdoptLog.Logger().Debug("session.adopt.started",
		"component", "session",
		"subcomponent", "adopt",
		"candidates", len(ordered),
		"known_identities", len(known),
		"existing_names", len(existingNames),
	)

	var adopted []AdoptedSession
	skippedAutoOrSubagent := 0
	skippedScratch := 0
	skippedNoSessionID := 0
	skippedKnown := 0
	createFailed := 0
	for _, r := range ordered {
		if r.IsAutoName || r.IsSubagent {
			skippedAutoOrSubagent++
			continue
		}
		if isClydeScratch(r.WorkspaceRoot) {
			skippedScratch++
			continue
		}
		if r.ProviderSessionID() == "" {
			skippedNoSessionID++
			continue
		}
		if _, ok := known[r.ProviderSessionKey()]; ok {
			skippedKnown++
			continue
		}
		name, nameSource := pickAdoptedName(r, existingNames)
		existingNames[name] = true

		md := Metadata{
			Name:            name,
			Provider:        NormalizeProviderID(r.Provider),
			SessionID:       r.ProviderSessionID(),
			WorkspaceRoot:   r.WorkspaceRoot,
			WorkDir:         r.WorkspaceRoot,
			DisplayTitle:    r.CustomTitle,
			IsForkedSession: r.IsForked,
		}
		md.SetProviderTranscriptPath(r.PrimaryArtifactPath())
		if r.IsForked {
			if parentName, ok := known[r.ParentProviderSessionKey()]; ok {
				md.ParentSession = parentName
			}
		}
		fi, err := os.Stat(r.PrimaryArtifactPath())
		if err == nil {
			md.LastAccessed = fi.ModTime()
		}
		switch {
		case !r.FirstEntryTime.IsZero():
			md.Created = r.FirstEntryTime
		case !md.LastAccessed.IsZero():
			md.Created = md.LastAccessed
		default:
			md.Created = currentTime()
		}
		if md.LastAccessed.IsZero() {
			md.LastAccessed = md.Created
		}

		sess := &Session{Name: name, Metadata: md}
		if err := store.Create(sess); err != nil {
			createFailed++
			sessionAdoptLog.Logger().Warn("session.adopt.create_failed",
				"component", "session",
				"subcomponent", "adopt",
				"session", name,
				"provider", md.Provider,
				"session_id", r.ProviderSessionID(),
				"transcript", r.PrimaryArtifactPath(),
				"err", err,
			)
			continue
		}
		sessionAdoptLog.Logger().Debug("session.adopt.created",
			"component", "session",
			"subcomponent", "adopt",
			"session", name,
			"provider", md.Provider,
			"session_id", r.ProviderSessionID(),
			"forked", md.IsForkedSession,
			"parent_session", md.ParentSession,
			"transcript", r.PrimaryArtifactPath(),
			"workspace", r.WorkspaceRoot,
			"name_source", nameSource,
			"display_title", r.CustomTitle,
		)
		adopted = append(adopted, AdoptedSession{Name: name, Metadata: md})
		known[r.ProviderSessionKey()] = name
	}
	sessionAdoptLog.Logger().Debug("session.adopt.completed",
		"component", "session",
		"subcomponent", "adopt",
		"adopted", len(adopted),
		"considered", len(ordered),
		"skipped_auto_or_subagent", skippedAutoOrSubagent,
		"skipped_clyde_scratch", skippedScratch,
		"skipped_no_session_id", skippedNoSessionID,
		"skipped_already_known", skippedKnown,
		"create_failed", createFailed,
	)
	return adopted, nil
}

// pickAdoptedName chooses a session name for an adopted transcript. It
// prefers the sanitized Claude Code customTitle so clyde verbs accept
// the user-given chat name directly. Collisions with existing names are
// resolved with UniqueName. When customTitle is absent or sanitizes to
// empty (for example an emoji-only title) the function falls back to
// the workspace-plus-UUID scheme in uniqueAdoptedName. The second return
// value is a short label of the source used, for structured logs.
func pickAdoptedName(r DiscoveryResult, taken map[string]bool) (string, string) {
	if sanitized := Sanitize(r.CustomTitle); sanitized != "" {
		candidate := UniqueName(sanitized, taken)
		if candidate != "" && ValidateName(candidate) == nil {
			sessionAdoptLog.Logger().Debug("session.adopt.name_picked",
				"component", "session",
				"subcomponent", "adopt",
				"session_id", r.ProviderSessionID(),
				"source", "custom_title",
				"raw_title", r.CustomTitle,
				"name", candidate,
			)
			return candidate, "custom_title"
		}
		sessionAdoptLog.Logger().Debug("session.adopt.name_sanitize_unusable",
			"component", "session",
			"subcomponent", "adopt",
			"session_id", r.ProviderSessionID(),
			"raw_title", r.CustomTitle,
			"sanitized", sanitized,
		)
	}
	fallback := uniqueAdoptedName(r, taken)
	sessionAdoptLog.Logger().Debug("session.adopt.name_picked",
		"component", "session",
		"subcomponent", "adopt",
		"session_id", r.ProviderSessionID(),
		"source", "workspace_uuid_fallback",
		"raw_title", r.CustomTitle,
		"name", fallback,
	)
	return fallback, "workspace_uuid_fallback"
}

func buildKnownIdentitySet(store *FileStore) (map[string]string, error) {
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(all)*2)
	for _, s := range all {
		for _, id := range HistoricalIdentities(s) {
			if key := id.Key(); key != "" {
				out[key] = s.Name
			}
		}
	}
	return out, nil
}

func buildExistingNameSet(store *FileStore) (map[string]bool, error) {
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(all))
	for _, s := range all {
		out[s.Name] = true
	}
	return out, nil
}

// uniqueAdoptedName generates a registry-safe name for an adopted provider
// artifact. The base is a sanitized basename of the workspace root joined with
// a short provider session id prefix. Collisions are resolved with the shared
// UniqueName helper.
func uniqueAdoptedName(r DiscoveryResult, taken map[string]bool) string {
	base := workspaceBaseName(r.WorkspaceRoot)
	short := safeShortProviderSessionID(r.ProviderSessionID())
	return UniqueName(fmt.Sprintf("%s-%s", base, short), taken)
}

func workspaceBaseName(root string) string {
	if root == "" {
		return "adopted"
	}
	base := filepath.Base(root)
	base = strings.ToLower(base)
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "adopted"
	}
	return b.String()
}

func safeShortProviderSessionID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

func (r DiscoveryResult) ProviderIdentity() ProviderSessionID {
	return r.Identity.NormalizedForProvider(r.Provider)
}

func (r DiscoveryResult) ProviderSessionID() string {
	return r.ProviderIdentity().ID
}

func (r DiscoveryResult) ProviderSessionKey() string {
	return r.ProviderIdentity().Key()
}

func (r DiscoveryResult) ParentProviderSessionKey() string {
	parent := r.ForkParent.NormalizedForProvider(r.Provider)
	return parent.Key()
}

func (r DiscoveryResult) PrimaryArtifactPath() string {
	if r.Claude.TranscriptPath != "" {
		return r.Claude.TranscriptPath
	}
	return ""
}
