package codex

import "testing"

func TestHitRateTrackerRecordsRollingWindow(t *testing.T) {
	tracker := NewHitRateTracker(4)
	rate, size := tracker.Record("conv-1", true)
	if rate != 1 || size != 1 {
		t.Errorf("after 1 hit: rate=%v size=%d, want 1.0/1", rate, size)
	}
	rate, size = tracker.Record("conv-1", false)
	if rate != 0.5 || size != 2 {
		t.Errorf("after 1 hit + 1 miss: rate=%v size=%d, want 0.5/2", rate, size)
	}
	tracker.Record("conv-1", true)
	rate, size = tracker.Record("conv-1", true)
	if rate != 0.75 || size != 4 {
		t.Errorf("after 4 obs (3 hits): rate=%v size=%d, want 0.75/4", rate, size)
	}
}

func TestHitRateTrackerAgesOutOldObservations(t *testing.T) {
	tracker := NewHitRateTracker(3)
	tracker.Record("conv-x", true)
	tracker.Record("conv-x", true)
	tracker.Record("conv-x", true)
	rate, size := tracker.Record("conv-x", false)
	if size != 3 {
		t.Errorf("size should cap at window: got %d", size)
	}
	if rate != 2.0/3.0 {
		t.Errorf("oldest hit should age out: rate=%v, want 2/3", rate)
	}
}

func TestHitRateTrackerSeparateConversations(t *testing.T) {
	tracker := NewHitRateTracker(4)
	tracker.Record("a", true)
	tracker.Record("a", true)
	rateA, _ := tracker.Record("a", true)
	rateB, _ := tracker.Record("b", false)
	if rateA != 1.0 {
		t.Errorf("conv a rate = %v, want 1.0", rateA)
	}
	if rateB != 0 {
		t.Errorf("conv b rate = %v, want 0", rateB)
	}
}

func TestHitRateTrackerEmptyKeyIsNoOp(t *testing.T) {
	tracker := NewHitRateTracker(4)
	rate, size := tracker.Record("", true)
	if rate != 0 || size != 0 {
		t.Errorf("empty key: rate=%v size=%d, want 0/0", rate, size)
	}
}

func TestHitRateTrackerNilSafe(t *testing.T) {
	var nilTracker *HitRateTracker
	if rate, size := nilTracker.Record("conv", true); rate != 0 || size != 0 {
		t.Errorf("nil tracker: rate=%v size=%d, want 0/0", rate, size)
	}
	nilTracker.Forget("conv") // should not panic
}

func TestHitRateTrackerForgetClearsConversation(t *testing.T) {
	tracker := NewHitRateTracker(4)
	tracker.Record("conv-z", true)
	tracker.Record("conv-z", true)
	tracker.Forget("conv-z")
	rate, size := tracker.Record("conv-z", false)
	if rate != 0 || size != 1 {
		t.Errorf("after Forget then 1 miss: rate=%v size=%d, want 0/1", rate, size)
	}
}

func TestHitRateTrackerDefaultWindow(t *testing.T) {
	tracker := NewHitRateTracker(0)
	if tracker.window != defaultHitRateWindow {
		t.Errorf("zero window should fall back to default %d, got %d", defaultHitRateWindow, tracker.window)
	}
}

func TestMismatchDiffSummaryStableAcrossRuns(t *testing.T) {
	a := MismatchDiffSummary("expected", "current")
	b := MismatchDiffSummary("expected", "current")
	if a == "" {
		t.Fatal("expected non-empty summary")
	}
	if a != b {
		t.Errorf("summary should be deterministic: %q vs %q", a, b)
	}
}

func TestMismatchDiffSummaryDifferentForDifferentInputs(t *testing.T) {
	a := MismatchDiffSummary("expected_a", "current")
	b := MismatchDiffSummary("expected_b", "current")
	if a == b {
		t.Errorf("different expected payloads should produce different summaries: %q == %q", a, b)
	}
}

func TestMismatchDiffSummaryEmptyInputs(t *testing.T) {
	if got := MismatchDiffSummary("", ""); got != "" {
		t.Errorf("both empty should produce empty: %q", got)
	}
	if got := MismatchDiffSummary("a", ""); got == "" {
		t.Errorf("one populated should still produce a summary, got empty")
	}
}

func TestContinuationMismatchFieldFromReason(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"output_item_baseline_mismatch", "output_item_baseline"},
		{"input_baseline_mismatch", "input_baseline"},
		{"no_incremental_input", "input"},
		{"", ""},
		{"unknown", "other"},
	}
	for _, tc := range cases {
		if got := continuationMismatchFieldFromReason(tc.reason); got != tc.want {
			t.Errorf("continuationMismatchFieldFromReason(%q) = %q, want %q", tc.reason, got, tc.want)
		}
	}
}
