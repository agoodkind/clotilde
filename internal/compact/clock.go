package compact

import "time"

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

var compactClock systemClock
