package ui

import (
	"strings"
	"testing"
)

func TestLegendForCompactNoLongerUsesTabHint(t *testing.T) {
	segs := legendFor(StatusCompact)
	joined := ""
	for _, seg := range segs {
		joined += seg.Text
	}
	if strings.Contains(joined, "tab") {
		t.Fatalf("expected compact legend to avoid tab hint: %q", joined)
	}
	if !strings.Contains(joined, "enter/spc") {
		t.Fatalf("expected compact legend to include enter/spc: %q", joined)
	}
}

func TestLegendForBrowseDifferentiatesEnterAndSpace(t *testing.T) {
	segs := legendFor(StatusBrowse)
	joined := ""
	for _, seg := range segs {
		joined += seg.Text
	}
	if !strings.Contains(joined, "enter/O select option") {
		t.Fatalf("expected browse legend to include enter option action: %q", joined)
	}
	if !strings.Contains(joined, "space select detail") {
		t.Fatalf("expected browse legend to include space detail action: %q", joined)
	}
}
