package ui

func logUIGoroutinePanic(name string, recoveredValue string) {
	tuiLog.Logger().Error("tui.goroutine.panic",
		"component", "tui",
		"goroutine", name,
		"recover", recoveredValue,
		"err", "panic")
}
