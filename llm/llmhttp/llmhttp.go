// Package llmhttp provides HTTP utilities for LLM requests, namely a
// custom transport that adds Shelley-specific headers and enforces an
// idle/stall timeout on streaming responses.
package llmhttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/version"
)

// contextKey is the type for context keys in this package.
type contextKey int

const (
	conversationIDKey contextKey = iota
	modelIDKey
	providerKey
)

// shelleyRequestIDHeader is the header Shelley sets on every LLM request with a
// locally-generated id. It flows through the exe.dev gateway into the access
// logs, so a user-reported id can be correlated with the server-side trace_id.
const shelleyRequestIDHeader = "Shelley-Request-Id"

// upstreamRequestIDHeaders are response headers, in priority order, that
// providers use to expose their own request/correlation id. The exe.dev
// gateway strips account-identifying headers (Cf-Ray, rate limits, org) but
// forwards these request ids, so Shelley can surface them.
var upstreamRequestIDHeaders = []string{
	"X-Request-Id",
	"Request-Id",
	"Openai-Request-Id",
	"X-Amzn-Requestid",
}

// newRequestID returns a short random hex id for correlating a request.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read essentially never fails; fall back to a timestamp so we
		// still emit *something* correlatable.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// captureUpstreamRequestID records the provider's request id from response
// headers into trace, if present. No-op when trace is nil.
func captureUpstreamRequestID(trace *llm.RequestTrace, h http.Header) {
	if trace == nil || h == nil {
		return
	}
	for _, name := range upstreamRequestIDHeaders {
		if value := h.Get(name); value != "" {
			trace.Set("upstream_request_id", value)
			return
		}
	}
}

// WithConversationID returns a context with the conversation ID attached.
func WithConversationID(ctx context.Context, conversationID string) context.Context {
	return context.WithValue(ctx, conversationIDKey, conversationID)
}

// ConversationIDFromContext returns the conversation ID from the context, if any.
func ConversationIDFromContext(ctx context.Context) string {
	if v := ctx.Value(conversationIDKey); v != nil {
		return v.(string)
	}
	return ""
}

// WithModelID returns a context with the model ID attached.
func WithModelID(ctx context.Context, modelID string) context.Context {
	return context.WithValue(ctx, modelIDKey, modelID)
}

// ModelIDFromContext returns the model ID from the context, if any.
func ModelIDFromContext(ctx context.Context) string {
	if v := ctx.Value(modelIDKey); v != nil {
		return v.(string)
	}
	return ""
}

// WithProvider returns a context with the provider name attached.
func WithProvider(ctx context.Context, provider string) context.Context {
	return context.WithValue(ctx, providerKey, provider)
}

// ProviderFromContext returns the provider name from the context, if any.
func ProviderFromContext(ctx context.Context) string {
	if v := ctx.Value(providerKey); v != nil {
		return v.(string)
	}
	return ""
}

const idleTimeoutMessage = "llm stream idle timeout: no data received within idle window"

type idleTimeoutError struct {
	timeout time.Duration
	cause   error
}

var _ llm.RequestError = (*idleTimeoutError)(nil)

func (e *idleTimeoutError) Error() string {
	return fmt.Sprintf("%s after %s: %v", idleTimeoutMessage, e.timeout, e.cause)
}

func (e *idleTimeoutError) RequestErrorInfo() llm.RequestErrorInfo {
	return llm.RequestErrorInfo{
		Retryable:         true,
		IdleStallDuration: e.timeout,
	}
}

// DefaultIdleTimeout is the idle/stall timeout applied to LLM requests when a
// client is built without an explicit value. It bounds how long we wait
// between bytes (including time-to-first-byte), not the total duration of a
// turn: a slow-but-progressing stream (e.g. a long high-reasoning response)
// runs to completion as long as it keeps making progress.
const DefaultIdleTimeout = 3 * time.Minute

// Transport wraps an http.RoundTripper to add Shelley-specific headers and
// enforce an idle/stall timeout on the response body.
type Transport struct {
	Base http.RoundTripper
	// IdleTimeout, when > 0, aborts a request if no response bytes are
	// received for this long. The timer resets on every successful read, so
	// it measures the gap between chunks (and time-to-first-byte), not total
	// duration. Zero disables the mechanism.
	IdleTimeout time.Duration
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid modifying the original
	req = req.Clone(req.Context())

	// Add User-Agent with Shelley version
	info := version.GetInfo()
	userAgent := "Shelley"
	if info.Commit != "" {
		userAgent += "/" + info.Commit[:min(8, len(info.Commit))]
	}
	req.Header.Set("User-Agent", userAgent)

	// Assign a Shelley request id (or honor one already set) so every LLM
	// request is correlatable. The id is echoed into any RequestTrace on the
	// context so callers can surface it even when the request fails before a
	// response arrives (e.g. an idle/stall timeout).
	requestID := req.Header.Get(shelleyRequestIDHeader)
	if requestID == "" {
		requestID = newRequestID()
		req.Header.Set(shelleyRequestIDHeader, requestID)
	}
	trace := llm.RequestTraceFromContext(req.Context())
	if trace != nil {
		trace.Set("shelley_request_id", requestID)
	}

	// Add conversation ID header if present
	if conversationID := ConversationIDFromContext(req.Context()); conversationID != "" {
		req.Header.Set("Shelley-Conversation-Id", conversationID)

		// Add x-session-affinity header for Fireworks to enable prompt caching
		if ProviderFromContext(req.Context()) == "fireworks" {
			req.Header.Set("x-session-affinity", conversationID)
		}
	}
	if err := prepareOpenRouterCaching(req, ConversationIDFromContext(req.Context())); err != nil {
		return nil, err
	}

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	if t.IdleTimeout <= 0 {
		resp, err := base.RoundTrip(req)
		if resp != nil {
			captureUpstreamRequestID(trace, resp.Header)
		}
		return resp, err
	}

	// Install an idle watchdog. We derive a cancelable context so that when
	// the stream stalls we can abort the in-flight read at the transport
	// layer, unblocking a Body.Read that is waiting on the network. The
	// watchdog covers time-to-first-byte too: RoundTrip itself blocks until
	// headers arrive, so we start the timer before the RoundTrip call.
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	watch := &idleWatchdog{timeout: t.IdleTimeout, cancel: cancel}
	watch.start()

	resp, err := base.RoundTrip(req)
	if resp != nil {
		captureUpstreamRequestID(trace, resp.Header)
	}
	if err != nil {
		watch.stop()
		cancel()
		return nil, watch.translate(err)
	}

	// Wrap the body so each read resets the idle timer, and so the final
	// read error exposes idle-stall metadata when the watchdog fired.
	resp.Body = &idleReadCloser{
		ReadCloser: resp.Body,
		watch:      watch,
		cancel:     cancel,
	}
	return resp, nil
}

// prepareOpenRouterCaching gives OpenRouter a stable session key and sends
// explicit cache hints only for model families that need them. OpenRouter
// enables prefix caching automatically for many other providers; session_id
// keeps those requests on the same provider during a Shelley conversation.
func prepareOpenRouterCaching(req *http.Request, conversationID string) error {
	if req.URL.Hostname() != "openrouter.ai" || req.Method != http.MethodPost {
		return nil
	}
	path := strings.TrimSuffix(req.URL.Path, "/")
	if !strings.HasSuffix(path, "/chat/completions") && !strings.HasSuffix(path, "/responses") {
		return nil
	}
	if req.Body == nil {
		return nil
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return fmt.Errorf("read OpenRouter request for cache settings: %w", err)
	}
	_ = req.Body.Close()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("decode OpenRouter request for cache settings: %w", err)
	}
	if conversationID != "" {
		if _, exists := payload["session_id"]; !exists {
			sum := sha256.Sum256([]byte(conversationID))
			sessionID, _ := json.Marshal("shelley-" + hex.EncodeToString(sum[:]))
			payload["session_id"] = sessionID
		}
	}

	var requestModel string
	if err := json.Unmarshal(payload["model"], &requestModel); err != nil {
		return fmt.Errorf("decode OpenRouter model for cache settings: %w", err)
	}
	model := strings.TrimPrefix(strings.ToLower(requestModel), "~")
	switch {
	case strings.HasPrefix(model, "anthropic/"):
		// OpenRouter's automatic Claude caching advances this boundary with
		// the conversation and also translates it for Bedrock and Vertex.
		if _, exists := payload["cache_control"]; !exists {
			payload["cache_control"] = json.RawMessage(`{"type":"ephemeral"}`)
		}
	case explicitOpenRouterCacheModel(model) && strings.HasSuffix(path, "/chat/completions"):
		// These OpenRouter models require a content breakpoint. The shared
		// Shelley loop already puts its cache boundary on the latest user turn.
		messages, err := addOpenRouterCacheBreakpoint(payload["messages"])
		if err != nil {
			return err
		}
		if messages != nil {
			payload["messages"] = messages
		}
	}

	updated, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode OpenRouter cache settings: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(updated))
	req.ContentLength = int64(len(updated))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(updated)), nil
	}
	return nil
}

func explicitOpenRouterCacheModel(model string) bool {
	switch {
	case model == "deepseek/deepseek-v3.2":
		return true
	case strings.HasPrefix(model, "qwen/qwen3-max"),
		strings.HasPrefix(model, "qwen/qwen-plus"),
		strings.HasPrefix(model, "qwen/qwen3.6-plus"),
		strings.HasPrefix(model, "qwen/qwen3-coder-plus"),
		strings.HasPrefix(model, "qwen/qwen3-coder-flash"):
		return true
	default:
		return false
	}
}

func addOpenRouterCacheBreakpoint(rawMessages json.RawMessage) (json.RawMessage, error) {
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return nil, fmt.Errorf("decode OpenRouter messages for cache breakpoint: %w", err)
	}
	for i := len(messages) - 1; i >= 0; i-- {
		var role string
		if err := json.Unmarshal(messages[i]["role"], &role); err != nil || role != "user" {
			continue
		}
		content := messages[i]["content"]
		if len(content) == 0 {
			return nil, nil
		}
		var text string
		if err := json.Unmarshal(content, &text); err == nil {
			block := map[string]any{"type": "text", "text": text, "cache_control": map[string]string{"type": "ephemeral"}}
			encoded, err := json.Marshal([]any{block})
			if err != nil {
				return nil, fmt.Errorf("encode OpenRouter cache breakpoint: %w", err)
			}
			messages[i]["content"] = encoded
		} else {
			var blocks []map[string]json.RawMessage
			if err := json.Unmarshal(content, &blocks); err != nil {
				return nil, fmt.Errorf("decode OpenRouter message content for cache breakpoint: %w", err)
			}
			for j := len(blocks) - 1; j >= 0; j-- {
				var kind string
				if err := json.Unmarshal(blocks[j]["type"], &kind); err == nil && kind == "text" {
					blocks[j]["cache_control"] = json.RawMessage(`{"type":"ephemeral"}`)
					encoded, err := json.Marshal(blocks)
					if err != nil {
						return nil, fmt.Errorf("encode OpenRouter message content for cache breakpoint: %w", err)
					}
					messages[i]["content"] = encoded
					break
				}
			}
		}
		encoded, err := json.Marshal(messages)
		if err != nil {
			return nil, fmt.Errorf("encode OpenRouter messages for cache breakpoint: %w", err)
		}
		return encoded, nil
	}
	return nil, nil
}

// idleWatchdog cancels a request's context if no progress is reported within
// the timeout. Each call to reset() restarts the countdown.
type idleWatchdog struct {
	timeout time.Duration
	cancel  context.CancelFunc

	mu      sync.Mutex
	timer   *time.Timer
	fired   bool
	stopped bool
}

func (w *idleWatchdog) start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer = time.AfterFunc(w.timeout, w.onFire)
}

func (w *idleWatchdog) onFire() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.fired = true
	w.mu.Unlock()
	w.cancel()
}

// reset restarts the idle countdown. It is a no-op once the watchdog has
// fired or been stopped.
func (w *idleWatchdog) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.fired || w.timer == nil {
		return
	}
	w.timer.Reset(w.timeout)
}

// stop halts the watchdog. Safe to call multiple times.
func (w *idleWatchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
	}
}

func (w *idleWatchdog) hasFired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fired
}

// translate converts an error into a retryable idle-stall error when the
// watchdog fired, preserving the underlying cause text for debugging.
func (w *idleWatchdog) translate(err error) error {
	if err == nil || !w.hasFired() {
		return err
	}
	return &idleTimeoutError{timeout: w.timeout, cause: err}
}

// idleReadCloser resets the idle watchdog on every read and translates a read
// error caused by the watchdog into provider-neutral idle-stall metadata.
type idleReadCloser struct {
	io.ReadCloser
	watch  *idleWatchdog
	cancel context.CancelFunc
}

func (r *idleReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		// Progress: restart the countdown.
		r.watch.reset()
	}
	if err != nil {
		// A natural end of stream is not a stall. Stop the watchdog so a
		// delayed Close (or a read past EOF) can't retroactively translate
		// this into a spurious idle-stall error.
		if errors.Is(err, io.EOF) {
			r.watch.stop()
			return n, err
		}
		return n, r.watch.translate(err)
	}
	return n, nil
}

func (r *idleReadCloser) Close() error {
	r.watch.stop()
	r.cancel()
	return r.ReadCloser.Close()
}

// NewClient creates an http.Client with Shelley headers applied via Transport
// and the default idle/stall timeout.
func NewClient(base *http.Client) *http.Client {
	return NewClientWithIdleTimeout(base, DefaultIdleTimeout)
}

// NewClientWithIdleTimeout is like NewClient but with an explicit idle/stall
// timeout. A value <= 0 disables the idle timeout.
func NewClientWithIdleTimeout(base *http.Client, idleTimeout time.Duration) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}

	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	return &http.Client{
		Transport: &Transport{Base: transport, IdleTimeout: idleTimeout},
		Timeout:   base.Timeout,
	}
}
