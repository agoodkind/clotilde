package config_test

import (
	"strings"
	"testing"

	"goodkind.io/clyde/internal/config"
)

func TestAdapterOAuthValidateOAuthFields(t *testing.T) {
	full := config.AdapterOAuth{
		TokenURL:         "https://example/token",
		MessagesURL:      "https://example/messages",
		ClientID:         "cid",
		AnthropicBeta:    "beta",
		AnthropicVersion: "ver",
		KeychainService:  "svc",
		Scopes:           []string{"a", "b"},
	}
	if err := full.ValidateOAuthFields(); err != nil {
		t.Fatalf("valid oauth: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*config.AdapterOAuth)
		sub  string
	}{
		{"empty_token_url", func(o *config.AdapterOAuth) { o.TokenURL = "" }, "token_url"},
		{"empty_messages_url", func(o *config.AdapterOAuth) { o.MessagesURL = "" }, "messages_url"},
		{"empty_client_id", func(o *config.AdapterOAuth) { o.ClientID = "" }, "client_id"},
		{"empty_anthropic_beta", func(o *config.AdapterOAuth) { o.AnthropicBeta = "" }, "anthropic_beta"},
		{"empty_anthropic_version", func(o *config.AdapterOAuth) { o.AnthropicVersion = "" }, "anthropic_version"},
		{"empty_keychain_service", func(o *config.AdapterOAuth) { o.KeychainService = "" }, "keychain_service"},
		{"empty_scopes", func(o *config.AdapterOAuth) { o.Scopes = nil }, "scopes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := full
			tc.mut(&o)
			err := o.ValidateOAuthFields()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.sub) {
				t.Fatalf("err = %v want substring %q", err, tc.sub)
			}
		})
	}
}
