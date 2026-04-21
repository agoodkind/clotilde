package compact

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"
)

// Strippers selects which categories the user wants to act on. The
// orchestrator demotes/strips in a fixed order: thinking, images,
// tools (full -> line-only -> drop), then chat.
type Strippers struct {
	Thinking bool
	Images   bool
	Tools    bool
	Chat     bool
}

// Any reports whether any stripper bit is set.
func (s Strippers) Any() bool {
	return s.Thinking || s.Images || s.Tools || s.Chat
}

// SetAll turns every bit on.
func (s *Strippers) SetAll() {
	s.Thinking = true
	s.Images = true
	s.Tools = true
	s.Chat = true
}

// Counter is the narrow interface the target loop needs. It is
// satisfied by the concrete *TokenCounter in this package, and by any
// sessionctx.Layer-backed adapter so callers can route every token
// question through the unified context layer without the planner
// needing to know about it.
type Counter interface {
	CountSyntheticUser(ctx context.Context, contentArray []OutputBlock) (int, error)
}

// PlanInput is the orchestrator's input bundle.
type PlanInput struct {
	Slice          *Slice
	Strippers      Strippers
	Target         int           // /context total ceiling, 0 = no target
	StaticOverhead int           // calibrated overhead, ignored when Target == 0
	Reserved       int           // reserved buffer (default 13_000)
	Counter        Counter       // required when Target > 0
	Out            io.Writer     // fallback streaming sink when OnIteration nil
	OnIteration    func(IterationRecord) // preferred: called after each measure
	BatchSize      int           // tool demotion batch size; default 8
	ChatBatchSize  int           // chat-drop batch size; default 4
	StopTimeout    time.Duration // max wall time for whole loop; 0 = no limit
}

// PlanResult holds the final synthesis options plus the iteration log
// and the last measured tail token count. Apply consumes this.
type PlanResult struct {
	Options      SynthOptions
	BaselineTail int
	FinalTail    int
	Iterations   []IterationRecord
	HitTarget    bool
	BoundaryTail []OutputBlock // the synthesized content array
}

// IterationRecord is one row of the iteration log printed in the
// preview output. It also carries the current category-breakdown
// counts so the CLI can render a live dashboard showing exactly what
// has been stripped as the loop progresses.
type IterationRecord struct {
	Step       string // human-readable description
	TailTokens int    // count_tokens result after this step
	CtxTotal   int    // static + tail + reserved
	Delta      int    // ctx - target (negative = OK)

	// ThinkingDropped is true once DropThinking is on.
	ThinkingDropped bool
	// ImagesPlaceholder is true once ImagesAsPlaceholder is on.
	ImagesPlaceholder bool

	// Tool pair counts by current fidelity level. Sum equals the
	// total number of tool pairs in PostBoundary.
	ToolsFull     int
	ToolsLineOnly int
	ToolsDropped  int

	// Chat turn counts. Total is the count of user/assistant turns
	// in PostBoundary. Dropped grows as the planner drops oldest
	// turns; Refine may un-drop some at the tail of the run.
	ChatTurnsTotal   int
	ChatTurnsDropped int
}

// RunPlan drives the target loop. When Target == 0 it just synthesizes
// once with the requested strippers and returns. When Target > 0 it
// iterates, calling count_tokens after each demotion batch, and stops
// the moment static + tail + reserved <= target.
func RunPlan(ctx context.Context, in PlanInput) (*PlanResult, error) {
	if in.Slice == nil {
		return nil, fmt.Errorf("plan: nil slice")
	}
	if in.BatchSize <= 0 {
		in.BatchSize = 32
	}
	if in.ChatBatchSize <= 0 {
		in.ChatBatchSize = 64
	}
	if in.Target > 0 && in.Counter == nil {
		return nil, fmt.Errorf("plan: target set but no token counter")
	}

	opts := SynthOptions{
		ToolDefault:        ToolDetailFull,
		ToolDetailOverride: map[string]ToolDetail{},
		DroppedChatEntries: map[int]bool{},
	}
	// Strippers without a target: apply the full effect of each flag
	// and return without any count_tokens iterations.
	if in.Target == 0 {
		applyStrippersFully(in.Slice, in.Strippers, &opts)
		result := &PlanResult{
			Options:      opts,
			BoundaryTail: Synthesize(in.Slice, opts),
		}
		return result, nil
	}

	// Target loop.
	if in.StopTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, in.StopTimeout)
		defer cancel()
	}

	// Precompute totals that do not change across iterations. The
	// breakdown in each IterationRecord references these so the CLI
	// dashboard can render progress bars.
	totalToolPairs := len(in.Slice.PairIndex)
	totalChatTurns := 0
	for _, e := range in.Slice.PostBoundary {
		if e.Type == "user" || e.Type == "assistant" {
			totalChatTurns++
		}
	}

	measure := func(label string, prevCtx int) (IterationRecord, error) {
		array := Synthesize(in.Slice, opts)
		tail, err := in.Counter.CountSyntheticUser(ctx, array)
		if err != nil {
			return IterationRecord{}, fmt.Errorf("count_tokens after %q: %w", label, err)
		}
		ctxTotal := in.StaticOverhead + tail + in.Reserved

		// Compute current fidelity breakdown from opts. ToolsFull is
		// the implicit residual after counting overrides so the three
		// tool counts always sum to totalToolPairs.
		toolsLineOnly, toolsDropped := 0, 0
		for _, lvl := range opts.ToolDetailOverride {
			switch lvl {
			case ToolDetailLineOnly:
				toolsLineOnly++
			case ToolDetailDrop:
				toolsDropped++
			}
		}
		toolsFull := totalToolPairs - toolsLineOnly - toolsDropped
		chatDropped := len(opts.DroppedChatEntries)

		record := IterationRecord{
			Step:              label,
			TailTokens:        tail,
			CtxTotal:          ctxTotal,
			Delta:             ctxTotal - in.Target,
			ThinkingDropped:   opts.DropThinking,
			ImagesPlaceholder: opts.ImagesAsPlaceholder,
			ToolsFull:         toolsFull,
			ToolsLineOnly:     toolsLineOnly,
			ToolsDropped:      toolsDropped,
			ChatTurnsTotal:    totalChatTurns,
			ChatTurnsDropped:  chatDropped,
		}
		if in.OnIteration != nil {
			in.OnIteration(record)
		} else if in.Out != nil {
			tag := "OK"
			if record.Delta > 0 {
				tag = fmt.Sprintf("+%d over", record.Delta)
			} else {
				tag = fmt.Sprintf("-%d under", -record.Delta)
			}
			fmt.Fprintf(in.Out, "  iter  %-44s tail=%d  ctx=%d  %s\n", label, tail, ctxTotal, tag)
		}
		return record, nil
	}

	var log []IterationRecord
	rec, err := measure("baseline (no transforms)", 0)
	if err != nil {
		return nil, err
	}
	tail, ctxTotal := rec.TailTokens, rec.CtxTotal
	log = append(log, rec)
	baseline := tail

	if ctxTotal <= in.Target {
		return &PlanResult{
			Options:      opts,
			BaselineTail: baseline,
			FinalTail:    tail,
			Iterations:   log,
			HitTarget:    true,
			BoundaryTail: Synthesize(in.Slice, opts),
		}, nil
	}

	// Step 1: thinking is dropped unconditionally for target loops.
	// It carries no signal across turn boundaries in the live API.
	if !opts.DropThinking {
		opts.DropThinking = true
		rec, err = measure("drop thinking", ctxTotal)
		if err != nil {
			return nil, err
		}
		tail, ctxTotal = rec.TailTokens, rec.CtxTotal
		log = append(log, rec)
		if ctxTotal <= in.Target {
			return finalize(in, opts, baseline, tail, log, true), nil
		}
	}

	// Step 2: images.
	if (in.Strippers.Images || in.Strippers.Any() && allImpliedByTarget(in.Strippers)) && !opts.ImagesAsPlaceholder {
		opts.ImagesAsPlaceholder = true
		rec, err = measure("replace images with placeholders", ctxTotal)
		if err != nil {
			return nil, err
		}
		tail, ctxTotal = rec.TailTokens, rec.CtxTotal
		log = append(log, rec)
		if ctxTotal <= in.Target {
			return finalize(in, opts, baseline, tail, log, true), nil
		}
	}

	// Steps 3 and 4: tools (full -> line-only, then line-only -> drop).
	if in.Strippers.Tools {
		toolIDs := orderedToolUseIDs(in.Slice)
		// Demote oldest first to line-only.
		i := 0
		for i < len(toolIDs) && ctxTotal > in.Target {
			batchEnd := i + in.BatchSize
			if batchEnd > len(toolIDs) {
				batchEnd = len(toolIDs)
			}
			for _, id := range toolIDs[i:batchEnd] {
				opts.ToolDetailOverride[id] = ToolDetailLineOnly
			}
			label := fmt.Sprintf("tools full -> line-only (oldest %d)", batchEnd-i)
			rec, err = measure(label, ctxTotal)
			if err != nil {
				return nil, err
			}
			tail, ctxTotal = rec.TailTokens, rec.CtxTotal
			log = append(log, rec)
			i = batchEnd
		}
		if ctxTotal <= in.Target {
			return finalize(in, opts, baseline, tail, log, true), nil
		}
		// Demote oldest first to drop.
		i = 0
		for i < len(toolIDs) && ctxTotal > in.Target {
			batchEnd := i + in.BatchSize
			if batchEnd > len(toolIDs) {
				batchEnd = len(toolIDs)
			}
			for _, id := range toolIDs[i:batchEnd] {
				opts.ToolDetailOverride[id] = ToolDetailDrop
			}
			label := fmt.Sprintf("tools line-only -> drop (oldest %d)", batchEnd-i)
			rec, err = measure(label, ctxTotal)
			if err != nil {
				return nil, err
			}
			tail, ctxTotal = rec.TailTokens, rec.CtxTotal
			log = append(log, rec)
			i = batchEnd
		}
		if ctxTotal <= in.Target {
			return finalize(in, opts, baseline, tail, log, true), nil
		}
	}

	// Step 5: chat. Drop oldest text turns while preserving the most
	// recent assistant + its preceding user. Uses an adaptive batch
	// size: starts at ChatBatchSize, then shrinks as we approach the
	// target so we land near target rather than punching through.
	// After the main loop, a refine phase un-drops turns one at a time
	// until adding one more would exceed the target.
	if in.Strippers.Chat {
		dropOrder := chatDropOrder(in.Slice)
		i := 0
		prevCtx := ctxTotal
		droppedIdxHistory := []int{} // track indices so refine can revert
		for i < len(dropOrder) && ctxTotal > in.Target {
			deltaOver := ctxTotal - in.Target
			batchSize := in.ChatBatchSize
			// Estimate tokens per turn from the last observed drop and
			// size the batch so it should land within one batch of the
			// target. Bias slightly high so we still hit or cross it.
			if len(log) > 0 {
				lastDrop := prevCtx - ctxTotal
				lastBatch := in.ChatBatchSize
				if lastDrop > 0 {
					tokensPerTurn := lastDrop / maxInt(lastBatch, 1)
					if tokensPerTurn > 0 {
						estNeeded := (deltaOver / tokensPerTurn) + 1
						if estNeeded < batchSize {
							batchSize = maxInt(estNeeded, 1)
						}
					}
				}
			}
			batchEnd := i + batchSize
			if batchEnd > len(dropOrder) {
				batchEnd = len(dropOrder)
			}
			for _, ei := range dropOrder[i:batchEnd] {
				opts.DroppedChatEntries[ei] = true
				droppedIdxHistory = append(droppedIdxHistory, ei)
			}
			label := fmt.Sprintf("drop oldest chat turns (%d)", batchEnd-i)
			prevCtx = ctxTotal
			rec, err = measure(label, ctxTotal)
			if err != nil {
				return nil, err
			}
			tail, ctxTotal = rec.TailTokens, rec.CtxTotal
			log = append(log, rec)
			i = batchEnd
		}

		// Refine: if we crossed under target, walk the last batch back
		// one turn at a time until re-adding one more would push us
		// over. Lands close to target without undershooting wildly.
		if ctxTotal <= in.Target && len(droppedIdxHistory) > 1 {
			for k := len(droppedIdxHistory) - 1; k >= 0; k-- {
				candidate := droppedIdxHistory[k]
				delete(opts.DroppedChatEntries, candidate)
				label := "refine: restore newest dropped turn"
				testRec, measureErr := measure(label, ctxTotal)
				if measureErr != nil {
					// Put it back; abort refine.
					opts.DroppedChatEntries[candidate] = true
					break
				}
				if testRec.CtxTotal > in.Target {
					// Restoring this turn would push us over. Put it
					// back and stop.
					opts.DroppedChatEntries[candidate] = true
					break
				}
				// Keep the restore and continue.
				tail, ctxTotal = testRec.TailTokens, testRec.CtxTotal
				log = append(log, testRec)
			}
		}
	}

	hit := ctxTotal <= in.Target
	return finalize(in, opts, baseline, tail, log, hit), nil
}

// maxInt is a tiny helper to avoid importing golang.org/x/exp or
// introducing a generics constraint just for this file.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// applyStrippersFully sets opts to the most aggressive variant of each
// requested stripper without iterating against count_tokens.
func applyStrippersFully(slice *Slice, s Strippers, opts *SynthOptions) {
	if s.Thinking {
		opts.DropThinking = true
	}
	if s.Images {
		opts.ImagesAsPlaceholder = true
	}
	if s.Tools {
		for id := range slice.PairIndex {
			opts.ToolDetailOverride[id] = ToolDetailDrop
		}
	}
	if s.Chat {
		for _, ei := range chatDropOrder(slice) {
			opts.DroppedChatEntries[ei] = true
		}
	}
}

// allImpliedByTarget reports whether the orchestrator should treat
// Images as part of a target-driven sweep even when --images was not
// passed explicitly. True when --all was effectively set (every flag
// on). Conservative: only sweep images when the user opted in.
func allImpliedByTarget(s Strippers) bool {
	return s.Thinking && s.Images && s.Tools && s.Chat
}

// orderedToolUseIDs returns tool_use ids in entry-order, oldest
// first. Used by the demotion loop so older noise vanishes before
// newer context.
func orderedToolUseIDs(slice *Slice) []string {
	type idx struct {
		id      string
		entryIx int
		blockIx int
	}
	var rows []idx
	for ei, e := range slice.PostBoundary {
		for bi, b := range e.Content {
			if b.Type == "tool_use" && b.ToolUseID != "" {
				rows = append(rows, idx{id: b.ToolUseID, entryIx: ei, blockIx: bi})
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].entryIx != rows[j].entryIx {
			return rows[i].entryIx < rows[j].entryIx
		}
		return rows[i].blockIx < rows[j].blockIx
	})
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.id
	}
	return out
}

// chatDropOrder returns post-boundary entry indexes for chat turns in
// drop-priority order: oldest first, but the most recent assistant
// turn AND its immediate preceding user turn are excluded so the
// model always sees the latest exchange verbatim.
func chatDropOrder(slice *Slice) []int {
	chatEntries := []int{}
	lastAssistant := -1
	for ei, e := range slice.PostBoundary {
		if e.Type == "user" && !isToolResultOnly(e) {
			chatEntries = append(chatEntries, ei)
		}
		if e.Type == "assistant" {
			chatEntries = append(chatEntries, ei)
			lastAssistant = ei
		}
	}
	preserve := map[int]bool{}
	if lastAssistant >= 0 {
		preserve[lastAssistant] = true
		// Find the most recent non-tool-result user entry before it.
		for ei := lastAssistant - 1; ei >= 0; ei-- {
			if slice.PostBoundary[ei].Type == "user" && !isToolResultOnly(slice.PostBoundary[ei]) {
				preserve[ei] = true
				break
			}
		}
	}
	out := make([]int, 0, len(chatEntries))
	for _, ei := range chatEntries {
		if preserve[ei] {
			continue
		}
		out = append(out, ei)
	}
	return out
}

func finalize(in PlanInput, opts SynthOptions, baseline, finalTail int, log []IterationRecord, hit bool) *PlanResult {
	return &PlanResult{
		Options:      opts,
		BaselineTail: baseline,
		FinalTail:    finalTail,
		Iterations:   log,
		HitTarget:    hit,
		BoundaryTail: Synthesize(in.Slice, opts),
	}
}
