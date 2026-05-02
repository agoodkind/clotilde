package ui

import "time"

type uiClock interface {
	Now() time.Time
}

type systemUIClock struct{}

func (systemUIClock) Now() time.Time {
	return time.Now()
}

var defaultUIClock uiClock = systemUIClock{}

func currentUITime() time.Time {
	return defaultUIClock.Now()
}

func (a *App) now() time.Time {
	if a == nil || a.clock == nil {
		return currentUITime()
	}
	return a.clock.Now()
}
