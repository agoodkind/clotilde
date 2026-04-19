package compact

import "testing"

func TestParseTokenCount_Valid(t *testing.T) {
	cases := []struct {
		in  string
		out int
	}{
		{"200k", 200_000},
		{"200K", 200_000},
		{"1m", 1_000_000},
		{"1.5m", 1_500_000},
		{"0.5m", 500_000},
		{"120000", 120_000},
		{"120,000", 120_000},
		{"1,234,567", 1_234_567},
		{"  500  ", 500},
		{"500", 500},
		{"0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseTokenCount(tc.in)
			if err != nil {
				t.Fatalf("ParseTokenCount(%q) unexpected err: %v", tc.in, err)
			}
			if got != tc.out {
				t.Fatalf("ParseTokenCount(%q) = %d, want %d", tc.in, got, tc.out)
			}
		})
	}
}

func TestParseTokenCount_Invalid(t *testing.T) {
	bads := []string{
		"",
		"  ",
		"abc",
		"k",
		"m",
		"1.2.3",
		"-1",
		"-500k",
	}
	for _, s := range bads {
		t.Run(s, func(t *testing.T) {
			if _, err := ParseTokenCount(s); err == nil {
				t.Fatalf("ParseTokenCount(%q) expected error", s)
			}
		})
	}
}
