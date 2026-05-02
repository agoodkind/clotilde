package search

import "time"

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

var searchClock systemClock
