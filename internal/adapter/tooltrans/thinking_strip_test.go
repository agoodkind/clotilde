package tooltrans

import "testing"

func TestStripThinkingBlockquoteRemovesOurMarker(t *testing.T) {
	in := "<!--clyde-thinking--><details><summary><sub>💭 Thinking…</sub></summary>\n\n<sub>line one\nline two</sub></details><!--/clyde-thinking-->\n\nAnswer: 42"
	got := stripThinkingBlockquote(in)
	want := "Answer: 42"
	if got != want {
		t.Fatalf("strip mismatch:\n got  %q\n want %q", got, want)
	}
}

func TestStripThinkingBlockquotePassThrough(t *testing.T) {
	cases := []string{
		"",
		"Just a plain answer.",
		"> A blockquote the user wrote, not our marker",
		"<details><summary>User-authored details, no sentinel</summary>preserved</details>",
		"Answer with <details> tags that are NOT ours, <details>nested</details>",
	}
	for _, in := range cases {
		if got := stripThinkingBlockquote(in); got != in {
			t.Errorf("stripThinkingBlockquote(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestStripThinkingBlockquoteHandlesMultipleBlocks(t *testing.T) {
	in := "<!--clyde-thinking--><details>first reasoning pass</details><!--/clyde-thinking-->\n\nFirst answer.\n\n<!--clyde-thinking--><details>second reasoning pass</details><!--/clyde-thinking-->\n\nSecond answer."
	got := stripThinkingBlockquote(in)
	want := "First answer.\n\nSecond answer."
	if got != want {
		t.Fatalf("multi-block strip mismatch:\n got  %q\n want %q", got, want)
	}
}

func TestStripThinkingBlockquotePreservesLegitimateDetails(t *testing.T) {
	in := "Here's the plan. <details><summary>Optional steps</summary>step a\nstep b</details> Done."
	got := stripThinkingBlockquote(in)
	if got != in {
		t.Fatalf("should not strip non-sentinel <details>:\n got  %q\n want %q", got, in)
	}
}
