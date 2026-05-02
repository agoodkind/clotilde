package transcript

import (
	"log/slog"
)

func transcriptLog() *slog.Logger {
	return slog.Default()
}
