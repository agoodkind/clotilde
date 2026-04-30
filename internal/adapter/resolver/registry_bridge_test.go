package resolver

import (
	"testing"

	adaptermodel "goodkind.io/clyde/internal/adapter/model"
)

func TestBackendToProviderMapping(t *testing.T) {
	cases := []struct {
		backend string
		want    ProviderID
	}{
		{adaptermodel.BackendAnthropic, ProviderAnthropic},
		{adaptermodel.BackendCodex, ProviderCodex},
		{adaptermodel.BackendClaude, ProviderUnknown},
		{adaptermodel.BackendShunt, ProviderUnknown},
		{"", ProviderUnknown},
		{"nonsense", ProviderUnknown},
	}
	for _, tc := range cases {
		if got := backendToProvider(tc.backend); got != tc.want {
			t.Errorf("backendToProvider(%q) = %v, want %v", tc.backend, got, tc.want)
		}
	}
}

func TestModelRegistryAdapterNilInner(t *testing.T) {
	a := NewModelRegistryAdapter(nil)
	if _, err := a.Resolve("anything", ""); err == nil {
		t.Fatal("expected error from nil-inner adapter, got nil")
	}
	var nilAdapter *ModelRegistryAdapter
	if _, err := nilAdapter.Resolve("anything", ""); err == nil {
		t.Fatal("expected error from nil adapter, got nil")
	}
}
