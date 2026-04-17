package ui

// ConfirmModel is the public handle for a Yes/No dialog. The builder chain
// (NewConfirm(...).WithDetails(...).WithDestructive()) mirrors the legacy
// BubbleTea API so cmd/delete and similar callers need no changes.
type ConfirmModel struct {
	Title       string
	Message     string
	Details     []string
	Destructive bool
}

// NewConfirm constructs a default (non-destructive) confirm dialog.
func NewConfirm(title, message string) ConfirmModel {
	return ConfirmModel{Title: title, Message: message}
}

// WithDetails adds a bullet list below the message.
func (m ConfirmModel) WithDetails(details []string) ConfirmModel {
	m.Details = details
	return m
}

// WithDestructive paints the confirm button red to signal a destructive op.
func (m ConfirmModel) WithDestructive() ConfirmModel {
	m.Destructive = true
	return m
}

// RunConfirm displays the confirm dialog and returns true if the user
// approved, false on cancel. Ctrl+C also returns (false, nil).
func RunConfirm(m ConfirmModel) (bool, error) {
	confirmed := false
	err := runOverlay(func(done func()) Widget {
		modal := &Modal{
			Title:       m.Title,
			Body:        m.Message,
			Details:     m.Details,
			Buttons:     []string{"Cancel", "Confirm"},
			ActiveIndex: 0,
			Destructive: m.Destructive,
			Shortcuts:   map[rune]int{'y': 1, 'Y': 1, 'n': 0, 'N': 0},
		}
		modal.OnChoice = func(idx int) {
			confirmed = idx == 1
			done()
		}
		return modal
	})
	if err != nil {
		return false, err
	}
	return confirmed, nil
}
