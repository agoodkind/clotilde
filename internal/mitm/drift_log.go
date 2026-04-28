package mitm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DriftOutcome is one entry in the drift log. Exactly one of V1 or V2
// is populated, matching SchemaVersion. The TranscriptPath points at
// the JSONL capture that produced the candidate snapshot so the run
// can be reconstructed later.
type DriftOutcome struct {
	Timestamp      time.Time     `json:"timestamp"`
	Upstream       string        `json:"upstream"`
	SchemaVersion  string        `json:"schema_version"`
	ReferencePath  string        `json:"reference_path"`
	TranscriptPath string        `json:"transcript_path"`
	StartedAt      time.Time     `json:"started_at"`
	Diverged       bool          `json:"diverged"`
	Summary        string        `json:"summary"`
	V1             *DiffReport   `json:"v1,omitempty"`
	V2             *DiffReportV2 `json:"v2,omitempty"`
}

// AppendDriftOutcome serializes the outcome and appends it to a
// JSONL log. Creates parent directories on first write. The Timestamp,
// Diverged, and Summary fields are populated automatically when zero.
func AppendDriftOutcome(path string, outcome DriftOutcome) error {
	if path == "" {
		return fmt.Errorf("drift log path is empty")
	}
	if outcome.Timestamp.IsZero() {
		outcome.Timestamp = time.Now().UTC()
	}
	switch outcome.SchemaVersion {
	case "v2":
		if outcome.V2 != nil {
			outcome.Diverged = outcome.V2.HasDiverged()
			outcome.Summary = outcome.V2.SummaryString()
		}
	case "v1":
		if outcome.V1 != nil {
			outcome.Diverged = outcome.V1.HasDiverged()
			outcome.Summary = outcome.V1.SummaryString()
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("drift log mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("drift log open: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(outcome); err != nil {
		return fmt.Errorf("drift log encode: %w", err)
	}
	return nil
}
