package ui

import "github.com/gdamore/tcell/v2"

// Catppuccin Mocha-inspired color palette for a cohesive dark theme.
var (
	// Base colors
	ColorBase     = tcell.NewRGBColor(30, 30, 46)    // #1E1E2E background
	ColorSurface  = tcell.NewRGBColor(49, 50, 68)    // #313244 surface/elevated
	ColorOverlay  = tcell.NewRGBColor(69, 71, 90)    // #45475A borders/dividers
	ColorSubtext  = tcell.NewRGBColor(166, 173, 200) // #A6ADC8 secondary text
	ColorText     = tcell.NewRGBColor(205, 214, 244) // #CDD6F4 primary text

	// Accent colors
	ColorAccent   = tcell.NewRGBColor(137, 180, 250) // #89B4FA blue (primary accent)
	ColorSuccess  = tcell.NewRGBColor(166, 227, 161) // #A6E3A1 green
	ColorWarning  = tcell.NewRGBColor(249, 226, 175) // #F9E2AF yellow/peach
	ColorError    = tcell.NewRGBColor(243, 139, 168) // #F38BA8 red/pink
	ColorInfo     = tcell.NewRGBColor(137, 180, 250) // #89B4FA blue (same as accent)

	// Semantic colors
	ColorFork      = tcell.NewRGBColor(249, 226, 175) // #F9E2AF peach for forks
	ColorIncognito = tcell.NewRGBColor(203, 166, 247) // #CBA6F7 mauve for incognito
	ColorMuted     = tcell.NewRGBColor(108, 112, 134) // #6C7086 muted/dimmed
	ColorCyan      = tcell.NewRGBColor(148, 226, 213) // #94E2D5 teal

	// Model colors (subtle, not screaming)
	ColorModelOpus   = tcell.NewRGBColor(249, 226, 175) // #F9E2AF warm gold
	ColorModelSonnet = tcell.NewRGBColor(137, 180, 250) // #89B4FA blue
	ColorModelHaiku  = tcell.NewRGBColor(166, 227, 161) // #A6E3A1 green

	// Table
	ColorHeaderBg    = tcell.NewRGBColor(49, 50, 68)    // #313244
	ColorRowEven     = tcell.ColorDefault
	ColorRowOdd      = tcell.NewRGBColor(35, 35, 52)    // slightly lighter than base
	ColorSelected    = tcell.NewRGBColor(88, 91, 112)    // #585B70 brighter than header
	ColorSelectedFg  = tcell.NewRGBColor(255, 255, 255)  // pure white on selection
)

// Mode colors for the status bar badge
var (
	ColorModeBrowse  = tcell.NewRGBColor(166, 227, 161) // green
	ColorModeDetail  = tcell.NewRGBColor(137, 180, 250) // blue
	ColorModeSearch  = tcell.NewRGBColor(249, 226, 175) // peach
	ColorModeCompact = tcell.NewRGBColor(250, 179, 135) // #FAB387 orange/peach
	ColorModeView    = tcell.NewRGBColor(148, 226, 213) // teal
	ColorModeFilter  = tcell.NewRGBColor(203, 166, 247) // mauve
)
