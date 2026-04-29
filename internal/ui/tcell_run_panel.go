package ui

import (
	"fmt"
	"strings"
)

// MarkedSlider is a small reusable slider renderer for discrete
// checkpoint-style controls. Selected points are wrapped in brackets;
// everything to the right is rendered as included.
type MarkedSlider struct {
	Marks    []string
	Selected int
}

func (s MarkedSlider) Render() string {
	if len(s.Marks) == 0 {
		return "[]"
	}
	return s.renderWithSpacer("........", "========")
}

func (s MarkedSlider) RenderForWidth(width int) string {
	full := s.Render()
	if width <= 0 || runeCount(full) <= width {
		return full
	}
	return s.windowed().renderWithSpacer("....", "====")
}

func (s MarkedSlider) renderWithSpacer(beforeSelected string, afterSelected string) string {
	selected := clamp(s.Selected, 0, len(s.Marks)-1)
	var b strings.Builder
	b.WriteString("[")
	for i, mark := range s.Marks {
		if i > 0 {
			if i > selected {
				b.WriteString(afterSelected)
			} else {
				b.WriteString(beforeSelected)
			}
		}
		if i == selected {
			b.WriteString("[")
			b.WriteString(mark)
			b.WriteString("]")
			continue
		}
		b.WriteString(mark)
	}
	b.WriteString("]")
	return b.String()
}

func (s MarkedSlider) windowed() MarkedSlider {
	if len(s.Marks) <= 8 {
		return s
	}
	selected := clamp(s.Selected, 0, len(s.Marks)-1)
	last := len(s.Marks) - 1
	keep := map[int]bool{
		0:        true,
		selected: true,
		last:     true,
	}
	if last > 0 {
		keep[last-1] = true
	}
	for i := selected - 1; i <= selected+1; i++ {
		if i >= 0 && i <= last {
			keep[i] = true
		}
	}

	indices := make([]int, 0, len(keep))
	for i := range keep {
		indices = append(indices, i)
	}
	for i := 1; i < len(indices); i++ {
		for j := i; j > 0 && indices[j-1] > indices[j]; j-- {
			indices[j-1], indices[j] = indices[j], indices[j-1]
		}
	}

	marks := make([]string, 0, len(indices)+2)
	newSelected := 0
	for i, idx := range indices {
		if i > 0 && idx-indices[i-1] > 1 {
			marks = append(marks, compactSliderRangeLabel(s.Marks[indices[i-1]+1], s.Marks[idx-1]))
		}
		marks = append(marks, s.Marks[idx])
		if idx == selected {
			newSelected = len(marks) - 1
		}
	}
	return MarkedSlider{Marks: marks, Selected: newSelected}
}

func compactSliderRangeLabel(first string, last string) string {
	if first == last {
		return first
	}
	return fmt.Sprintf("%s-%s", first, last)
}

func renderCheckItem(name string, on bool, focused bool) string {
	marker := " "
	if on {
		marker = "x"
	}
	prefix := " "
	suffix := " "
	if focused {
		prefix = ">"
		suffix = "<"
	}
	return prefix + "[" + marker + "] " + name + suffix
}

func renderActionLabel(label string, focused bool) string {
	if focused {
		return "[> " + label + " <]"
	}
	return "[ " + label + " ]"
}
