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
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/oauth"
)

// sessionID is a per-daemon-process UUIDv4 used for the session
// correlation header. Generated lazily once at the first messages
// request and stable for the lifetime of the daemon.
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
// cfg carries wire values from [adapter.client_identity] and
// [adapter.oauth]. New does not validate cfg; callers should refuse
// to start when required fields are empty.
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

// UserAgent returns the configured User-Agent so callers can derive
// a semver-like prefix for the billing line without re-parsing config.
func (c *Client) UserAgent() string { return c.cfg.UserAgent }

// CCVersion returns the configured cc_version fallback when User-Agent
// parsing yields no version segment.
func (c *Client) CCVersion() string { return c.cfg.CCVersion }

// CCEntrypoint returns the configured cc_entrypoint suffix for the
// billing line.
func (c *Client) CCEntrypoint() string { return c.cfg.CCEntrypoint }

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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.MessagesURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build messages request: %w", err)
	}
	// Wire signals required by the upstream identity check; values come from cfg.

	// Auth + protocol.
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("anthropic-version", c.cfg.OAuthAnthropicVersion)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")

	// CLYDE_PROBE_DROP is a comma-separated list of header names to
	// omit below for debugging. Empty means send the full configured set.
	dropped := probeDropSet()
	setHard := func(key, value string) {
		if _, skip := dropped[strings.ToLower(key)]; skip {
			return
		}
		httpReq.Header.Set(key, value)
	}

	// Header values from client identity config.
	beta := c.cfg.BetaHeader
	if len(req.ExtraBetas) > 0 {
		// Dedupe-merge: only append flags not already in the static set.
		existing := map[string]struct{}{}
		for _, f := range strings.Split(beta, ",") {
			existing[strings.TrimSpace(f)] = struct{}{}
		}
		for _, extra := range req.ExtraBetas {
			extra = strings.TrimSpace(extra)
			if extra == "" {
				continue
			}
			if _, dup := existing[extra]; dup {
				continue
			}
			beta = beta + "," + extra
			existing[extra] = struct{}{}
		}
	}
	setHard("anthropic-beta", beta)
	setHard("User-Agent", c.cfg.UserAgent)
	setHard("X-Stainless-Package-Version", c.cfg.StainlessPackageVersion)
	setHard("X-Stainless-Runtime", c.cfg.StainlessRuntime)
	setHard("X-Stainless-Runtime-Version", c.cfg.StainlessRuntimeVersion)

	// Runtime-derived headers plus defaults.
	for _, h := range freeIdentityHeaders(c) {
		httpReq.Header.Set(h.key, h.value)
	}

	if len(dropped) > 0 {
		keys := make([]string, 0, len(dropped))
		for k := range dropped {
			keys = append(keys, k)
		}
		slog.Warn("anthropic.probe.headers_dropped",
			"subcomponent", "anthropic",
			"dropped", keys,
		)
	}

	slog.Debug("anthropic.messages.request",
		"subcomponent", "anthropic",
		"model", req.Model,
		"url", c.cfg.MessagesURL,
		"body_bytes", len(body),
		"headers", redactedOutboundHeaders(httpReq.Header),
		"body", string(body),
		"body_b64", base64.StdEncoding.EncodeToString(body),
	)

	postStarted := time.Now()
	resp, err := c.http.Do(httpReq)
	if resp != nil {
		// We set Accept-Encoding explicitly, so Go's transparent gzip
		// handling is disabled.
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

// probeDropSet returns the lowercased set of header names in CLYDE_PROBE_DROP.
func probeDropSet() map[string]struct{} {
	raw := strings.TrimSpace(os.Getenv("CLYDE_PROBE_DROP"))
	if raw == "" {
		return nil
	}
	out := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

type freeHeader struct {
	key   string
	value string
}

func freeIdentityHeaders(c *Client) []freeHeader {
	return []freeHeader{
		{key: "x-app", value: "cli"},
		{key: "Anthropic-Dangerous-Direct-Browser-Access", value: "true"},
		{key: "X-Claude-Code-Session-Id", value: sessionID},
		{key: "X-Stainless-Lang", value: "js"},
		{key: "X-Stainless-Os", value: stainlessOS()},
		{key: "X-Stainless-Arch", value: stainlessArch()},
		// clyde does not retry at the HTTP layer; SDK retry is delegated to the caller.
		// Value is honest at 0.
		{key: "X-Stainless-Retry-Count", value: "0"},
		{key: "X-Stainless-Timeout", value: c.stainlessTimeout()},
	}
}

func (c *Client) stainlessTimeout() string {
	if c.http.Timeout == 0 {
		return "600"
	}
	return strconv.Itoa(int(c.http.Timeout / time.Second))
}

func stainlessOS() string {
	return stainlessOSFromGOOS(runtime.GOOS)
}

func stainlessOSFromGOOS(goos string) string {
	switch goos {
	case "darwin":
		return "MacOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return "Unknown"
	}
}

func stainlessArch() string {
	return stainlessArchFromGOARCH(runtime.GOARCH)
}

func stainlessArchFromGOARCH(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	default:
		return goarch
	}
}

// redactedOutboundHeaders returns a flat map[string]string of the
// headers we set on the outbound messages request, with secret
// values masked. Keys are lowercased so log diffs are deterministic
// and friendly to grep. Used by the anthropic.messages.request slog
// event so debug captures show exactly what we sent.
// decodeResponseBody swaps resp.Body for a decoding reader matching
// resp.Header.Get("Content-Encoding"). Stdlib covers gzip and deflate;
// br and zstd are passed through untouched (the upstream rarely picks
// them when gzip is also offered). Removes the Content-Encoding
// header on success so callers don't double-decode.
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
		// the slog body field if the server actually picks one of these.
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
