package chatemit

type ErrorWriter func(status int, code, msg string) error

// EscalateOrWrite returns err unchanged when escalate is true; otherwise it
// writes an OpenAI-compatible error envelope to w and returns nil.
func EscalateOrWrite(err error, escalate bool, writeError ErrorWriter, status int, code, msg string) error {
	if escalate {
		return err
	}
	if writeError == nil || err == nil {
		return nil
	}
	_ = writeError(status, code, msg)
	return nil
}
