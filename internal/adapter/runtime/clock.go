package runtime

import "time"

type wallClock struct{}

func (wallClock) Now() time.Time {
	return time.Now()
}

var runtimeClock wallClock
