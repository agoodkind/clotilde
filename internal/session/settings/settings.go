// Package settings dispatches provider-owned per-session settings operations.
package settings

import (
	"fmt"

	"goodkind.io/clyde/internal/session"
)

// Load returns provider-owned per-session settings for sess.
func Load(store session.Store, sess *session.Session) (*session.Settings, error) {
	if store == nil {
		return nil, fmt.Errorf("nil store")
	}
	if sess == nil {
		return nil, fmt.Errorf("nil session")
	}
	switch sess.ProviderID() {
	case session.ProviderClaude:
		return store.LoadSettings(sess.Name)
	default:
		return nil, fmt.Errorf("unsupported session provider %q", sess.ProviderID())
	}
}

// Save persists provider-owned per-session settings for sess.
func Save(store session.Store, sess *session.Session, settings *session.Settings) error {
	if store == nil {
		return fmt.Errorf("nil store")
	}
	if sess == nil {
		return fmt.Errorf("nil session")
	}
	switch sess.ProviderID() {
	case session.ProviderClaude:
		return store.SaveSettings(sess.Name, settings)
	default:
		return fmt.Errorf("unsupported session provider %q", sess.ProviderID())
	}
}
