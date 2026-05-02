package prune

import "time"

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

var pruneClock systemClock
