package ui

import "testing"

func TestTerminalThemeFromSignalsPrefersExplicitOverride(t *testing.T) {
	if got := terminalThemeFromSignals("light", "0;0", "Dark"); got != terminalThemeLight {
		t.Fatalf("explicit light theme = %v, want light", got)
	}
	if got := terminalThemeFromSignals("dark", "0;15", ""); got != terminalThemeDark {
		t.Fatalf("explicit dark theme = %v, want dark", got)
	}
}

func TestTerminalThemeFromColorFGBG(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  terminalTheme
		ok    bool
	}{
		{name: "dark background", value: "15;0", want: terminalThemeDark, ok: true},
		{name: "light background", value: "0;15", want: terminalThemeLight, ok: true},
		{name: "xterm light gray", value: "0;252", want: terminalThemeLight, ok: true},
		{name: "missing", value: "", want: terminalThemeDark, ok: false},
		{name: "invalid", value: "0;nope", want: terminalThemeDark, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := terminalThemeFromColorFGBG(tt.value)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("terminalThemeFromColorFGBG(%q) = (%v, %v), want (%v, %v)", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestTerminalThemeFromSignalsUsesMacAppearanceFallback(t *testing.T) {
	if got := terminalThemeFromSignals("", "", ""); got != terminalThemeDark {
		t.Fatalf("empty fallback = %v, want dark", got)
	}
	if got := terminalThemeFromSignals("", "", "Light"); got != terminalThemeLight {
		t.Fatalf("macOS light fallback = %v, want light", got)
	}
	if got := terminalThemeFromSignals("", "", "Dark"); got != terminalThemeDark {
		t.Fatalf("macOS dark fallback = %v, want dark", got)
	}
}
