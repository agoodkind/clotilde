package compact

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"

	compactengine "goodkind.io/clyde/internal/compact"
)

// Styled output for the compact CLI. The existing plain-text fallback
// stays around for non-TTY sinks (pipes, hook captures, test harnesses);
// the styled variants kick in when the output is an interactive TTY.
// Styles follow Charm's convention: foreground colors, bold for headers,
// faint for secondary info, borders for summary boxes.

// Mode signals whether the run will mutate the transcript. Rendered
// prominently in every panel so destructive vs non-destructive runs
// are obvious at a glance.
type Mode int

const (
	ModePreview Mode = iota
	ModeApply
)

func (m Mode) Label() string {
	if m == ModeApply {
		return "APPLY"
	}
	return "PREVIEW"
}

var (
	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	styleKey   = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Width(14)
	styleVal   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleNum   = lipgloss.NewStyle().Foreground(lipgloss.Color("48")).Bold(true)
	styleMuted = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleBad   = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	styleGood  = lipgloss.NewStyle().Foreground(lipgloss.Color("48")).Bold(true)
	styleWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)

	// Mode ribbons. Preview uses calm cyan; apply uses hot red with a
	// bang so destructive runs scream visually.
	styleRibbonPreview = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("15")).
				Background(lipgloss.Color("39")).
				Padding(0, 1)
	styleRibbonApply = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("15")).
				Background(lipgloss.Color("160")).
				Padding(0, 1)

	stylePreviewBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("39")).
			Padding(0, 1)
	styleApplyBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("160")).
			Padding(0, 1)
	styleMutedBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("244")).
			Padding(0, 1)
)

func ribbon(m Mode) string {
	switch m {
	case ModeApply:
		return styleRibbonApply.Render(" " + m.Label() + " · will mutate transcript ")
	default:
		return styleRibbonPreview.Render(" " + m.Label() + " · no disk writes ")
	}
}

func boxFor(m Mode) lipgloss.Style {
	switch m {
	case ModeApply:
		return styleApplyBox
	default:
		return stylePreviewBox
	}
}

// UpfrontStats captures everything the planner can show instantly,
// before any count_tokens call. All fields are filled from the slice,
// the calibration file, and (when cached) the session context probe.
type UpfrontStats struct {
	SessionName   string
	SessionID     string
	Model         string
	Mode          Mode
	CurrentTotal  int // live /context total when available (cached or fresh)
	MaxTokens     int // 1,000,000 or session-specific
	Target        int
	StaticFloor   int
	Reserved      int
	Thinking      int
	Images        int
	ToolPairs     int
	ChatTurns     int
	BaselineTail  int // zero when not yet counted
	StrippersText string
	TargetDate    string // calibration capture date (empty when not calibrated)
}

// RenderUpfrontPanel draws the phase-1 information box. Every number
// a user could want BEFORE the long calculation is on screen. The box
// border and ribbon both reflect Mode so there is no ambiguity.
func RenderUpfrontPanel(w io.Writer, s UpfrontStats) {
	box := boxFor(s.Mode)

	shrinkBy := s.CurrentTotal - s.Target
	floorSum := s.StaticFloor + s.Reserved
	budget := s.Target - floorSum
	if budget < 0 {
		budget = 0
	}

	rows := []string{
		styleTitle.Render("compact") + "   " + ribbon(s.Mode),
		"",
		kv("session", s.SessionName),
		kv("uuid", styleMuted.Render(shortUUID(s.SessionID))),
		kv("model", s.Model),
	}

	if s.CurrentTotal > 0 {
		pct := ""
		if s.MaxTokens > 0 {
			pct = styleMuted.Render(fmt.Sprintf("   (%d%% of %s, what your chat shows)",
				int(float64(s.CurrentTotal)/float64(s.MaxTokens)*100),
				humanInt(s.MaxTokens)))
		}
		rows = append(rows,
			"",
			kv("current ctx", humanInt(s.CurrentTotal)+pct),
		)
	}

	if s.Target > 0 {
		shrinkNote := ""
		if shrinkBy > 0 {
			shrinkNote = styleMuted.Render(fmt.Sprintf("   → must shrink by %s", humanInt(shrinkBy)))
		}
		rows = append(rows, kv("target", humanInt(s.Target)+shrinkNote))
	}

	rows = append(rows,
		"",
		kv("static floor", humanInt(s.StaticFloor)+styleMuted.Render("   (system + tools + memory + skills)")),
		kv("reserved", humanInt(s.Reserved)+styleMuted.Render("   (autocompact buffer)")),
	)

	if s.TargetDate != "" {
		rows = append(rows, kv("calibrated", styleMuted.Render(s.TargetDate)))
	}

	if s.Target > 0 {
		rows = append(rows,
			"",
			kv("tail budget", humanInt(budget)+styleMuted.Render(fmt.Sprintf("   (target - static - reserved = %s)", humanInt(budget)))),
		)
	}

	rows = append(rows,
		"",
		kv("tail blocks", fmt.Sprintf("thinking %d · images %d · tools %d · chat %d",
			s.Thinking, s.Images, s.ToolPairs, s.ChatTurns)),
	)
	if s.StrippersText != "" {
		rows = append(rows, kv("strippers", s.StrippersText))
	}

	fmt.Fprintln(w, box.Render(strings.Join(rows, "\n")))
}

// progressView renders a rolling one liner during the target loop.
// Spinner frames animate on a time ticker so the UI stays smooth even
// when a single count_tokens call takes 500ms to 2s. Numbers update
// on each iteration. Mode stays visible on every frame so destructive
// runs are always marked.
type progressView struct {
	w         io.Writer
	target    int
	mode      Mode
	isTTY     bool
	startedAt time.Time

	// Shared state written by Update and read by the ticker goroutine.
	// The mutex protects reads and writes. Contention is negligible.
	// The ticker fires every 60ms or so. Update fires on iteration
	// completion.
	mu            sync.Mutex
	iterCount     int
	lastRec       compactengine.IterationRecord
	frame         int
	lastLineCount int

	// Ticker goroutine lifecycle channels.
	stop chan struct{}
	done chan struct{}
}

// spinnerFPS is the animation rate. 16 frames per second keeps the
// braille spinner visibly smooth without burning CPU.
const spinnerFPS = 16

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func newProgressView(w io.Writer, target int, mode Mode, isTTY bool) *progressView {
	p := &progressView{
		w:         w,
		target:    target,
		mode:      mode,
		isTTY:     isTTY,
		startedAt: time.Now(),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	if isTTY {
		go p.animate()
	} else {
		close(p.done)
	}
	return p
}

// animate is the ticker goroutine. It runs at spinnerFPS until stop is
// closed. The goroutine calls draw concurrently with Update. Both
// paths hold the same mutex so the shared frame counter stays
// consistent.
func (p *progressView) animate() {
	interval := time.Second / spinnerFPS
	t := time.NewTicker(interval)
	defer t.Stop()
	defer close(p.done)
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.mu.Lock()
			p.frame++
			p.mu.Unlock()
			p.draw()
		}
	}
}

func (p *progressView) modeLabel() string {
	if p.mode == ModeApply {
		return styleBad.Render("APPLY")
	}
	return styleGood.Render("PREVIEW")
}

// lastLineCount tracks how many lines the previous draw emitted so
// the next draw can rewind the cursor to the top of the dashboard and
// redraw in place.
func (p *progressView) draw() {
	p.mu.Lock()
	iter := p.iterCount
	rec := p.lastRec
	frame := p.frame
	prevLines := p.lastLineCount
	p.mu.Unlock()

	if iter == 0 && rec.Step == "" {
		rec.Step = "warming up"
	}

	spin := spinnerFrames[frame%len(spinnerFrames)]
	elapsed := time.Since(p.startedAt).Round(time.Second)

	step := strings.TrimSpace(rec.Step)
	if len(step) > 44 {
		step = step[:41] + "..."
	}

	ctxStr := humanInt(rec.CtxTotal)
	if rec.CtxTotal == 0 {
		ctxStr = "?"
	}

	lines := []string{
		fmt.Sprintf("%s %s  %s  %s",
			spin,
			p.modeLabel(),
			styleMuted.Render(fmt.Sprintf("iter %d · %s", iter, elapsed)),
			styleMuted.Render("· "+step)),
		fmt.Sprintf("  %s %s → %s  %s",
			styleKey.Render("ctx"),
			styleNum.Render(ctxStr),
			humanInt(p.target),
			deltaText(rec.Delta)),
		renderBreakdownRow("thinking", categoryStateThinking(rec)),
		renderBreakdownRow("images", categoryStateImages(rec)),
		renderBreakdownRow("tools", categoryStateTools(rec)),
		renderBreakdownRow("chat", categoryStateChat(rec)),
	}

	var buf strings.Builder
	// Move cursor up prevLines (if any) and clear each line so we can
	// redraw without leaving stale content.
	if prevLines > 0 {
		fmt.Fprintf(&buf, "\x1b[%dF", prevLines)
	} else {
		buf.WriteString("\r")
	}
	for _, line := range lines {
		buf.WriteString("\x1b[2K") // clear entire line
		buf.WriteString(line)
		buf.WriteString("\n")
	}

	p.mu.Lock()
	p.lastLineCount = len(lines)
	p.mu.Unlock()
	fmt.Fprint(p.w, buf.String())
}

// deltaText formats a ctx-to-target delta as colored "+X over" or
// "-X under" suitable for inline display.
func deltaText(d int) string {
	if d > 0 {
		return styleBad.Render(fmt.Sprintf("+%s over", humanInt(d)))
	}
	return styleGood.Render(fmt.Sprintf("-%s under", humanInt(-d)))
}

// categoryStateThinking / categoryStateImages / categoryStateTools /
// categoryStateChat each render the right-hand cell of a breakdown
// row. They know how to translate an IterationRecord into a one-line
// summary with optional progress bar.
func categoryStateThinking(r compactengine.IterationRecord) string {
	if r.ThinkingDropped {
		return styleGood.Render("dropped")
	}
	return styleMuted.Render("kept")
}

func categoryStateImages(r compactengine.IterationRecord) string {
	if r.ImagesPlaceholder {
		return styleGood.Render("placeholdered")
	}
	return styleMuted.Render("kept")
}

func categoryStateTools(r compactengine.IterationRecord) string {
	total := r.ToolsFull + r.ToolsLineOnly + r.ToolsDropped
	if total == 0 {
		return styleMuted.Render("none")
	}
	// Stacked bar: full = muted, line-only = warn, dropped = bad.
	bar := stackedBar(20, []segment{
		{count: r.ToolsFull, style: styleMuted, char: "█"},
		{count: r.ToolsLineOnly, style: styleWarn, char: "█"},
		{count: r.ToolsDropped, style: styleBad, char: "█"},
	}, total)
	return fmt.Sprintf("%s  %d full · %d line-only · %d dropped",
		bar, r.ToolsFull, r.ToolsLineOnly, r.ToolsDropped)
}

func categoryStateChat(r compactengine.IterationRecord) string {
	if r.ChatTurnsTotal == 0 {
		return styleMuted.Render("none")
	}
	kept := r.ChatTurnsTotal - r.ChatTurnsDropped
	bar := stackedBar(20, []segment{
		{count: kept, style: styleMuted, char: "█"},
		{count: r.ChatTurnsDropped, style: styleBad, char: "█"},
	}, r.ChatTurnsTotal)
	return fmt.Sprintf("%s  %d kept · %d dropped  %s",
		bar, kept, r.ChatTurnsDropped,
		styleMuted.Render(fmt.Sprintf("of %d", r.ChatTurnsTotal)))
}

// renderBreakdownRow formats a label + value row with consistent
// indentation so the live dashboard aligns cleanly.
func renderBreakdownRow(label, value string) string {
	return "  " + styleKey.Render(label) + value
}

// segment is one colored slice of a stacked bar.
type segment struct {
	count int
	style lipgloss.Style
	char  string
}

// stackedBar renders a width-cell bar divided among segments in
// proportion to their count of total. Cell widths round so the total
// never exceeds width. Zero-count segments contribute no cells.
func stackedBar(width int, segs []segment, total int) string {
	if total <= 0 || width <= 0 {
		return strings.Repeat(" ", width)
	}
	var cells []int
	remaining := width
	for i, s := range segs {
		if i == len(segs)-1 {
			cells = append(cells, remaining)
			break
		}
		c := s.count * width / total
		if c > remaining {
			c = remaining
		}
		cells = append(cells, c)
		remaining -= c
	}
	var b strings.Builder
	for i, s := range segs {
		if cells[i] <= 0 {
			continue
		}
		b.WriteString(s.style.Render(strings.Repeat(s.char, cells[i])))
	}
	return b.String()
}

// Update refreshes the iteration numbers. For non TTY sinks, like
// pipes or test harnesses, it also emits one line per iteration so
// the log stays readable without terminal control codes.
func (p *progressView) Update(r compactengine.IterationRecord) {
	p.mu.Lock()
	p.iterCount++
	p.lastRec = r
	iter := p.iterCount
	p.mu.Unlock()

	if p.isTTY {
		// Force an immediate redraw so numbers refresh without waiting
		// for the next ticker beat.
		p.draw()
		return
	}

	// Non TTY fallback. One line per iteration, no ANSI.
	step := strings.TrimSpace(r.Step)
	tag := "OK"
	if r.Delta > 0 {
		tag = fmt.Sprintf("+%d over", r.Delta)
	} else {
		tag = fmt.Sprintf("-%d under", -r.Delta)
	}
	fmt.Fprintf(p.w, "  [%s] iter %02d %-44s ctx=%s %s\n",
		p.mode.Label(), iter, step, humanInt(r.CtxTotal), tag)
}

// Finish stops the ticker and clears the dashboard. The function
// blocks until the ticker goroutine has exited so the next print does
// not race with the ticker.
func (p *progressView) Finish() {
	if !p.isTTY {
		return
	}
	close(p.stop)
	<-p.done

	p.mu.Lock()
	lines := p.lastLineCount
	p.mu.Unlock()

	if lines > 0 {
		// Move up to the top of the dashboard and clear each row so
		// the next render starts with a clean canvas.
		var buf strings.Builder
		fmt.Fprintf(&buf, "\x1b[%dF", lines)
		for i := 0; i < lines; i++ {
			buf.WriteString("\x1b[2K")
			if i < lines-1 {
				buf.WriteString("\n")
			}
		}
		// Return cursor to the top of the cleared area.
		fmt.Fprintf(&buf, "\x1b[%dF", lines-1)
		fmt.Fprint(p.w, buf.String())
	}
}

// LastRecord returns the final iteration's record so callers can
// carry the end-state breakdown into the result box without keeping
// a separate reference.
func (p *progressView) LastRecord() compactengine.IterationRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastRec
}

// finalBreakdownRows renders the same four-category breakdown the
// live dashboard uses, but from the final IterationRecord. Shared by
// preview and apply result boxes so the end-state matches the last
// live frame the user saw.
func finalBreakdownRows(final compactengine.IterationRecord) []string {
	return []string{
		renderBreakdownRow("thinking", categoryStateThinking(final)),
		renderBreakdownRow("images", categoryStateImages(final)),
		renderBreakdownRow("tools", categoryStateTools(final)),
		renderBreakdownRow("chat", categoryStateChat(final)),
	}
}

// finalRecord extracts the last IterationRecord from PlanResult so
// both Render functions can show the same end-state breakdown.
func finalRecord(res *compactengine.PlanResult) compactengine.IterationRecord {
	if len(res.Iterations) == 0 {
		return compactengine.IterationRecord{}
	}
	return res.Iterations[len(res.Iterations)-1]
}

// RenderFinalPreview draws the phase-3 result box for a preview run
// with the full what-was-lopped-off breakdown.
func RenderFinalPreview(w io.Writer, res *compactengine.PlanResult, target, static, reserved int) {
	before := static + res.BaselineTail + reserved
	after := static + res.FinalTail + reserved
	reduction := 0
	if res.BaselineTail > 0 {
		reduction = int(float64(res.BaselineTail-res.FinalTail) / float64(res.BaselineTail) * 100)
	}

	var verdict string
	if res.HitTarget {
		verdict = styleGood.Render("✓ under target")
	} else {
		verdict = styleBad.Render("✗ STILL OVER TARGET")
	}

	rows := []string{
		styleTitle.Render("result") + "   " + ribbon(ModePreview) + "   " + verdict,
		"",
		kv("tail", fmt.Sprintf("%s → %s   %s",
			humanInt(res.BaselineTail),
			humanInt(res.FinalTail),
			styleMuted.Render(fmt.Sprintf("(%d%% reduction)", reduction)))),
		kv("ctx total", fmt.Sprintf("%s → %s", humanInt(before), humanInt(after))),
		kv("target", fmt.Sprintf("%s   %s",
			humanInt(target),
			styleMuted.Render(fmt.Sprintf("(margin %s)", humanInt(target-after))))),
		kv("iterations", fmt.Sprintf("%d", len(res.Iterations))),
		"",
		styleTitle.Render("what was stripped"),
	}
	rows = append(rows, finalBreakdownRows(finalRecord(res))...)
	rows = append(rows,
		"",
		styleMuted.Render("nothing was written. pass --apply to mutate."),
	)
	fmt.Fprintln(w, stylePreviewBox.Render(strings.Join(rows, "\n")))
}

// RenderFinalApply draws the phase-3 result box for an applied run.
// The box is red-bordered and explicit about the transcript mutation.
func RenderFinalApply(w io.Writer, res *compactengine.PlanResult, target, static, reserved int, transcriptPath string) {
	before := static + res.BaselineTail + reserved
	after := static + res.FinalTail + reserved
	reduction := 0
	if res.BaselineTail > 0 {
		reduction = int(float64(res.BaselineTail-res.FinalTail) / float64(res.BaselineTail) * 100)
	}

	verdict := styleWarn.Render("✓ APPLIED · transcript mutated")
	if !res.HitTarget {
		verdict = styleBad.Render("⚠ APPLIED but STILL OVER TARGET")
	}

	rows := []string{
		styleTitle.Render("result") + "   " + ribbon(ModeApply) + "   " + verdict,
		"",
		kv("tail", fmt.Sprintf("%s → %s   %s",
			humanInt(res.BaselineTail),
			humanInt(res.FinalTail),
			styleMuted.Render(fmt.Sprintf("(%d%% reduction)", reduction)))),
		kv("ctx total", fmt.Sprintf("%s → %s", humanInt(before), humanInt(after))),
		kv("target", humanInt(target)),
		kv("transcript", styleMuted.Render(transcriptPath)),
		"",
		styleTitle.Render("what was stripped"),
	}
	rows = append(rows, finalBreakdownRows(finalRecord(res))...)
	rows = append(rows,
		"",
		styleMuted.Render("to revert: clyde compact <session> --undo"),
	)
	fmt.Fprintln(w, styleApplyBox.Render(strings.Join(rows, "\n")))
}

// RenderNoTarget prints a compact summary for the strippers-only path
// that does not iterate against count_tokens.
func RenderNoTarget(w io.Writer, mode Mode, sessName string, s compactengine.Strippers, res *compactengine.PlanResult, boundaryBlocks, postBoundary int) {
	rows := []string{
		styleTitle.Render("plan") + "   " + ribbon(mode) + "   " + styleMuted.Render("(no target; max fidelity drops)"),
		"",
		kv("session", sessName),
		kv("strippers", strippersDescribe(s)),
		kv("synth blocks", fmt.Sprintf("%d", boundaryBlocks)),
		kv("post-boundary", fmt.Sprintf("%d entries", postBoundary)),
	}
	_ = res
	fmt.Fprintln(w, boxFor(mode).Render(strings.Join(rows, "\n")))
}

func kv(label, value string) string {
	return styleKey.Render(label) + styleVal.Render(value)
}
