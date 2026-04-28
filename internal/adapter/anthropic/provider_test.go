package anthropic

import (
	"context"
	"errors"
	"testing"

	adapterprovider "goodkind.io/clyde/internal/adapter/provider"
	adapterresolver "goodkind.io/clyde/internal/adapter/resolver"
)

func TestProviderIDIsAnthropic(t *testing.T) {
	p := NewProvider(ProviderOptions{})
	if got := p.ID(); got != adapterresolver.ProviderAnthropic {
		t.Errorf("ID() = %q, want %q", got, adapterresolver.ProviderAnthropic)
	}
}

func TestProviderExecuteReturnsLegacyDispatchSentinel(t *testing.T) {
	p := NewProvider(ProviderOptions{})
	_, err := p.Execute(context.Background(), adapterresolver.ResolvedRequest{}, nil)
	if !errors.Is(err, ErrLegacyDispatchPath) {
		t.Fatalf("Execute() err = %v, want ErrLegacyDispatchPath", err)
	}
}

// satisfiesProviderInterface fails to compile if Provider does not
// satisfy the upstream-agnostic adapter/provider.Provider contract.
// It is the cheapest available guarantee that a future change to the
// Provider type does not silently regress its registry compatibility.
func TestProviderSatisfiesInterface(t *testing.T) {
	var _ adapterprovider.Provider = (*Provider)(nil)
}
