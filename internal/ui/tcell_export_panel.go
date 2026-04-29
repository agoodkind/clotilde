package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
)

type ExportPanel struct {
	Rect Rect

	sessionName string
	stats       SessionExportStats

	format SessionExportFormat
	folder string
	name   string

	historyStart int
	whitespace   SessionExportWhitespaceCompression

	includeChat            bool
	includeThinking        bool
	includeSystemPrompts   bool
	includeToolCalls       bool
	includeToolOutputs     bool
	includeRawJSONMetadata bool
	copyToClipboard        bool
	saveToFile             bool
	overwrite              bool

	focusGroup int
	contentIdx int
	destIdx    int
	actionIdx  int

	status string
	log    []string

	OnExport       func(SessionExportRequest)
	OnPreview      func(SessionExportRequest)
	OnChooseFolder func(*ExportPanel)
	OnClose        func()
}

func NewExportPanel(sessionName string, stats SessionExportStats, folder string) *ExportPanel {
	if folder == "" {
		folder = "."
	}
	name := sessionName
	if name == "" {
		name = "session"
	}
	name = defaultExportFilename(name, SessionExportMarkdown)
	p := &ExportPanel{
		sessionName:      sessionName,
		stats:            stats,
		format:           SessionExportMarkdown,
		folder:           folder,
		name:             name,
		historyStart:     defaultExportHistoryStart(stats.Compactions),
		whitespace:       SessionExportWhitespaceTidy,
		includeChat:      true,
		includeThinking:  true,
		includeToolCalls: true,
		copyToClipboard:  true,
		saveToFile:       true,
		status:           "adjust controls and preview export",
	}
	p.updateEstimate()
	return p
}

func defaultExportHistoryStart(compactions int) int {
	if compactions <= 0 {
		return 0
	}
	return compactions - 1
}

func (p *ExportPanel) SetFolder(folder string) {
	if strings.TrimSpace(folder) == "" {
		return
	}
	p.folder = folder
	p.status = "folder selected"
}

func (p *ExportPanel) StartLoadingStats() {
	p.status = "loading export stats..."
	p.log = []string{"output loading..."}
}

func (p *ExportPanel) ApplyStats(stats SessionExportStats) {
	prevCompactions := p.stats.Compactions
	p.stats = stats
	if prevCompactions == 0 && p.historyStart == 0 {
		p.historyStart = defaultExportHistoryStart(stats.Compactions)
	}
	if p.historyStart > p.stats.Compactions {
		p.historyStart = p.stats.Compactions
	}
	if p.status == "" || strings.HasPrefix(p.status, "loading export stats") {
		p.status = "adjust controls and preview export"
	}
	p.updateEstimate()
}

func (p *ExportPanel) Draw(scr tcell.Screen, r Rect) {
	p.Rect = r
	dimBackground(scr)
	box := Rect{X: r.X + 2, Y: r.Y + 1, W: r.W - 4, H: r.H - 2}
	clearRect(scr, box)
	drawBoxBorder(scr, box, ColorBorder)

	inner := Rect{X: box.X + 2, Y: box.Y + 1, W: box.W - 4, H: box.H - 2}
	y := inner.Y
	drawString(scr, inner.X, y, StyleHeader, "Export Transcript", inner.W)
	drawString(scr, inner.X+imax(0, inner.W-runeCount(p.sessionName)-2), y, StyleMuted, p.sessionName, inner.W)
	y += 2

	drawString(scr, inner.X, y, StyleHeader, "Summary", inner.W)
	y++
	drawString(scr, inner.X, y, StyleDefault, fmt.Sprintf("visible tokens %s   visible messages %s   file size %s",
		formatDetailTokens(p.stats.VisibleTokensEstimate),
		formatWithCommas(p.stats.VisibleMessages),
		formatBytes(p.stats.TranscriptSizeBytes)), inner.W)
	y++
	drawString(scr, inner.X, y, StyleDefault, fmt.Sprintf("user msgs %s   assistant msgs %s   tool results %s",
		formatWithCommas(p.stats.UserMessages),
		formatWithCommas(p.stats.AssistantMessages),
		formatWithCommas(p.stats.ToolResultMessages)), inner.W)
	y++
	drawString(scr, inner.X, y, StyleDefault, fmt.Sprintf("tool calls %s   system prompts %s   compactions %s",
		formatWithCommas(p.stats.ToolCalls),
		formatWithCommas(p.stats.SystemPrompts),
		formatWithCommas(p.stats.Compactions)), inner.W)
	y += 2

	drawString(scr, inner.X, y, StyleHeader, "History", inner.W)
	y++
	historyLabel := "include from   "
	drawString(scr, inner.X, y, p.focusStyle(0), historyLabel+p.historySliderForWidth(inner.W-runeCount(historyLabel)), inner.W)
	y++
	drawString(scr, inner.X, y, StyleMuted, "included       "+p.historyIncludedLabel(), inner.W)
	y++
	drawString(scr, inner.X, y, StyleMuted, "estimate       "+p.estimateLabel(), inner.W)
	y += 2

	drawString(scr, inner.X, y, StyleHeader, "Content", inner.W)
	y++
	drawString(scr, inner.X, y, p.focusStyle(1), p.renderContentChecks(), inner.W)
	y++
	drawString(scr, inner.X, y, p.focusStyle(2), p.renderMoreContentChecks(), inner.W)
	y += 2

	drawString(scr, inner.X, y, StyleHeader, "Compression", inner.W)
	y++
	drawString(scr, inner.X, y, p.focusStyle(3), "whitespace  [ "+p.whitespaceLabel()+" ]", inner.W)
	y++
	descPrefix := "description    "
	descIndent := strings.Repeat(" ", runeCount(descPrefix))
	for i, line := range wrapText(p.whitespaceDescription(), imax(20, inner.W-runeCount(descPrefix))) {
		prefix := descPrefix
		if i > 0 {
			prefix = descIndent
		}
		drawString(scr, inner.X, y, StyleMuted, prefix+line, inner.W)
		y++
	}
	y += 2

	drawString(scr, inner.X, y, StyleHeader, "Destination", inner.W)
	y++
	drawString(scr, inner.X, y, p.focusStyle(4), p.renderDestinationChecks(), inner.W)
	y++
	drawString(scr, inner.X, y, p.focusStyle(5), "folder  [ Choose folder... ]  "+p.folder, inner.W)
	y++
	drawString(scr, inner.X, y, p.focusStyle(6), "name    ["+p.name+"]", inner.W)
	y += 2

	drawString(scr, inner.X, y, StyleHeader, "Live Status", inner.W)
	y++
	drawString(scr, inner.X, y, StyleMuted, "status: "+p.status, inner.W)
	y++
	for _, line := range p.log {
		if y >= inner.Y+inner.H-3 {
			break
		}
		drawString(scr, inner.X, y, StyleDefault, line, inner.W)
		y++
	}

	actionsY := inner.Y + inner.H - 2
	drawString(scr, inner.X, actionsY-1, StyleHeader, "Actions", inner.W)
	drawString(scr, inner.X, actionsY, p.focusStyle(7), p.renderActions(), inner.W)
}

func (p *ExportPanel) StatusLegendActions() []LegendAction {
	return []LegendAction{LegendFocus, LegendAdjust, LegendSelect, LegendClose}
}

func (p *ExportPanel) HandleEvent(ev tcell.Event) bool {
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
		switch e.Key() {
		case tcell.KeyUp:
			p.focusGroup = (p.focusGroup + 7) % 8
			return true
		case tcell.KeyDown:
			p.focusGroup = (p.focusGroup + 1) % 8
			return true
		}
		switch p.focusGroup {
		case 0:
			return p.handleHistoryKeys(e)
		case 1, 2:
			return p.handleContentKeys(e)
		case 3:
			return p.handleWhitespaceKeys(e)
		case 4:
			return p.handleDestinationKeys(e)
		case 5:
			if e.Key() == tcell.KeyEnter || (e.Key() == tcell.KeyRune && e.Rune() == ' ') {
				if p.OnChooseFolder != nil {
					p.OnChooseFolder(p)
				}
				return true
			}
		case 6:
			return p.handleNameKeys(e)
		case 7:
			return p.handleActionKeys(e)
		}
	}
	return false
}

func (p *ExportPanel) handleHistoryKeys(e *tcell.EventKey) bool {
	switch e.Key() {
	case tcell.KeyLeft:
		p.historyStart = clamp(p.historyStart-1, 0, p.stats.Compactions)
		p.updateEstimate()
		return true
	case tcell.KeyRight:
		p.historyStart = clamp(p.historyStart+1, 0, p.stats.Compactions)
		p.updateEstimate()
		return true
	case tcell.KeyHome:
		p.historyStart = 0
		p.updateEstimate()
		return true
	case tcell.KeyEnd:
		p.historyStart = p.stats.Compactions
		p.updateEstimate()
		return true
	}
	return false
}

func (p *ExportPanel) handleContentKeys(e *tcell.EventKey) bool {
	maxIdx := 2
	switch e.Key() {
	case tcell.KeyLeft:
		if p.focusGroup == 1 {
			p.contentIdx = (p.contentIdx + 2) % 3
		} else {
			p.contentIdx = (p.contentIdx + 2) % 3
		}
		return true
	case tcell.KeyRight:
		p.contentIdx = (p.contentIdx + 1) % (maxIdx + 1)
		return true
	case tcell.KeyEnter:
		p.toggleContent()
		return true
	case tcell.KeyRune:
		if e.Rune() == ' ' {
			p.toggleContent()
			return true
		}
	}
	return false
}

func (p *ExportPanel) handleWhitespaceKeys(e *tcell.EventKey) bool {
	switch e.Key() {
	case tcell.KeyLeft:
		p.cycleWhitespace(-1)
		return true
	case tcell.KeyRight, tcell.KeyEnter:
		p.cycleWhitespace(1)
		return true
	case tcell.KeyRune:
		if e.Rune() == ' ' {
			p.cycleWhitespace(1)
			return true
		}
	}
	return false
}

func (p *ExportPanel) handleDestinationKeys(e *tcell.EventKey) bool {
	switch e.Key() {
	case tcell.KeyLeft:
		p.destIdx = (p.destIdx + 2) % 3
		return true
	case tcell.KeyRight:
		p.destIdx = (p.destIdx + 1) % 3
		return true
	case tcell.KeyEnter:
		p.toggleDestination()
		return true
	case tcell.KeyRune:
		if e.Rune() == ' ' {
			p.toggleDestination()
			return true
		}
	}
	return false
}

func (p *ExportPanel) handleNameKeys(e *tcell.EventKey) bool {
	switch e.Key() {
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(p.name) > 0 {
			p.name = p.name[:len(p.name)-1]
		}
		return true
	case tcell.KeyRune:
		r := e.Rune()
		if r >= 32 && r != '/' {
			p.name += string(r)
			return true
		}
	}
	return false
}

func (p *ExportPanel) handleActionKeys(e *tcell.EventKey) bool {
	switch e.Key() {
	case tcell.KeyLeft:
		p.actionIdx = (p.actionIdx + 3) % 4
		return true
	case tcell.KeyRight:
		p.actionIdx = (p.actionIdx + 1) % 4
		return true
	case tcell.KeyEnter:
		p.triggerAction()
		return true
	case tcell.KeyRune:
		if e.Rune() == ' ' {
			p.triggerAction()
			return true
		}
	}
	return false
}

func (p *ExportPanel) toggleContent() {
	if p.focusGroup == 1 {
		switch p.contentIdx {
		case 0:
			p.includeChat = !p.includeChat
		case 1:
			p.includeThinking = !p.includeThinking
		case 2:
			p.includeToolCalls = !p.includeToolCalls
		}
	} else {
		switch p.contentIdx {
		case 0:
			p.includeSystemPrompts = !p.includeSystemPrompts
		case 1:
			p.includeToolOutputs = !p.includeToolOutputs
		case 2:
			p.includeRawJSONMetadata = !p.includeRawJSONMetadata
		}
	}
	p.updateEstimate()
}

func (p *ExportPanel) toggleDestination() {
	switch p.destIdx {
	case 0:
		p.copyToClipboard = !p.copyToClipboard
	case 1:
		p.saveToFile = !p.saveToFile
	case 2:
		p.overwrite = !p.overwrite
	}
}

func (p *ExportPanel) triggerAction() {
	switch p.actionIdx {
	case 0:
		p.status = "preview ready"
		p.updateEstimate()
		if p.OnPreview != nil {
			p.OnPreview(p.buildRequest())
		}
	case 1:
		if p.OnExport != nil {
			p.OnExport(p.buildRequest())
		}
	case 2:
		reset := NewExportPanel(p.sessionName, p.stats, p.folder)
		p.format = reset.format
		p.name = reset.name
		p.historyStart = reset.historyStart
		p.whitespace = reset.whitespace
		p.includeChat = reset.includeChat
		p.includeThinking = reset.includeThinking
		p.includeSystemPrompts = reset.includeSystemPrompts
		p.includeToolCalls = reset.includeToolCalls
		p.includeToolOutputs = reset.includeToolOutputs
		p.includeRawJSONMetadata = reset.includeRawJSONMetadata
		p.copyToClipboard = reset.copyToClipboard
		p.saveToFile = reset.saveToFile
		p.overwrite = reset.overwrite
		p.status = "reset export controls"
	case 3:
		if p.OnClose != nil {
			p.OnClose()
		}
	}
}

func (p *ExportPanel) buildRequest() SessionExportRequest {
	return SessionExportRequest{
		SessionName:            p.sessionName,
		Format:                 p.format,
		HistoryStart:           p.historyStart,
		WhitespaceCompression:  p.whitespace,
		IncludeChat:            p.includeChat,
		IncludeThinking:        p.includeThinking,
		IncludeSystemPrompts:   p.includeSystemPrompts,
		IncludeToolCalls:       p.includeToolCalls,
		IncludeToolOutputs:     p.includeToolOutputs,
		IncludeRawJSONMetadata: p.includeRawJSONMetadata,
		CopyToClipboard:        p.copyToClipboard,
		SaveToFile:             p.saveToFile,
		Directory:              p.folder,
		Filename:               p.name,
		Overwrite:              p.overwrite,
	}
}

func (p *ExportPanel) historySlider() string {
	return p.historyMarkedSlider().Render()
}

func (p *ExportPanel) historySliderForWidth(width int) string {
	return p.historyMarkedSlider().RenderForWidth(width)
}

func (p *ExportPanel) historyMarkedSlider() MarkedSlider {
	marks := make([]string, 0, p.stats.Compactions+1)
	for i := 1; i <= p.stats.Compactions; i++ {
		marks = append(marks, fmt.Sprintf("C%d", i))
	}
	marks = append(marks, "VISIBLE")
	return MarkedSlider{Marks: marks, Selected: p.historyStart}
}

func (p *ExportPanel) historyIncludedLabel() string {
	if p.stats.Compactions <= 0 || p.historyStart >= p.stats.Compactions {
		return "visible transcript only"
	}
	count := p.stats.Compactions - p.historyStart
	if count == 1 {
		return fmt.Sprintf("C%d + visible transcript", p.stats.Compactions)
	}
	return fmt.Sprintf("C%d-C%d + visible transcript", p.historyStart+1, p.stats.Compactions)
}

func (p *ExportPanel) cycleWhitespace(delta int) {
	modes := exportWhitespaceModes()
	idx := 0
	for i, mode := range modes {
		if mode == p.whitespace {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(modes)) % len(modes)
	p.whitespace = modes[idx]
	p.status = "whitespace compression: " + p.whitespaceLabel()
}

func exportWhitespaceModes() []SessionExportWhitespaceCompression {
	return []SessionExportWhitespaceCompression{
		SessionExportWhitespacePreserve,
		SessionExportWhitespaceTidy,
		SessionExportWhitespaceCompact,
		SessionExportWhitespaceDense,
	}
}

func (p *ExportPanel) whitespaceLabel() string {
	switch p.whitespace {
	case SessionExportWhitespacePreserve:
		return "preserve"
	case SessionExportWhitespaceCompact:
		return "compact"
	case SessionExportWhitespaceDense:
		return "dense"
	default:
		return "tidy"
	}
}

func (p *ExportPanel) whitespaceDescription() string {
	return exportWhitespaceDescription(p.whitespace)
}

func exportWhitespaceDescription(mode SessionExportWhitespaceCompression) string {
	switch mode {
	case SessionExportWhitespacePreserve:
		return "Keep rendered export spacing as-is."
	case SessionExportWhitespaceCompact:
		return "Apply tidy cleanup, then make the transcript more paste-friendly by tightening spacing between conversation turns while preserving lists, headings, tables, quoted text, and code blocks."
	case SessionExportWhitespaceDense:
		return "Remove blank lines where possible for the smallest readable export while preserving indentation-sensitive code, lists, tables, quoted text, and markdown structure."
	default:
		return "Trim leading/trailing whitespace, collapse extra spaces in prose, and reduce multiple blank lines to one blank line."
	}
}

func (p *ExportPanel) estimateLabel() string {
	tokens := p.estimatedTokens()
	msgs := p.stats.VisibleMessages + p.selectedCompactions()*98
	return fmt.Sprintf("%s   %s messages   %s compaction snapshot(s)",
		formatDetailTokens(tokens),
		formatWithCommas(msgs),
		formatWithCommas(p.selectedCompactions()))
}

func (p *ExportPanel) estimatedTokens() int {
	tokens := 0
	if p.includeChat {
		tokens += p.stats.VisibleTokensEstimate
	}
	if p.includeThinking {
		tokens += p.stats.VisibleTokensEstimate / 8
	}
	if p.includeToolCalls {
		tokens += p.stats.ToolCalls * 90
	}
	if p.includeToolOutputs {
		tokens += p.stats.ToolCalls * 350
	}
	if p.includeSystemPrompts {
		tokens += p.stats.SystemPrompts * 180
	}
	if p.includeRawJSONMetadata {
		tokens += p.stats.VisibleMessages * 12
	}
	tokens += p.selectedCompactions() * imax(1000, p.stats.VisibleTokensEstimate/5)
	return tokens
}

func (p *ExportPanel) selectedCompactions() int {
	if p.stats.Compactions <= 0 || p.historyStart >= p.stats.Compactions {
		return 0
	}
	return p.stats.Compactions - p.historyStart
}

func (p *ExportPanel) updateEstimate() {
	p.log = []string{"output " + p.estimateLabel()}
}

func (p *ExportPanel) renderContentChecks() string {
	return renderCheckItem("chat", p.includeChat, p.focusGroup == 1 && p.contentIdx == 0) + "  " +
		renderCheckItem("thinking", p.includeThinking, p.focusGroup == 1 && p.contentIdx == 1) + "  " +
		renderCheckItem("tool calls", p.includeToolCalls, p.focusGroup == 1 && p.contentIdx == 2)
}

func (p *ExportPanel) renderMoreContentChecks() string {
	return renderCheckItem("system", p.includeSystemPrompts, p.focusGroup == 2 && p.contentIdx == 0) + "  " +
		renderCheckItem("tool outputs", p.includeToolOutputs, p.focusGroup == 2 && p.contentIdx == 1) + "  " +
		renderCheckItem("raw json", p.includeRawJSONMetadata, p.focusGroup == 2 && p.contentIdx == 2)
}

func (p *ExportPanel) renderDestinationChecks() string {
	return renderCheckItem("copy to clipboard", p.copyToClipboard, p.focusGroup == 4 && p.destIdx == 0) + "  " +
		renderCheckItem("save file", p.saveToFile, p.focusGroup == 4 && p.destIdx == 1) + "  " +
		renderCheckItem("overwrite", p.overwrite, p.focusGroup == 4 && p.destIdx == 2)
}

func (p *ExportPanel) renderActions() string {
	labels := []string{"Preview", "Export", "Reset", "Close"}
	parts := make([]string, 0, len(labels))
	for i, label := range labels {
		parts = append(parts, renderActionLabel(label, p.focusGroup == 7 && p.actionIdx == i))
	}
	return strings.Join(parts, " ")
}

func (p *ExportPanel) focusStyle(group int) tcell.Style {
	if p.focusGroup == group {
		return StyleSelected
	}
	return StyleDefault
}

func exportOutputPath(req SessionExportRequest) string {
	name := strings.TrimSpace(req.Filename)
	if name == "" {
		name = "session." + exportFormatExt(req.Format)
	}
	if filepath.Ext(name) == "" {
		name += "." + exportFormatExt(req.Format)
	}
	return filepath.Join(req.Directory, name)
}

func defaultExportFilename(sessionName string, format SessionExportFormat) string {
	base := strings.TrimSpace(sessionName)
	if base == "" {
		base = "session"
	}
	base = sanitizeExportFilenamePart(base)
	date := time.Now().Format("2006-01-02")
	return date + "-" + base + "." + exportFormatExt(format)
}

func sanitizeExportFilenamePart(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "session"
	}
	return out
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if runeCount(line)+1+runeCount(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	lines = append(lines, line)
	return lines
}

func formatBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div := int64(unit)
	exp := 0
	for value := n / unit; value >= unit && exp < 4; value /= unit {
		div *= unit
		exp++
	}
	suffixes := []string{"KB", "MB", "GB", "TB", "PB"}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), suffixes[exp])
}
