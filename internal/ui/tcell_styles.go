package ui

import "github.com/gdamore/tcell/v2"

// Terminal-adaptive palette using default foreground/background for core
// text so the TUI follows both dark and light terminal themes. Fixed
// 256-color values are reserved for accents, badges, and structural bars.
// The lipgloss palette in styles.go is still used by output.go and the
// legacy subcommand UIs.
var (
	// Text
	ColorText    = tcell.ColorDefault // terminal theme foreground
	ColorSubtext = tcell.ColorGray    // readable secondary text on dark/light
	ColorMuted   = tcell.ColorGray    // headers, hints, disabled controls

	// Accents
	ColorAccent  = tcell.Color75  // light blue
	ColorSuccess = tcell.Color114 // soft green
	ColorWarning = tcell.Color222 // warm yellow
	ColorError   = tcell.Color204 // soft red/pink
	ColorInfo    = tcell.Color75  // light blue

	// Semantic
	ColorFork      = tcell.Color222 // warm yellow for forks
	ColorIncognito = tcell.Color141 // light purple
	ColorCyan      = tcell.Color80  // teal

	// Models
	ColorModelOpus   = tcell.Color222 // warm yellow
	ColorModelSonnet = tcell.Color75  // light blue
	ColorModelHaiku  = tcell.Color114 // soft green

	// Table
	ColorHeaderBg   = tcell.Color61      // muted purple body header band
	ColorHeaderFg   = tcell.Color231     // bright white text on the header
	ColorTabBg      = tcell.Color99      // bright purple for the tab strip
	ColorTabFg      = tcell.Color231     // bright white text on tabs
	ColorTabActive  = tcell.ColorDefault // active tab follows terminal background
	ColorTabActiveF = tcell.Color99      // active tab text matches strip color
	ColorSelected   = tcell.ColorGray    // neutral selection highlight
	ColorSelectedFg = tcell.ColorDefault
	ColorBorder     = tcell.ColorGray // thin border color
	ColorStatusBg   = tcell.Color236  // status bar background

	// Mode badge colors
	ColorModeBrowse  = tcell.Color114 // green
	ColorModeDetail  = tcell.Color75  // blue
	ColorModeSearch  = tcell.Color222 // yellow
	ColorModeCompact = tcell.Color208 // orange
	ColorModeView    = tcell.Color80  // teal
	ColorModeFilter  = tcell.Color141 // purple
)

// Pre-built styles for common use.
var (
	StyleDefault    = tcell.StyleDefault.Foreground(ColorText)
	StyleSubtext    = tcell.StyleDefault.Foreground(ColorSubtext)
	StyleMuted      = tcell.StyleDefault.Foreground(ColorMuted)
	StyleHeader     = tcell.StyleDefault.Foreground(ColorMuted).Bold(true).Underline(true)
	StyleSelected   = tcell.StyleDefault.Foreground(ColorSelectedFg).Background(ColorSelected).Bold(true)
	StyleHeaderBar  = tcell.StyleDefault.Background(ColorHeaderBg).Foreground(ColorHeaderFg)
	StyleTabBar     = tcell.StyleDefault.Background(ColorTabBg).Foreground(ColorTabFg)
	StyleTabActive  = tcell.StyleDefault.Background(ColorTabActive).Foreground(ColorTabActiveF).Bold(true)
	StyleStatusBar  = tcell.StyleDefault.Background(ColorStatusBg).Foreground(ColorMuted)
	StyleStatusBold = tcell.StyleDefault.Background(ColorStatusBg).Foreground(ColorText)
)
