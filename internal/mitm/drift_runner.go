package mitm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// DriftCheckOptions configures one compare-only drift run against the
// current local capture store. The Reference path's snapshot version
// (v1 vs v2) is auto-detected. UA / body-key filters apply only to v2.
type DriftCheckOptions struct {
	Upstream        string
	Reference       string
	CaptureRoot     string
	CACertPath      string // kept for backward-compatible CLI/config shape; compare-only drift does not capture
	DriftLogPath    string
	IncludeUA       []string
	ExcludeUA       []string
	RequireBodyKeys []string
	ForbidBodyKeys  []string
	Log             *slog.Logger
}

// RunDriftCheck performs the snapshot + diff cycle for one upstream
// using the current local capture store and appends the structured
// outcome to DriftLogPath.
// Returns the outcome and a non-nil error on infrastructure failure.
// The outcome's Diverged field reports whether the snapshots
// disagreed; callers may want to escalate divergence separately from
// infrastructure failures.
func RunDriftCheck(ctx context.Context, opts DriftCheckOptions) (DriftOutcome, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if strings.TrimSpace(opts.Reference) == "" {
		return DriftOutcome{}, fmt.Errorf("reference path is required")
	}
	if strings.TrimSpace(opts.DriftLogPath) == "" {
		return DriftOutcome{}, fmt.Errorf("drift log path is required")
	}
	captureRoot := strings.TrimSpace(opts.CaptureRoot)
	if captureRoot == "" {
		captureRoot = DefaultCaptureRoot()
	}
	transcriptPath, err := ResolveTranscriptPath(captureRoot, opts.Upstream)
	if err != nil {
		return DriftOutcome{}, err
	}
	startedAt := time.Now().UTC()

	outcome := DriftOutcome{
		Upstream:       opts.Upstream,
		ReferencePath:  opts.Reference,
		TranscriptPath: transcriptPath,
		StartedAt:      startedAt,
	}

	versionTag := "live-" + startedAt.Format("20060102T150405")
	if isV2SnapshotFile(opts.Reference) {
		ref, err := LoadSnapshotV2TOML(opts.Reference)
		if err != nil {
			return outcome, fmt.Errorf("load v2 reference: %w", err)
		}
		cand, err := ExtractSnapshotV2(transcriptPath, SnapshotV2Options{
			UpstreamName:               opts.Upstream,
			UpstreamVersion:            versionTag,
			ProviderFilter:             ProviderForUpstream(opts.Upstream),
			IncludeUserAgentSubstrings: opts.IncludeUA,
			ExcludeUserAgentSubstrings: opts.ExcludeUA,
			RequireBodyKeys:            opts.RequireBodyKeys,
			ForbidBodyKeys:             opts.ForbidBodyKeys,
		})
		if err != nil {
			return outcome, fmt.Errorf("extract v2: %w", err)
		}
		report := DiffSnapshotsV2(ref, cand)
		outcome.SchemaVersion = "v2"
		outcome.V2 = &report
	} else {
		ref, err := LoadSnapshotTOML(opts.Reference)
		if err != nil {
			return outcome, fmt.Errorf("load reference: %w", err)
		}
		cand, err := ExtractSnapshot(transcriptPath, SnapshotOptions{
			UpstreamName:    opts.Upstream,
			UpstreamVersion: versionTag,
			ProviderFilter:  ProviderForUpstream(opts.Upstream),
		})
		if err != nil {
			return outcome, fmt.Errorf("extract: %w", err)
		}
		report := DiffSnapshots(ref, cand)
		outcome.SchemaVersion = "v1"
		outcome.V1 = &report
	}

	if err := AppendDriftOutcome(opts.DriftLogPath, outcome); err != nil {
		opts.Log.Warn("mitm.drift.log_append_failed", "path", opts.DriftLogPath, "err", err)
	}
	// AppendDriftOutcome populates Diverged + Summary on the in-place
	// outcome. Re-derive here so callers that skip the log path still
	// see those fields populated on the returned value.
	if outcome.SchemaVersion == "v2" && outcome.V2 != nil {
		outcome.Diverged = outcome.V2.HasDiverged()
		outcome.Summary = outcome.V2.SummaryString()
	} else if outcome.SchemaVersion == "v1" && outcome.V1 != nil {
		outcome.Diverged = outcome.V1.HasDiverged()
		outcome.Summary = outcome.V1.SummaryString()
	}
	return outcome, nil
}

// isV2SnapshotFile sniffs a reference TOML for the v2 [[flavors]]
// table. Cheap heuristic; matches what the cli `isV2Snapshot` helper
// does but lives in the mitm package so the daemon can reuse it.
func isV2SnapshotFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return false
	}
	return strings.Contains(string(data), "[[flavors]]")
}
