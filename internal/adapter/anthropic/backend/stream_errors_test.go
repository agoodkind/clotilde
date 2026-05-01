package anthropicbackend

import (
	"errors"
	"net/http"
	"testing"

	"goodkind.io/clyde/internal/adapter/anthropic"
)

func TestActionableStreamErrorMessageRoutesByClass(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "real_429_upstream_error_keeps_rate_limit_text",
			err: &anthropic.UpstreamError{
				Classification: anthropic.Classify(&http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}, nil),
				Status:         http.StatusTooManyRequests,
				Message:        "rate limited",
			},
			want: "Clyde adapter hit an upstream rate limit. Wait a moment and retry.",
		},
		{
			name: "real_401_upstream_error_keeps_auth_text",
			err: &anthropic.UpstreamError{
				Classification: anthropic.Classify(&http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}}, nil),
				Status:         http.StatusUnauthorized,
				Message:        "token expired",
			},
			want: "Clyde adapter upstream auth failed. Re-authenticate Claude with `claude /login`, then retry.",
		},
		{
			name: "fatal_4xx_upstream_error_uses_generic_text",
			err: &anthropic.UpstreamError{
				Classification: anthropic.Classify(&http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{}}, nil),
				Status:         http.StatusBadRequest,
				Message:        "bad request",
			},
			want: "Clyde adapter request failed upstream. Check ~/.local/state/clyde/clyde.jsonl, then retry.",
		},
		{
			name: "bare_error_with_rate_limit_word_uses_generic_text",
			err:  errors.New("downstream subprocess complained about rate limit fairness"),
			want: "Clyde adapter request failed upstream. Check ~/.local/state/clyde/clyde.jsonl, then retry.",
		},
		{
			name: "bare_error_with_429_token_uses_generic_text",
			err:  errors.New("agent reported 429 children in payload"),
			want: "Clyde adapter request failed upstream. Check ~/.local/state/clyde/clyde.jsonl, then retry.",
		},
		{
			name: "bare_oauth_error_keeps_auth_text",
			err:  errors.New("oauth: refresh failed"),
			want: "Clyde adapter upstream auth failed. Re-authenticate Claude with `claude /login`, then retry.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ActionableStreamErrorMessage(tc.err); got != tc.want {
				t.Fatalf("ActionableStreamErrorMessage(%v) = %q\nwant: %q", tc.err, got, tc.want)
			}
		})
	}
}
