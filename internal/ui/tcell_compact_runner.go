package ui

// RunCompactUI opens the standalone tcell compact form for the given session
// transcript. It replaces the legacy BubbleTea RunCompactUI. The return
// signature is kept identical so cmd/compact.go does not need changes.
func RunCompactUI(sessionName, transcriptPath string, chainLines []int, allLines []string) (CompactChoices, error) {
	_ = transcriptPath // the caller already has the data; signature parity only

	var result CompactChoices
	err := runOverlay(func(done func()) Widget {
		form := NewCompactForm(sessionName, allLines, chainLines)
		form.OnDone = func(c CompactChoices) {
			result = c
			done()
		}
		return form
	})
	if err != nil {
		return CompactChoices{}, err
	}
	// If the user hit Ctrl+C or closed without choosing, treat as cancel.
	if !result.Applied && !result.DryRun && !result.Cancelled {
		result.Cancelled = true
	}
	return result, nil
}
