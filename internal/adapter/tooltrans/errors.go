package tooltrans

import "errors"

var (
	// ErrAudioUnsupported is returned when a message includes an input_audio part.
	ErrAudioUnsupported = errors.New("audio content parts are not supported by the Anthropic backend")

	// ErrUnknownToolType is returned when tools[].type is set to a value other than "function".
	ErrUnknownToolType = errors.New("tool.type must be \"function\"")
)
