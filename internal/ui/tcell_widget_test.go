package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestTerminalThemeFromSignalsPrefersExplicitOverride(t *testing.T) {
	if got := terminalThemeFromSignals("light", "0;0", "Dark"); got != terminalThemeLight {
		t.Fatalf("explicit light theme = %v, want light", got)
	}
	if got := terminalThemeFromSignals("dark", "0;15", ""); got != terminalThemeDark {
		t.Fatalf("explicit dark theme = %v, want dark", got)
	}
	if got := terminalThemeFromSignals("unknown", "0;15", "Dark"); got != terminalThemeLight {
		t.Fatalf("invalid explicit theme = %v, want COLORFGBG light fallback", got)
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

func TestApplyTUIThemeBuildsReadableLightPalette(t *testing.T) {
	defer applyTUITheme(detectedTerminalTheme)

	applyTUITheme(terminalThemeLight)

	if detectedTerminalTheme != terminalThemeLight {
		t.Fatalf("detectedTerminalTheme = %v, want light", detectedTerminalTheme)
	}
	for name, color := range map[string]tcell.Color{
		"subtext":     ColorSubtext,
		"muted":       ColorMuted,
		"model_opus":  ColorModelOpus,
		"fork":        ColorFork,
		"warning":     ColorWarning,
		"selected_fg": ColorSelectedFg,
	} {
		if isPaleDarkThemeColor(color) {
			t.Fatalf("%s uses pale dark-theme color %v in light palette", name, color)
		}
	}
	if ColorText != tcell.ColorDefault {
		t.Fatalf("ColorText = %v, want terminal default", ColorText)
	}
	if ColorStatusBg == tcell.Color236 {
		t.Fatalf("ColorStatusBg kept dark status background in light palette")
	}

	fg, _, _ := StyleSubtext.Decompose()
	if fg != ColorSubtext {
		t.Fatalf("StyleSubtext fg = %v, want %v", fg, ColorSubtext)
	}
	fg, bg, _ := StyleStatusBar.Decompose()
	if fg != ColorMuted || bg != ColorStatusBg {
		t.Fatalf("StyleStatusBar = fg %v bg %v, want fg %v bg %v", fg, bg, ColorMuted, ColorStatusBg)
	}
}

func TestApplyTUIThemeRestoresDarkPalette(t *testing.T) {
	defer applyTUITheme(detectedTerminalTheme)

	applyTUITheme(terminalThemeLight)
	applyTUITheme(terminalThemeDark)

	if ColorModelOpus != tcell.Color222 {
		t.Fatalf("ColorModelOpus = %v, want dark palette warm yellow", ColorModelOpus)
	}
	if ColorStatusBg != tcell.Color236 {
		t.Fatalf("ColorStatusBg = %v, want dark palette status background", ColorStatusBg)
	}
	if ColorDimOverlay != tcell.Color234 {
		t.Fatalf("ColorDimOverlay = %v, want dark palette overlay", ColorDimOverlay)
	}
}

func TestTableDrawUsesForcedLightPalette(t *testing.T) {
	defer applyTUITheme(detectedTerminalTheme)
	applyTUITheme(terminalThemeLight)

	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	scr.SetSize(40, 5)
	table := NewTableWidget([]string{"NAME", "MODEL"})
	table.Rows = [][]TableCell{{
		{Text: "alpha", Style: StyleDefault.Foreground(ColorText)},
		{Text: "opus", Style: StyleDefault.Foreground(ColorModelOpus)},
	}}
	table.Draw(scr, Rect{X: 0, Y: 0, W: 40, H: 5})

	_, nameStyle, _ := scr.Get(0, 1)
	nameFG, _, _ := nameStyle.Decompose()
	if nameFG != ColorText {
		t.Fatalf("name cell fg = %v, want %v", nameFG, ColorText)
	}

	modelX := len("alpha") + table.ColGaps
	_, modelStyle, _ := scr.Get(modelX, 1)
	modelFG, _, _ := modelStyle.Decompose()
	if modelFG != ColorModelOpus {
		t.Fatalf("model cell fg = %v, want %v", modelFG, ColorModelOpus)
	}
	if isPaleDarkThemeColor(modelFG) {
		t.Fatalf("model cell kept unreadable pale dark-theme fg %v", modelFG)
	}
}

func isPaleDarkThemeColor(color tcell.Color) bool {
	switch color {
	case tcell.ColorGray, tcell.Color222, tcell.Color231, tcell.Color252:
		return true
	default:
		return false
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
