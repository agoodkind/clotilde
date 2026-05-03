// Package oauth manages adapter OAuth token flows and persistence.
package oauth

import (
	"fmt"
	"strings"
	"time"

	"goodkind.io/clyde/internal/claude"
)

// refreshSafetyWindow is how far before expiresAt we proactively refresh.
const refreshSafetyWindow = 30 * time.Second

// Tokens is the Claude Code OAuth credential payload used by the adapter.
type Tokens = claude.OAuthTokens

type credentialSnapshot struct {
	Source              claude.OAuthCredentialSource
	Fingerprint         string
	ExpiresAt           int64
	RefreshTokenPresent bool
	FileMtime           int64
}

type selectedCredential struct {
	Source    claude.OAuthCredentialSource
	Tokens    *Tokens
	Metadata  claude.OAuthCredentialMetadata
	Summaries []claude.OAuthCredentialSummary
}

// OAuthCredentialError describes an unusable local Claude OAuth credential set.
type OAuthCredentialError struct {
	Message   string
	Summaries []claude.OAuthCredentialSummary
}

func (e *OAuthCredentialError) Error() string {
	parts := make([]string, 0, len(e.Summaries))
	for _, summary := range e.Summaries {
		parts = append(parts, fmt.Sprintf("%s present=%t access_token_present=%t refresh_token_present=%t expired=%t parse_error=%q",
			summary.Source,
			summary.Present,
			summary.AccessTokenPresent,
			summary.RefreshTokenPresent,
			summary.Expired,
			summary.ParseError,
		))
	}
	if len(parts) == 0 {
		return e.Message
	}
	return e.Message + ": " + strings.Join(parts, "; ")
}
