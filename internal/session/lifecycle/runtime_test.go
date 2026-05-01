package lifecycle

import (
	"testing"

	"goodkind.io/clyde/internal/session"
)

func TestForProviderClaude(t *testing.T) {
	runtime, err := ForProvider(session.ProviderClaude, nil)
	if err != nil {
		t.Fatalf("ForProvider returned error: %v", err)
	}
	if runtime == nil {
		t.Fatal("ForProvider returned nil runtime")
	}
}

func TestForProviderUnknown(t *testing.T) {
	runtime, err := ForProvider(session.ProviderID("unknown"), nil)
	if err == nil {
		t.Fatal("ForProvider returned nil error for unknown provider")
	}
	if runtime != nil {
		t.Fatalf("ForProvider returned runtime %#v for unknown provider", runtime)
	}
}
