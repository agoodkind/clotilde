package mitm

import (
	"strings"
	"testing"
)

func TestDiffSnapshotsCleanWhenIdentical(t *testing.T) {
	snap := Snapshot{
		Upstream: SnapshotUpstream{Name: "codex-cli"},
		Body:     SnapshotBody{Type: "response.create", FieldNames: []string{"input", "model"}},
		Constants: SnapshotConstants{
			Originator: "codex_exec",
			OpenAIBeta: "responses_websockets=2026-02-06",
		},
		FrameSequence: SnapshotFrameSequence{Opening: "warmup_then_real_chained", ChainsPrev: true},
	}
	report := DiffSnapshots(snap, snap)
	if report.HasDiverged() {
		t.Errorf("expected clean diff, got %s", report.SummaryString())
	}
}

func TestDiffSnapshotsDetectsConstantDrift(t *testing.T) {
	ref := Snapshot{
		Constants: SnapshotConstants{Originator: "codex_exec", OpenAIBeta: "responses_websockets=2026-02-06"},
	}
	cand := Snapshot{
		Constants: SnapshotConstants{Originator: "Codex Desktop", OpenAIBeta: "responses_websockets=2026-02-06"},
	}
	report := DiffSnapshots(ref, cand)
	if !report.HasDiverged() {
		t.Fatalf("expected divergence")
	}
	found := false
	for _, m := range report.Mismatches {
		if m.Field == "constants.originator" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected originator mismatch, got %s", report.SummaryString())
	}
}

func TestDiffSnapshotsDetectsExtraAndMissingFields(t *testing.T) {
	ref := Snapshot{
		Body: SnapshotBody{FieldNames: []string{"input", "model", "tools"}},
	}
	cand := Snapshot{
		Body: SnapshotBody{FieldNames: []string{"input", "model", "extra_field"}},
	}
	report := DiffSnapshots(ref, cand)
	if len(report.Missing) == 0 || len(report.Extra) == 0 {
		t.Fatalf("expected missing+extra, got %s", report.SummaryString())
	}
	missingFound := false
	for _, m := range report.Missing {
		if m.Expected == "tools" {
			missingFound = true
		}
	}
	if !missingFound {
		t.Errorf("expected 'tools' missing, got %v", report.Missing)
	}
	extraFound := false
	for _, m := range report.Extra {
		if m.Got == "extra_field" {
			extraFound = true
		}
	}
	if !extraFound {
		t.Errorf("expected 'extra_field' as extra, got %v", report.Extra)
	}
}

func TestDiffSnapshotsDetectsHeaderValueDrift(t *testing.T) {
	ref := Snapshot{
		Handshake: SnapshotHandshake{
			Headers: []SnapshotHeader{
				{Name: "openai-beta", Value: "responses_websockets=2026-02-06"},
			},
		},
	}
	cand := Snapshot{
		Handshake: SnapshotHandshake{
			Headers: []SnapshotHeader{
				{Name: "openai-beta", Value: "responses_websockets=2026-09-01"},
			},
		},
	}
	report := DiffSnapshots(ref, cand)
	if len(report.Missing) == 0 {
		t.Fatalf("expected header drift, got %s", report.SummaryString())
	}
	if !strings.Contains(report.Missing[0].Field, "openai-beta") {
		t.Errorf("expected openai-beta drift, got %s", report.SummaryString())
	}
}
