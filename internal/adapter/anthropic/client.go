// Client construction and /v1/messages request execution.
package anthropic

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/oauth"
)

// sessionID is a per-daemon-process UUIDv4 used for the
// X-Claude-Code-Session-Id header. Generated lazily once at first
// /v1/messages request and stable for the lifetime of the daemon,
// which mirrors how the official CLI uses it.
var sessionID = onceSessionID()

func onceSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure on macOS is effectively impossible.
		// Fall back to a stable string so the header is always set.
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// New builds a Client. If httpClient is nil a 10 minute timeout
// client is used; long timeouts matter because /v1/messages can keep
// a connection open for the full inference window on large outputs.
// cfg carries the impersonation triplet sourced from
// [adapter.impersonation] in the user's toml. New does not validate
// cfg; callers (the daemon adapter wiring) should refuse to start
// when any field is empty.
func New(httpClient *http.Client, source *oauth.Manager, cfg Config) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Minute}
	}
	return &Client{http: httpClient, oauth: managerWrap(source), cfg: cfg}
}

// SystemPromptPrefix returns the configured prefix so callers
// (oauth_handler) can prepend it to outgoing system prompts without
// reaching into the Client struct.
func (c *Client) SystemPromptPrefix() string { return c.cfg.SystemPromptPrefix }

// UserAgent returns the configured impersonation User-Agent so callers
// (oauth_handler) can derive things like the CLI version for the
// attribution-header fingerprint without re-parsing the config.
func (c *Client) UserAgent() string { return c.cfg.UserAgent }

// managerWrap lets us pass an *oauth.Manager directly while keeping
// the OAuthSource interface for tests.
func managerWrap(m *oauth.Manager) OAuthSource {
	if m == nil {
		return nil
	}
	return OAuthSource(m)
}

// StreamEvents issues a streaming /v1/messages request and invokes sink
// for each decoded stream event (text, tool-use lifecycle, thinking,
// and final stop).
func (c *Client) StreamEvents(ctx context.Context, req Request, sink EventSink) (Usage, string, error) {
	req.Stream = true
	resp, err := c.do(ctx, req)
	if err != nil {
		return Usage{}, "", err
	}
	defer resp.Body.Close()

	usage := Usage{}
	stopReason := ""
	blockTypes := make(map[int]string)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)

	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			currentEvent = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(line[len("data:"):])
			if data == "" {
				continue
			}
			if err := dispatchSSE(currentEvent, data, sink, &usage, &stopReason, blockTypes); err != nil {
				return usage, stopReason, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, stopReason, fmt.Errorf("anthropic stream scan: %w", err)
	}
	return usage, stopReason, nil
}

func (c *Client) do(ctx context.Context, req Request) (*http.Response, error) {
	if c.oauth == nil {
		return nil, errors.New("anthropic client missing oauth source")
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = MaxOutputTokens
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	token, err := c.oauth.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("oauth token: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, MessagesURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build messages request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("anthropic-beta", c.cfg.BetaHeader)
	httpReq.Header.Set("anthropic-version", oauth.Version)
	httpReq.Header.Set("x-app", "cli")
	httpReq.Header.Set("User-Agent", c.cfg.UserAgent)
	httpReq.Header.Set("Content-Type", "application/json")
	// Match the official @anthropic-ai/sdk v0.81.0 request fingerprint
	// captured from claude-cli 2.1.114. These extra headers do not change
	// model behavior but appear to participate in OAuth bucket selection;
	// without them, identical requests land in a stricter throttling
	// bucket and 429 immediately. See docs/openai-adapter.md "OAuth bucket
	// impersonation drift" for the captured ground truth and diff method.
	// TODO: move these to the config file
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	httpReq.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
	httpReq.Header.Set("X-Claude-Code-Session-Id", sessionID)
	httpReq.Header.Set("X-Stainless-Lang", "js")
	httpReq.Header.Set("X-Stainless-Package-Version", "0.81.0")
	httpReq.Header.Set("X-Stainless-Os", "MacOS")
	httpReq.Header.Set("X-Stainless-Arch", "arm64")
	httpReq.Header.Set("X-Stainless-Runtime", "node")
	httpReq.Header.Set("X-Stainless-Runtime-Version", "v24.3.0")
	httpReq.Header.Set("X-Stainless-Retry-Count", "0")
	httpReq.Header.Set("X-Stainless-Timeout", "600")

	slog.Debug("anthropic.messages.request",
		"subcomponent", "anthropic",
		"model", req.Model,
		"url", MessagesURL,
		"body_bytes", len(body),
		"headers", redactedOutboundHeaders(httpReq.Header),
		"body", string(body),
		"body_b64", base64.StdEncoding.EncodeToString(body),
	)

	postStarted := time.Now()
	resp, err := c.http.Do(httpReq)
	if resp != nil {
		// We send Accept-Encoding ourselves to match the official CLI
		// fingerprint, so Go's transparent gzip handling is disabled.
		// Swap resp.Body in place with a decoding reader so every
		// downstream consumer (error body readers, SSE stream parser)
		// sees plaintext. Unsupported encodings (br, zstd) leave the
		// body untouched so callers can still inspect bytes for debug.
		decodeResponseBody(resp)
	}
	if err != nil {
		logResponse(slog.LevelError, "anthropic.messages.post_failed", responseEvent{
			Subcomponent: "anthropic",
			Model:        req.Model,
			BodyBytes:    len(body),
			DurationMs:   time.Since(postStarted).Milliseconds(),
			Err:          err.Error(),
		})
		return nil, fmt.Errorf("post /v1/messages: %w", err)
	}

	base := responseEvent{
		Subcomponent: "anthropic",
		Model:        req.Model,
		Status:       resp.StatusCode,
		RequestID:    resp.Header.Get("request-id"),
		BodyBytes:    len(body),
		DurationMs:   time.Since(postStarted).Milliseconds(),
		RateLimits:   rateLimitAttrs(resp.Header),
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		errBody := readDecodedBody(resp)
		ev := base
		ev.RetryAfter = resp.Header.Get("retry-after")
		ev.Body = string(errBody)
		ev.BodyB64 = base64.StdEncoding.EncodeToString(errBody)
		ev.BodyBytes = len(errBody)
		logResponse(slog.LevelWarn, "anthropic.ratelimit", ev)
		return nil, fmt.Errorf("anthropic %s: %s", resp.Status, truncate(string(errBody), 600))
	}
	if resp.StatusCode != http.StatusOK {
		errBody := readDecodedBody(resp)
		ev := base
		ev.Body = string(errBody)
		ev.BodyB64 = base64.StdEncoding.EncodeToString(errBody)
		ev.BodyBytes = len(errBody)
		logResponse(slog.LevelError, "anthropic.messages.upstream_error", ev)
		return nil, fmt.Errorf("anthropic %s: %s", resp.Status, truncate(string(errBody), 600))
	}
	logResponse(slog.LevelInfo, "anthropic.messages.connected", base)
	return resp, nil
}

// redactedOutboundHeaders returns a flat map[string]string of the
// headers we set on the Anthropic /v1/messages request, with secret
// values masked. Keys are lowercased so log diffs are deterministic
// and friendly to grep. Used by the anthropic.messages.request slog
// event so debug captures show exactly what we sent.
// decodeResponseBody swaps resp.Body for a decoding reader matching
// resp.Header.Get("Content-Encoding"). Stdlib covers gzip and deflate;
// br and zstd are passed through untouched (the upstream rarely picks
// them when gzip is also offered, and decoding them would require a
// new dep). Removes the Content-Encoding header on success so callers
// don't double-decode.
func decodeResponseBody(resp *http.Response) {
	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if enc == "" || enc == "identity" {
		return
	}
	switch enc {
	case "gzip":
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			slog.Warn("anthropic.response.gzip_decode_failed",
				"subcomponent", "anthropic", "err", err.Error())
			return
		}
		resp.Body = &decodedBody{r: zr, src: resp.Body}
		resp.Header.Del("Content-Encoding")
	case "deflate":
		fr := flate.NewReader(resp.Body)
		resp.Body = &decodedBody{r: fr, src: resp.Body}
		resp.Header.Del("Content-Encoding")
	default:
		// br, zstd, etc. Keep raw bytes; callers will see binary in
		// the slog body field if Anthropic actually picks one of these.
		slog.Warn("anthropic.response.unsupported_encoding",
			"subcomponent", "anthropic", "encoding", enc)
	}
}

// decodedBody wraps a decompressing reader so Close() also closes the
// underlying response body the http client owns.
type decodedBody struct {
	r   io.ReadCloser
	src io.ReadCloser
}

func (d *decodedBody) Read(p []byte) (int, error) { return d.r.Read(p) }
func (d *decodedBody) Close() error {
	rerr := d.r.Close()
	serr := d.src.Close()
	if rerr != nil {
		return rerr
	}
	return serr
}

// readDecodedBody reads resp.Body to EOF and closes it. Assumes
// decodeResponseBody already wrapped Body if Content-Encoding was set.
// Returns the bytes the caller would have seen as if no encoding was
// applied.
func readDecodedBody(resp *http.Response) []byte {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b
}

func redactedOutboundHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for key, values := range h {
		lk := strings.ToLower(key)
		joined := strings.Join(values, ", ")
		switch lk {
		case "authorization":
			out[lk] = fmt.Sprintf("Bearer <redacted len=%d>", len(joined)-len("Bearer "))
		case "x-api-key", "cookie", "proxy-authorization":
			out[lk] = "<redacted>"
		default:
			out[lk] = joined
		}
	}
	return out
}
