package adapter

import "context"

// BackgroundDetachContext returns [context.Background] for intentional
// long-running work that must survive client disconnect (for example the
// structured-output collect retry spawn).
func BackgroundDetachContext() context.Context {
	return context.Background()
}
