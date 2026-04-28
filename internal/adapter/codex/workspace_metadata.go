package codex

import (
	"bytes"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// WorkspaceProbe inspects a workspace path with three small git
// commands and returns the typed metadata Codex Desktop ships in
// `x-codex-turn-metadata.workspaces`. Results are cached for
// `cacheTTL` so a burst of Cursor turns on the same workspace does
// not fork-bomb git.
//
// The probe is best-effort. Any git failure leaves the field
// empty rather than failing the request. The most-common case is
// a workspace that is not a git repo at all; we return an entry
// with the path but no origin or commit.
type WorkspaceProbe struct {
	mu       sync.Mutex
	cache    map[string]workspaceCacheEntry
	cacheTTL time.Duration
	now      func() time.Time
	runner   workspaceCommandRunner
}

type workspaceCacheEntry struct {
	value   TurnMetadataWorkspace
	fetched time.Time
}

type workspaceCommandRunner interface {
	run(workdir string, args ...string) ([]byte, error)
}

type workspaceGitRunner struct{}

func (workspaceGitRunner) run(workdir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", workdir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

const defaultWorkspaceProbeTTL = 30 * time.Second

// NewWorkspaceProbe constructs a probe with a 30s cache window.
func NewWorkspaceProbe() *WorkspaceProbe {
	return newWorkspaceProbeWith(workspaceGitRunner{}, defaultWorkspaceProbeTTL, time.Now)
}

func newWorkspaceProbeWith(runner workspaceCommandRunner, ttl time.Duration, now func() time.Time) *WorkspaceProbe {
	if now == nil {
		now = time.Now
	}
	return &WorkspaceProbe{
		cache:    map[string]workspaceCacheEntry{},
		cacheTTL: ttl,
		now:      now,
		runner:   runner,
	}
}

// Probe returns the cached TurnMetadataWorkspace for path, refreshing
// it when the cache entry is older than cacheTTL.
func (p *WorkspaceProbe) Probe(path string) TurnMetadataWorkspace {
	path = strings.TrimSpace(path)
	if path == "" {
		return TurnMetadataWorkspace{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry, ok := p.cache[path]; ok && p.now().Sub(entry.fetched) < p.cacheTTL {
		return entry.value
	}
	value := p.fetch(path)
	p.cache[path] = workspaceCacheEntry{value: value, fetched: p.now()}
	return value
}

func (p *WorkspaceProbe) fetch(path string) TurnMetadataWorkspace {
	out := TurnMetadataWorkspace{}
	if origin, err := p.runner.run(path, "config", "--get", "remote.origin.url"); err == nil {
		if v := strings.TrimSpace(string(origin)); v != "" {
			out.AssociatedRemoteURLs.Origin = v
		}
	}
	if head, err := p.runner.run(path, "rev-parse", "HEAD"); err == nil {
		if v := strings.TrimSpace(string(head)); v != "" {
			out.LatestGitCommitHash = v
		}
	}
	if status, err := p.runner.run(path, "status", "--porcelain"); err == nil {
		out.HasChanges = strings.TrimSpace(string(status)) != ""
	}
	return out
}
