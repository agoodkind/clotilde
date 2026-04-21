//go:build !darwin

package tuiqa

import "fmt"

func newITermDriver() (Driver, error) {
	return nil, fmt.Errorf("tuiqa: iterm driver is only available on darwin")
}

// AttachITermSession is a stub on non-darwin builds.
func AttachITermSession(sessionID string) Driver {
	return nil
}

// ITermSessionID is a stub on non-darwin builds.
func ITermSessionID(d Driver) string {
	return ""
}
