package ui

import "github.com/gdamore/tcell/v2"

// tcell colors matching the existing palette
var (
	ColorSuccess   = tcell.ColorGreen
	ColorFork      = tcell.ColorYellow
	ColorIncognito = tcell.ColorPurple
	ColorMuted     = tcell.Color240 // gray
	ColorInfo      = tcell.ColorBlue
	ColorCyan      = tcell.NewRGBColor(0, 215, 215) // #00D7D7
	ColorWarning   = tcell.ColorOrange
	ColorError     = tcell.ColorRed
)

// Mode colors for the status bar
var (
	ColorModeBrowse  = tcell.ColorGreen
	ColorModeDetail  = tcell.ColorBlue
	ColorModeSearch  = tcell.ColorYellow
	ColorModeCompact = tcell.ColorOrange
	ColorModeView    = tcell.ColorTeal
	ColorModeFilter  = tcell.ColorPurple
)
