package anthropicbackend

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"goodkind.io/clyde/internal/adapter/anthropic"
)

// TestClassifyEscalationCauseExtractsTypedClassification confirms that
// the fallback escalation log enrichment correctly unwraps a typed
// *anthropic.UpstreamError into the flat (class, status, retryable)
// triple that the slog event needs. This is the lock-in test for
// Phase 4 step 3 (fallback transitions name the Anthropic classifier
// outcome).
func TestClassifyEscalationCauseExtractsTypedClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		err           error
		wantClass     string
		wantStatus    int
		wantRetryable bool
	}{
		{
			name:      "untyped_error_falls_through",
			err:       errors.New("plain transport"),
			wantClass: "untyped",
		},
		{
			name: "rate_limit_429_marks_retryable",
			err: &anthropic.UpstreamError{
				Classification: anthropic.Classify(&http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}, nil),
				Status:         http.StatusTooManyRequests,
			},
			wantClass:     "retryable_upstream_error",
			wantStatus:    http.StatusTooManyRequests,
			wantRetryable: true,
		},
		{
			name: "fatal_400_is_not_retryable",
			err: &anthropic.UpstreamError{
				Classification: anthropic.Classify(&http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{}}, nil),
				Status:         http.StatusBadRequest,
			},
			wantClass:  "fatal_upstream_error",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "transport_error_marks_retryable",
			err: &anthropic.UpstreamError{
				Classification: anthropic.Classify(nil, fmt.Errorf("dial tcp: timeout")),
			},
			wantClass:     "retryable_upstream_error",
			wantRetryable: true,
		},
		{
			name:      "nil_error_returns_untyped",
			err:       nil,
			wantClass: "untyped",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyEscalationCause(tc.err)
			if got.class != tc.wantClass {
				t.Fatalf("class=%q want %q", got.class, tc.wantClass)
			}
			if got.status != tc.wantStatus {
				t.Fatalf("status=%d want %d", got.status, tc.wantStatus)
			}
			if got.retryable != tc.wantRetryable {
				t.Fatalf("retryable=%t want %t", got.retryable, tc.wantRetryable)
			}
		})
	}
}
