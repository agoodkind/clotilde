package ui

import (
	"fmt"
	"strconv"

	"github.com/gdamore/tcell/v2"
)

type CompactRunRequest struct {
	SessionName    string
	TargetTokens   int
	ReservedTokens int
	Model          string
	ModelExplicit  bool
	Thinking       bool
	Images         bool
	Tools          bool
	Chat           bool
	Summarize      bool
	Force          bool
}

type CompactEvent struct {
	Kind          string
	Message       string
	Upfront       *CompactUpfront
	Iteration     *CompactIteration
	Final         *CompactFinal
	ApplyMutation *CompactApplyMutation
}

type CompactUpfront struct {
	SessionName    string
	SessionID      string
	Model          string
	CurrentTotal   int
	MaxTokens      int
	TargetTokens   int
	ReservedTokens int
}

type CompactIteration struct {
	Iteration int
	Step      string
	CtxTotal  int
	Delta     int
}

type CompactFinal struct {
	FinalTail      int
	TargetTokens   int
	StaticFloor    int
	ReservedTokens int
}

type CompactApplyMutation struct {
	BoundaryUUID  string
	SyntheticUUID string
	SnapshotPath  string
	LedgerPath    string
}

type CompactUndoResult struct {
	AppliedAt     string
	BoundaryUUID  string
	SyntheticUUID string
}

type CompactPanel struct {
	Rect Rect

	sessionName string
	sessionID   string
	model       string

	targetTokens int
	maxTokens    int
	reserved     int
	targetText   string

	thinking bool
	images   bool
	tools    bool
	chat     bool

	focusGroup   int
	checkboxIdx  int
	actionIdx    int
	confirmApply bool

	busy       bool
	busyAction string
	status     string

	iterationHistory []CompactIteration
	latestIteration  *CompactIteration
	latestFinal      *CompactFinal
	latestUndo       *CompactUndoResult

	OnPreview func(CompactRunRequest)
	OnApply   func(CompactRunRequest)
	OnUndo    func()
	OnClose   func()
}

func NewCompactPanel(sessionName string) *CompactPanel {
	return &CompactPanel{
		sessionName:  sessionName,
		targetTokens: 200000,
		maxTokens:    1000000,
		reserved:     13000,
		targetText:   "200000",
		thinking:     true,
		images:       true,
		tools:        true,
		chat:         true,
		status:       "adjust controls and run preview",
	}
}

func (p *CompactPanel) ApplyCompactEvent(ev CompactEvent) {
	switch ev.Kind {
	case "upfront":
		if ev.Upfront != nil {
			p.iterationHistory = nil
			p.latestIteration = nil
			p.latestFinal = nil
			p.latestUndo = nil
			p.sessionName = ev.Upfront.SessionName
			p.sessionID = ev.Upfront.SessionID
			p.model = ev.Upfront.Model
			if ev.Upfront.MaxTokens > 0 {
				p.maxTokens = ev.Upfront.MaxTokens
			}
			if ev.Upfront.TargetTokens > 0 {
				p.targetTokens = ev.Upfront.TargetTokens
				p.targetText = strconv.Itoa(ev.Upfront.TargetTokens)
			}
		}
	case "iteration":
		if ev.Iteration != nil {
			p.latestIteration = ev.Iteration
			p.iterationHistory = append(p.iterationHistory, *ev.Iteration)
		}
	case "final":
		p.latestFinal = ev.Final
	case "status":
		if ev.Message != "" {
			p.status = ev.Message
		}
	}
}

func (p *CompactPanel) SetBusy(action string, busy bool) {
	p.busy = busy
	p.busyAction = action
	if busy {
		p.status = action + " in progress..."
	}
}

func (p *CompactPanel) SetUndoResult(res *CompactUndoResult, err error) {
	if err != nil {
		p.status = fmt.Sprintf("undo failed: %v", err)
		return
	}
	p.latestUndo = res
	p.status = "undo completed"
}

func (p *CompactPanel) Draw(scr tcell.Screen, r Rect) {
	p.Rect = r
	dimBackground(scr)
	box := Rect{X: r.X + 2, Y: r.Y + 1, W: r.W - 4, H: r.H - 2}
	clearRect(scr, box)
	drawBoxBorder(scr, box, ColorBorder)

	inner := Rect{X: box.X + 2, Y: box.Y + 1, W: box.W - 4, H: box.H - 2}
	y := inner.Y
	drawString(scr, inner.X, y, StyleHeader, "Compact (Interactive)", inner.W)
	y++
	drawString(scr, inner.X, y, StyleMuted, fmt.Sprintf("session %s  model %s", p.valueOrDash(p.sessionName), p.valueOrDash(p.model)), inner.W)
	y += 2

	drawString(scr, inner.X, y, StyleHeader, "Target", inner.W)
	y++
	drawString(scr, inner.X, y, p.focusStyle(0), "slider "+p.renderSlider(20)+" "+p.percentLabel(), inner.W)
	y++
	drawString(scr, inner.X, y, p.focusStyle(1), "target tokens ["+p.targetText+"]", inner.W)
	y++
	drawString(scr, inner.X, y, p.focusStyle(2), p.renderChecks(), inner.W)
	y += 2

	drawString(scr, inner.X, y, StyleHeader, "Live Status", inner.W)
	y++
	drawString(scr, inner.X, y, StyleMuted, "status: "+p.status, inner.W)
	y++

	actionsY := inner.Y + inner.H - 2
	if actionsY < y+1 {
		actionsY = y + 1
	}
	for _, line := range p.visibleIterationLines(actionsY - y) {
		drawString(scr, inner.X, y, StyleDefault, line, inner.W)
		y++
	}
	if p.latestFinal != nil {
		total := p.latestFinal.StaticFloor + p.latestFinal.ReservedTokens + p.latestFinal.FinalTail
		drawString(scr, inner.X, y, StyleDefault, fmt.Sprintf(
			"final total %s  target %s",
			formatWithCommas(total),
			formatWithCommas(p.latestFinal.TargetTokens),
		), inner.W)
		y++
	}
	if p.latestUndo != nil {
		drawString(scr, inner.X, y, StyleMuted, "last undo at "+p.latestUndo.AppliedAt, inner.W)
		y++
	}

	y = actionsY - 1
	drawString(scr, inner.X, y, StyleHeader, "Actions", inner.W)
	y++
	p.drawActionButtons(scr, inner.X, y, inner.W)
}

func (p *CompactPanel) StatusLegendActions() []LegendAction {
	return []LegendAction{
		LegendFocus,
		LegendAdjust,
		LegendSelect,
		LegendClose,
	}
}

func (p *CompactPanel) HandleEvent(ev tcell.Event) bool {
	switch e := ev.(type) {
	case *tcell.EventMouse:
		x, y := e.Position()
		return p.Rect.Contains(x, y)
	case *tcell.EventKey:
		if e.Key() == tcell.KeyEscape || (e.Key() == tcell.KeyRune && (e.Rune() == 'q' || e.Rune() == 'Q')) {
			if p.OnClose != nil {
				p.OnClose()
			}
			return true
		}
		if p.busy {
			return true
		}
		if e.Key() == tcell.KeyRune {
			switch e.Rune() {
			case 'p', 'P':
				p.triggerAction(0)
				return true
			case 'a', 'A':
				p.focusGroup = 3
				p.actionIdx = 1
				p.armApplyConfirmation()
				return true
			case 'u', 'U':
				p.triggerAction(2)
				return true
			}
		}
		switch e.Key() {
		case tcell.KeyUp:
			p.focusGroup = (p.focusGroup + 3) % 4
			p.clearApplyConfirmation()
			return true
		case tcell.KeyDown:
			p.focusGroup = (p.focusGroup + 1) % 4
			p.clearApplyConfirmation()
			return true
		}
		switch p.focusGroup {
		case 0:
			return p.handleSliderKeys(e)
		case 1:
			return p.handleTargetInputKeys(e)
		case 2:
			return p.handleCheckKeys(e)
		case 3:
			return p.handleActionKeys(e)
		}
	}
	return false
}

func (p *CompactPanel) handleSliderKeys(e *tcell.EventKey) bool {
	p.clearApplyConfirmation()
	switch e.Key() {
	case tcell.KeyLeft:
		p.adjustTargetByPercent(1)
		return true
	case tcell.KeyRight:
		p.adjustTargetByPercent(-1)
		return true
	case tcell.KeyPgUp:
		p.adjustTargetByPercent(5)
		return true
	case tcell.KeyPgDn:
		p.adjustTargetByPercent(-5)
		return true
	case tcell.KeyEnter:
		p.adjustTargetByPercent(1)
		return true
	}
	if e.Key() == tcell.KeyRune && e.Rune() == ' ' {
		p.adjustTargetByPercent(-1)
		return true
	}
	return false
}

func (p *CompactPanel) handleTargetInputKeys(e *tcell.EventKey) bool {
	p.clearApplyConfirmation()
	switch e.Key() {
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(p.targetText) > 0 {
			p.targetText = p.targetText[:len(p.targetText)-1]
		}
		p.updateTargetFromText()
		return true
	case tcell.KeyEnter:
		p.updateTargetFromText()
		return true
	case tcell.KeyRune:
		if e.Rune() >= '0' && e.Rune() <= '9' {
			p.targetText += string(e.Rune())
			p.updateTargetFromText()
			return true
		}
	}
	if e.Key() == tcell.KeyRune && e.Rune() == ' ' {
		p.updateTargetFromText()
		return true
	}
	return false
}

func (p *CompactPanel) handleCheckKeys(e *tcell.EventKey) bool {
	p.clearApplyConfirmation()
	switch e.Key() {
	case tcell.KeyLeft:
		p.checkboxIdx = (p.checkboxIdx + 3) % 4
		return true
	case tcell.KeyRight:
		p.checkboxIdx = (p.checkboxIdx + 1) % 4
		return true
	case tcell.KeyEnter:
		p.toggleCheckbox(p.checkboxIdx)
		return true
	case tcell.KeyRune:
		if e.Rune() == ' ' {
			p.toggleCheckbox(p.checkboxIdx)
			return true
		}
	}
	return false
}

func (p *CompactPanel) handleActionKeys(e *tcell.EventKey) bool {
	switch e.Key() {
	case tcell.KeyLeft:
		p.actionIdx = (p.actionIdx + 3) % 4
		if p.actionIdx != 1 {
			p.clearApplyConfirmation()
		}
		return true
	case tcell.KeyRight:
		p.actionIdx = (p.actionIdx + 1) % 4
		if p.actionIdx != 1 {
			p.clearApplyConfirmation()
		}
		return true
	case tcell.KeyEnter:
		p.triggerAction(p.actionIdx)
		return true
	}
	if e.Key() == tcell.KeyRune && e.Rune() == ' ' {
		p.triggerAction(p.actionIdx)
		return true
	}
	return false
}

func (p *CompactPanel) toggleCheckbox(idx int) {
	switch idx {
	case 0:
		p.thinking = !p.thinking
	case 1:
		p.images = !p.images
	case 2:
		p.tools = !p.tools
	case 3:
		p.chat = !p.chat
	}
}

func (p *CompactPanel) triggerAction(idx int) {
	switch idx {
	case 0:
		p.clearApplyConfirmation()
		if p.OnPreview != nil {
			p.OnPreview(p.buildRequest())
		}
	case 1:
		if !p.confirmApply {
			p.armApplyConfirmation()
			return
		}
		p.clearApplyConfirmation()
		if p.OnApply != nil {
			p.OnApply(p.buildRequest())
		}
	case 2:
		p.clearApplyConfirmation()
		if p.OnUndo != nil {
			p.OnUndo()
		}
	case 3:
		p.clearApplyConfirmation()
		if p.OnClose != nil {
			p.OnClose()
		}
	}
}

func (p *CompactPanel) armApplyConfirmation() {
	p.confirmApply = true
	p.status = "confirm apply: press Enter or Space on [Apply] again to mutate transcript"
}

func (p *CompactPanel) clearApplyConfirmation() {
	p.confirmApply = false
}

func (p *CompactPanel) buildRequest() CompactRunRequest {
	return CompactRunRequest{
		SessionName:    p.sessionName,
		TargetTokens:   p.targetTokens,
		ReservedTokens: p.reserved,
		Thinking:       p.thinking,
		Images:         p.images,
		Tools:          p.tools,
		Chat:           p.chat,
		Summarize:      true,
	}
}

func (p *CompactPanel) focusStyle(group int) tcell.Style {
	if p.focusGroup == group {
		return StyleSelected
	}
	return StyleDefault
}

func (p *CompactPanel) renderChecks() string {
	check := func(name string, on bool, idx int) string {
		marker := " "
		if on {
			marker = "x"
		}
		prefix := " "
		suffix := " "
		if p.focusGroup == 2 && p.checkboxIdx == idx {
			prefix = ">"
			suffix = "<"
		}
		return fmt.Sprintf("%s[%s] %s%s", prefix, marker, name, suffix)
	}
	return check("thinking", p.thinking, 0) + "  " +
		check("images", p.images, 1) + "  " +
		check("tools", p.tools, 2) + "  " +
		check("chat", p.chat, 3)
}

func (p *CompactPanel) renderActions() string {
	labels := []string{"Preview", "Apply", "Undo", "Close"}
	out := ""
	for i, label := range labels {
		if i > 0 {
			out += " "
		}
		left := "["
		right := "]"
		if p.focusGroup == 3 && p.actionIdx == i {
			left = "[>"
			right = "<]"
		}
		if i == 1 && p.confirmApply {
			label = "Apply (confirm)"
		}
		out += left + " " + label + " " + right
	}
	return out
}

func (p *CompactPanel) drawActionButtons(scr tcell.Screen, x, y, maxW int) {
	labels := []string{"Preview", "Apply", "Undo", "Close"}
	cursor := x
	for i, label := range labels {
		if i == 1 && p.confirmApply {
			label = "Apply (confirm)"
		}
		text := "[ " + label + " ]"
		width := cellCount(text)
		if cursor > x {
			if cursor-x+1 >= maxW {
				break
			}
			drawString(scr, cursor, y, StyleDefault, " ", maxW-(cursor-x))
			cursor++
		}
		if cursor-x+width > maxW {
			break
		}
		style := StyleDefault
		if p.focusGroup == 3 && p.actionIdx == i {
			style = StyleSelected
			fillRow(scr, cursor, y, width, style)
		}
		drawString(scr, cursor, y, style, text, maxW-(cursor-x))
		cursor += width
	}
}

func (p *CompactPanel) visibleIterationLines(limit int) []string {
	if limit <= 0 || len(p.iterationHistory) == 0 {
		return nil
	}
	if limit > len(p.iterationHistory) {
		limit = len(p.iterationHistory)
	}
	start := len(p.iterationHistory) - limit
	lines := make([]string, 0, limit)
	for _, iter := range p.iterationHistory[start:] {
		lines = append(lines, fmt.Sprintf(
			"iter %d  %s  total %s  %s",
			iter.Iteration,
			iter.Step,
			formatWithCommas(iter.CtxTotal),
			formatSignedWithCommas(iter.Delta),
		))
	}
	return lines
}

func (p *CompactPanel) renderSlider(width int) string {
	if width < 4 {
		width = 4
	}
	fill := p.compactedPercent() * width / 100
	if fill < 0 {
		fill = 0
	}
	if fill > width {
		fill = width
	}
	out := "|"
	for i := 0; i < width; i++ {
		if i < fill {
			out += "="
			continue
		}
		out += "-"
	}
	return out + "|"
}

func (p *CompactPanel) percent() int {
	if p.maxTokens <= 0 {
		return 0
	}
	return (p.targetTokens * 100) / p.maxTokens
}

func (p *CompactPanel) compactedPercent() int {
	return 100 - p.percent()
}

func (p *CompactPanel) percentLabel() string {
	return fmt.Sprintf(
		"%d%% (%s/%s)",
		p.percent(),
		formatWithCommas(p.targetTokens),
		formatWithCommas(p.maxTokens),
	)
}

func (p *CompactPanel) adjustTargetByPercent(delta int) {
	next := p.percent() + delta
	next = clamp(next, 1, 100)
	p.targetTokens = p.maxTokens * next / 100
	if p.targetTokens < 1 {
		p.targetTokens = 1
	}
	p.targetText = strconv.Itoa(p.targetTokens)
}

func (p *CompactPanel) updateTargetFromText() {
	if p.targetText == "" {
		return
	}
	v, err := strconv.Atoi(p.targetText)
	if err != nil {
		return
	}
	if v < 1 {
		v = 1
	}
	if p.maxTokens > 0 && v > p.maxTokens {
		v = p.maxTokens
	}
	p.targetTokens = v
	p.targetText = strconv.Itoa(v)
}

func (p *CompactPanel) valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func formatWithCommas(value int) string {
	if value == 0 {
		return "0"
	}
	isNegative := value < 0
	if isNegative {
		value = -value
	}
	digits := strconv.Itoa(value)
	formatted := ""
	for i := 0; i < len(digits); i++ {
		if i > 0 && (len(digits)-i)%3 == 0 {
			formatted += ","
		}
		formatted += string(digits[i])
	}
	if isNegative {
		return "-" + formatted
	}
	return formatted
}

func formatSignedWithCommas(value int) string {
	if value > 0 {
		return "+" + formatWithCommas(value)
	}
	return formatWithCommas(value)
}
