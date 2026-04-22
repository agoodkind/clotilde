package compact

import (
	"strings"
	"testing"
	"time"

	compactengine "goodkind.io/clyde/internal/compact"
)

func TestPhaseFromStep_ToolsPassLabels(t *testing.T) {
	got := phaseFromStep("tools full -> line-only (oldest 32)")
	if got != "tools pass 1/2 (full -> line-only)" {
		t.Fatalf("phaseFromStep pass1 = %q", got)
	}

	got = phaseFromStep("tools line-only -> drop (oldest 32)")
	if got != "tools pass 2/2 (line-only -> drop)" {
		t.Fatalf("phaseFromStep pass2 = %q", got)
	}
}

func TestComposePanelLines_UsesFriendlySections(t *testing.T) {
	p := &progressView{
		target: 200000,
		mode:   ModePreview,
		upfront: UpfrontStats{
			StaticFloor: 70703,
			Reserved:    13000,
		},
		startedAt: time.Now().Add(-3 * time.Second),
	}
	rec := compactengine.IterationRecord{
		Step:              "tools line-only -> drop (oldest 8)",
		TailTokens:        116297,
		CtxTotal:          200000,
		Delta:             0,
		ThinkingDropped:   true,
		ImagesPlaceholder: true,
		ToolsDropped:      10,
		ChatTurnsTotal:    20,
	}

	lines := p.composePanelLines("⠙", 3*time.Second, 12, rec, rec.Step, humanInt(rec.CtxTotal))
	joined := strings.Join(lines, "\n")

	wantSubstrings := []string{
		"run",
		"target",
		"token math",
		"what changed",
		"current total",
		"target limit",
		"over/under",
		"always-kept",
		"message budget",
		"tools pass 2/2 (line-only -> drop)",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(joined, want) {
			t.Fatalf("panel missing %q\n%s", want, joined)
		}
	}
}
