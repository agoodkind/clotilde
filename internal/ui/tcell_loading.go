package ui

import (
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
)

const loadingFrameInterval = 100 * time.Millisecond

// LoadingSpinner is the shared TUI loading affordance. Keep loading copy
// routed through this type so panes, overlays, and the status bar animate
// consistently.
type LoadingSpinner struct {
	Label string
	Frame int
	Style tcell.Style
}

func NewLoadingSpinner(label string, frame int) LoadingSpinner {
	return LoadingSpinner{Label: label, Frame: frame, Style: StyleMuted}
}

func ClockLoadingSpinner(label string) LoadingSpinner {
	return NewLoadingSpinner(label, currentLoadingFrame())
}

func (s LoadingSpinner) Text() string {
	label := strings.TrimSpace(s.Label)
	if label == "" {
		label = "loading..."
	}
	return LoadingSpinnerGlyph(s.Frame) + " " + label
}

func (s LoadingSpinner) Segment() TextSegment {
	style := s.Style
	if style == (tcell.Style{}) {
		style = StyleMuted
	}
	return TextSegment{Text: s.Text(), Style: style}
}

func (s LoadingSpinner) Draw(scr tcell.Screen, x, y int, width int) {
	drawString(scr, x, y, s.Segment().Style, s.Text(), width)
}

func LoadingSpinnerGlyph(frame int) string {
	frames := []string{"|", "/", "-", "\\"}
	if frame < 0 {
		frame = 0
	}
	return frames[frame%len(frames)]
}

func currentLoadingFrame() int {
	return int(time.Now().UnixNano() / int64(loadingFrameInterval))
}

func loadingValue(status string) string {
	trimmed := strings.TrimSpace(status)
	switch {
	case trimmed == "", trimmed == "loading...":
		return ClockLoadingSpinner("loading...").Text()
	case strings.HasPrefix(trimmed, "failed"):
		return trimmed
	default:
		return ClockLoadingSpinner(trimmed).Text()
	}
}
