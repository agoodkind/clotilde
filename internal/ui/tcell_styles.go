package ui

import "github.com/gdamore/tcell/v2"

// Dark theme color palette using standard 256 color values for broad
// terminal support. Used exclusively by the raw tcell TUI (app.go and
// its widget files). The lipgloss palette in styles.go is still used by
// output.go and the legacy subcommand UIs.
var (
	// Text
	ColorText    = tcell.Color255 // bright white
	ColorSubtext = tcell.Color252 // light gray (readable)
	ColorMuted   = tcell.Color245 // medium gray (headers, hints)

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
	ColorHeaderBg   = tcell.Color236 // dark gray
	ColorSelected   = tcell.Color237 // subtle selection highlight
	ColorSelectedFg = tcell.Color255 // bright white
	ColorBorder     = tcell.Color238 // thin border color
	ColorStatusBg   = tcell.Color236 // status bar background

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
	StyleHeaderBar  = tcell.StyleDefault.Background(ColorHeaderBg).Foreground(ColorText)
	StyleStatusBar  = tcell.StyleDefault.Background(ColorStatusBg).Foreground(ColorMuted)
	StyleStatusBold = tcell.StyleDefault.Background(ColorStatusBg).Foreground(ColorText)
)
