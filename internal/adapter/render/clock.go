package render

import "time"

type wallClock struct{}

func (wallClock) Now() time.Time {
	return time.Now()
}

var renderClock wallClock
