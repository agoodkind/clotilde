package compact

import (
	"fmt"
	"io"
	"strings"
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

// progressView renders a rolling one-liner during the target loop. The
// spinner frame cycles every call, numbers update with every
// iteration. Mode stays visible on every frame so users can never lose
// track of whether a long-running run is about to mutate.
type progressView struct {
	w          io.Writer
	target     int
	mode       Mode
	isTTY      bool
	iterCount  int
	startedAt  time.Time
	frame      int
	lastRender time.Time
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func newProgressView(w io.Writer, target int, mode Mode, isTTY bool) *progressView {
	return &progressView{
		w:         w,
		target:    target,
		mode:      mode,
		isTTY:     isTTY,
		startedAt: time.Now(),
	}
}

func (p *progressView) modeLabel() string {
	if p.mode == ModeApply {
		return styleBad.Render("APPLY")
	}
	return styleGood.Render("PREVIEW")
}

// Update renders one frame. For TTY: overwrites the current line. For
// non-TTY: emits one line per iteration.
func (p *progressView) Update(r compactengine.IterationRecord) {
	p.iterCount++

	spin := spinnerFrames[p.frame%len(spinnerFrames)]
	p.frame++

	elapsed := time.Since(p.startedAt).Round(time.Second)

	step := strings.TrimSpace(r.Step)
	if len(step) > 40 {
		step = step[:37] + "..."
	}

	budget := p.target
	// Drop tail-only formulation: show current ctx vs target ceiling.
	ctxStr := humanInt(r.CtxTotal)
	targetStr := humanInt(budget)

	modeTag := p.modeLabel()
	body := fmt.Sprintf("%s %s   %s  ctx %s → %s · iter %d · %s",
		spin,
		modeTag,
		styleMuted.Render(step),
		styleNum.Render(ctxStr),
		targetStr,
		p.iterCount,
		styleMuted.Render(elapsed.String()),
	)

	if p.isTTY {
		fmt.Fprint(p.w, "\r\x1b[K"+body)
	} else {
		fmt.Fprintln(p.w, body)
	}
}

func (p *progressView) Finish() {
	if p.isTTY {
		fmt.Fprint(p.w, "\r\x1b[K")
	}
}

// RenderFinalPreview draws the phase-3 result box for a preview run.
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
		styleMuted.Render("nothing was written. pass --apply to mutate."),
	}
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
		styleMuted.Render("to revert: clyde compact <session> --undo"),
	}
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
