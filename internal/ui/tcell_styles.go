package ui

import "github.com/gdamore/tcell/v2"

type tuiPalette struct {
	Text    tcell.Color
	Subtext tcell.Color
	Muted   tcell.Color

	Accent  tcell.Color
	Success tcell.Color
	Warning tcell.Color
	Error   tcell.Color
	Info    tcell.Color

	Fork      tcell.Color
	Incognito tcell.Color
	Cyan      tcell.Color

	ModelOpus   tcell.Color
	ModelSonnet tcell.Color
	ModelHaiku  tcell.Color

	HeaderBg    tcell.Color
	HeaderFg    tcell.Color
	TabBg       tcell.Color
	TabFg       tcell.Color
	TabActive   tcell.Color
	TabActiveFg tcell.Color
	Selected    tcell.Color
	SelectedFg  tcell.Color
	Border      tcell.Color
	StatusBg    tcell.Color

	ModeBrowse  tcell.Color
	ModeDetail  tcell.Color
	ModeSearch  tcell.Color
	ModeCompact tcell.Color
	ModeView    tcell.Color
	ModeFilter  tcell.Color

	DimOverlay tcell.Color
}

var (
	// Text
	ColorText    tcell.Color
	ColorSubtext tcell.Color
	ColorMuted   tcell.Color

	// Accents
	ColorAccent  tcell.Color
	ColorSuccess tcell.Color
	ColorWarning tcell.Color
	ColorError   tcell.Color
	ColorInfo    tcell.Color

	// Semantic
	ColorFork      tcell.Color
	ColorIncognito tcell.Color
	ColorCyan      tcell.Color

	// Models
	ColorModelOpus   tcell.Color
	ColorModelSonnet tcell.Color
	ColorModelHaiku  tcell.Color

	// Table
	ColorHeaderBg   tcell.Color
	ColorHeaderFg   tcell.Color
	ColorTabBg      tcell.Color
	ColorTabFg      tcell.Color
	ColorTabActive  tcell.Color
	ColorTabActiveF tcell.Color
	ColorSelected   tcell.Color
	ColorSelectedFg tcell.Color
	ColorBorder     tcell.Color
	ColorStatusBg   tcell.Color

	// Mode badge colors
	ColorModeBrowse  tcell.Color
	ColorModeDetail  tcell.Color
	ColorModeSearch  tcell.Color
	ColorModeCompact tcell.Color
	ColorModeView    tcell.Color
	ColorModeFilter  tcell.Color

	// Overlay
	ColorDimOverlay tcell.Color
)

// Pre-built styles for common use.
var (
	StyleDefault    tcell.Style
	StyleSubtext    tcell.Style
	StyleMuted      tcell.Style
	StyleHeader     tcell.Style
	StyleSelected   tcell.Style
	StyleHeaderBar  tcell.Style
	StyleTabBar     tcell.Style
	StyleTabActive  tcell.Style
	StyleStatusBar  tcell.Style
	StyleStatusBold tcell.Style
)

var detectedTerminalTheme terminalTheme

func init() {
	applyTUITheme(detectTerminalTheme())
}

func applyTUITheme(theme terminalTheme) {
	p := tuiPaletteForTheme(theme)
	detectedTerminalTheme = theme

	ColorText = p.Text
	ColorSubtext = p.Subtext
	ColorMuted = p.Muted
	ColorAccent = p.Accent
	ColorSuccess = p.Success
	ColorWarning = p.Warning
	ColorError = p.Error
	ColorInfo = p.Info
	ColorFork = p.Fork
	ColorIncognito = p.Incognito
	ColorCyan = p.Cyan
	ColorModelOpus = p.ModelOpus
	ColorModelSonnet = p.ModelSonnet
	ColorModelHaiku = p.ModelHaiku
	ColorHeaderBg = p.HeaderBg
	ColorHeaderFg = p.HeaderFg
	ColorTabBg = p.TabBg
	ColorTabFg = p.TabFg
	ColorTabActive = p.TabActive
	ColorTabActiveF = p.TabActiveFg
	ColorSelected = p.Selected
	ColorSelectedFg = p.SelectedFg
	ColorBorder = p.Border
	ColorStatusBg = p.StatusBg
	ColorModeBrowse = p.ModeBrowse
	ColorModeDetail = p.ModeDetail
	ColorModeSearch = p.ModeSearch
	ColorModeCompact = p.ModeCompact
	ColorModeView = p.ModeView
	ColorModeFilter = p.ModeFilter
	ColorDimOverlay = p.DimOverlay

	StyleDefault = tcell.StyleDefault.Foreground(ColorText)
	StyleSubtext = tcell.StyleDefault.Foreground(ColorSubtext)
	StyleMuted = tcell.StyleDefault.Foreground(ColorMuted)
	StyleHeader = tcell.StyleDefault.Foreground(ColorMuted).Bold(true).Underline(true)
	StyleSelected = tcell.StyleDefault.Foreground(ColorSelectedFg).Background(ColorSelected).Bold(true)
	StyleHeaderBar = tcell.StyleDefault.Background(ColorHeaderBg).Foreground(ColorHeaderFg)
	StyleTabBar = tcell.StyleDefault.Background(ColorTabBg).Foreground(ColorTabFg)
	StyleTabActive = tcell.StyleDefault.Background(ColorTabActive).Foreground(ColorTabActiveF).Bold(true)
	StyleStatusBar = tcell.StyleDefault.Background(ColorStatusBg).Foreground(ColorMuted)
	StyleStatusBold = tcell.StyleDefault.Background(ColorStatusBg).Foreground(ColorText)
}

func tuiPaletteForTheme(theme terminalTheme) tuiPalette {
	if theme == terminalThemeLight {
		return tuiPalette{
			Text:    tcell.ColorDefault,
			Subtext: tcell.Color238,
			Muted:   tcell.Color240,

			Accent:  tcell.Color25,
			Success: tcell.Color28,
			Warning: tcell.Color130,
			Error:   tcell.Color160,
			Info:    tcell.Color25,

			Fork:      tcell.Color130,
			Incognito: tcell.Color90,
			Cyan:      tcell.Color30,

			ModelOpus:   tcell.Color130,
			ModelSonnet: tcell.Color25,
			ModelHaiku:  tcell.Color28,

			HeaderBg:    tcell.Color60,
			HeaderFg:    tcell.Color231,
			TabBg:       tcell.Color61,
			TabFg:       tcell.Color231,
			TabActive:   tcell.ColorDefault,
			TabActiveFg: tcell.Color61,
			Selected:    tcell.Color252,
			SelectedFg:  tcell.Color16,
			Border:      tcell.Color244,
			StatusBg:    tcell.Color254,

			ModeBrowse:  tcell.Color35,
			ModeDetail:  tcell.Color33,
			ModeSearch:  tcell.Color136,
			ModeCompact: tcell.Color166,
			ModeView:    tcell.Color30,
			ModeFilter:  tcell.Color96,

			DimOverlay: tcell.Color252,
		}
	}
	return tuiPalette{
		Text:    tcell.ColorDefault,
		Subtext: tcell.ColorGray,
		Muted:   tcell.ColorGray,

		Accent:  tcell.Color75,
		Success: tcell.Color114,
		Warning: tcell.Color222,
		Error:   tcell.Color204,
		Info:    tcell.Color75,

		Fork:      tcell.Color222,
		Incognito: tcell.Color141,
		Cyan:      tcell.Color80,

		ModelOpus:   tcell.Color222,
		ModelSonnet: tcell.Color75,
		ModelHaiku:  tcell.Color114,

		HeaderBg:    tcell.Color61,
		HeaderFg:    tcell.Color231,
		TabBg:       tcell.Color99,
		TabFg:       tcell.Color231,
		TabActive:   tcell.ColorDefault,
		TabActiveFg: tcell.Color99,
		Selected:    tcell.ColorGray,
		SelectedFg:  tcell.ColorDefault,
		Border:      tcell.ColorGray,
		StatusBg:    tcell.Color236,

		ModeBrowse:  tcell.Color114,
		ModeDetail:  tcell.Color75,
		ModeSearch:  tcell.Color222,
		ModeCompact: tcell.Color208,
		ModeView:    tcell.Color80,
		ModeFilter:  tcell.Color141,

		DimOverlay: tcell.Color234,
	}
}
