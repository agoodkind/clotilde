package ui

import "github.com/gdamore/tcell/v2"

// Dark theme color palette using standard 256-color values for broad terminal support.
var (
	// Text
	ColorText    = tcell.Color252 // bright white-ish
	ColorSubtext = tcell.Color245 // medium gray
	ColorMuted   = tcell.Color240 // dim gray

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
	ColorRowEven    = tcell.ColorDefault
	ColorRowOdd     = tcell.Color234 // very subtle dark
	ColorSelected   = tcell.Color238 // medium dark for selection
	ColorSelectedFg = tcell.Color231 // pure white
)

// Mode colors for the status bar badge
var (
	ColorModeBrowse  = tcell.Color114 // green
	ColorModeDetail  = tcell.Color75  // blue
	ColorModeSearch  = tcell.Color222 // yellow
	ColorModeCompact = tcell.Color208 // orange
	ColorModeView    = tcell.Color80  // teal
	ColorModeFilter  = tcell.Color141 // purple
)
