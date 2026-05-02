package codex

import "time"

type wallClock struct{}

func (wallClock) Now() time.Time {
	return time.Now()
}

var codexClock wallClock
