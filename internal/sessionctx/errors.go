package sessionctx

import "errors"

// errNullLayer is returned by the nullLayer produced when NewDefault
// is called with a nil session. Exposed through Usage and Count so
// callers can test with errors.Is.
var errNullLayer = errors.New("sessionctx: layer bound to nil session")
