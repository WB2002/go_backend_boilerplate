package httpclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sony/gobreaker"
	"go_backend_boilerplate/internal/core/middleware"
	"golang.org/x/sync/singleflight"
)

// ResilientClient defines the contract for resilient outbound HTTP operations.
type ResilientClient interface {
	Get(ctx context.Context, path string) (*http.Response, error)
	Post(ctx context.Context, path string, body any) (*http.Response, error)
	Do(req *http.Request) (*http.Response, error)
}

// Client implements ResilientClient with exponential jitter retries, circuit breaking,
// concurrency bulkheads, singleflight coalescing, and SSRF guard protection.
type Client struct {
	baseURL             string
	httpClient          *http.Client
	timeout             time.Duration
	maxRetries          int
	minRetryInterval    time.Duration
	maxRetryInterval    time.Duration
	circuitBreaker      *gobreaker.TwoStepCircuitBreaker
	concurrencySem      chan struct{}
	singleflightEnabled bool
	deadlinePropagation bool
	sfGroup             *singleflight.Group
}

var _ ResilientClient = (*Client)(nil)

// Builder configures and constructs a production-ready *Client.
type Builder struct {
	baseURL             string
	timeout             time.Duration
	maxRetries          int
	minRetryInterval    time.Duration
	maxRetryInterval    time.Duration
	circuitBreaker      *gobreaker.TwoStepCircuitBreaker
	concurrencyLimit    int
	singleflightEnabled bool
	deadlinePropagation bool
	ssrfProtection      bool
	httpClient          *http.Client
}

// New creates a production ResilientClient with safe production defaults.
func New() ResilientClient {
	return NewBuilder().Build()
}

// NewBuilder initializes a new Builder with safe production defaults.
func NewBuilder() *Builder {
	return &Builder{
		timeout:             30 * time.Second,
		minRetryInterval:    100 * time.Millisecond,
		maxRetryInterval:    2 * time.Second,
		deadlinePropagation: true,
		ssrfProtection:      true,
	}
}

// WithBaseURL sets the base URL for relative request paths.
func (b *Builder) WithBaseURL(url string) *Builder {
	b.baseURL = strings.TrimRight(url, "/")
	return b
}

// WithTimeout sets the overall client request timeout.
func (b *Builder) WithTimeout(d time.Duration) *Builder {
	if d > 0 {
		b.timeout = d
	}
	return b
}

// WithRetry configures exponential backoff with full jitter.
// Accepts maxRetries and optional minInterval and maxInterval durations.
func (b *Builder) WithRetry(maxRetries int, intervals ...time.Duration) *Builder {
	b.maxRetries = maxRetries
	if len(intervals) >= 1 && intervals[0] > 0 {
		b.minRetryInterval = intervals[0]
	}
	if len(intervals) >= 2 && intervals[1] > 0 {
		b.maxRetryInterval = intervals[1]
	} else if len(intervals) == 1 {
		b.maxRetryInterval = b.minRetryInterval * 10
	}
	return b
}

// WithCircuitBreaker configures Sony Gobreaker protection.
// Accepts (name string, failureRatio float64, timeout time.Duration),
// (threshold float64, timeout time.Duration), or a pre-configured *gobreaker.TwoStepCircuitBreaker.
func (b *Builder) WithCircuitBreaker(args ...any) *Builder {
	if len(args) == 0 {
		return b
	}

	if cb, ok := args[0].(*gobreaker.TwoStepCircuitBreaker); ok {
		b.circuitBreaker = cb
		return b
	}

	name := "http-client"
	failureRatio := 0.5
	timeout := 60 * time.Second

	switch len(args) {
	case 1:
		if r, ok := toFloat(args[0]); ok {
			failureRatio = r
		} else if s, ok := args[0].(string); ok {
			name = s
		}
	case 2:
		if r, ok := toFloat(args[0]); ok {
			failureRatio = r
			if t, ok := args[1].(time.Duration); ok {
				timeout = t
			}
		} else if s, ok := args[0].(string); ok {
			name = s
			if r, ok := toFloat(args[1]); ok {
				failureRatio = r
			} else if t, ok := args[1].(time.Duration); ok {
				timeout = t
			}
		}
	case 3:
		if s, ok := args[0].(string); ok {
			name = s
		}
		if r, ok := toFloat(args[1]); ok {
			failureRatio = r
		}
		if t, ok := args[2].(time.Duration); ok {
			timeout = t
		}
	}

	st := gobreaker.Settings{
		Name:    name,
		Timeout: timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < 3 {
				return false
			}
			failureRate := float64(counts.TotalFailures) / float64(counts.Requests)
			return failureRate >= failureRatio
		},
	}
	b.circuitBreaker = gobreaker.NewTwoStepCircuitBreaker(st)
	return b
}

func toFloat(val any) (float64, bool) {
	switch v := val.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

// WithConcurrencyLimit sets a bulkhead limit on concurrent outbound requests.
func (b *Builder) WithConcurrencyLimit(maxConcurrent int) *Builder {
	b.concurrencyLimit = maxConcurrent
	return b
}

// WithSingleflight enables or disables coalescing of duplicate concurrent GET requests.
func (b *Builder) WithSingleflight(enabled ...bool) *Builder {
	if len(enabled) == 0 {
		b.singleflightEnabled = true
	} else {
		b.singleflightEnabled = enabled[0]
	}
	return b
}

// WithDeadlinePropagation enables or disables propagating the context deadline
// and request ID in outbound HTTP headers.
func (b *Builder) WithDeadlinePropagation(enabled ...bool) *Builder {
	if len(enabled) == 0 {
		b.deadlinePropagation = true
	} else {
		b.deadlinePropagation = enabled[0]
	}
	return b
}

// WithSSRFProtection enables or disables the safe dialer that blocks private/loopback CIDRs.
func (b *Builder) WithSSRFProtection(enabled ...bool) *Builder {
	if len(enabled) == 0 {
		b.ssrfProtection = true
	} else {
		b.ssrfProtection = enabled[0]
	}
	return b
}

// WithHTTPClient overrides the underlying *http.Client (useful for unit testing transports).
func (b *Builder) WithHTTPClient(client *http.Client) *Builder {
	b.httpClient = client
	return b
}

// Build creates the configured *Client.
func (b *Builder) Build() *Client {
	var sem chan struct{}
	if b.concurrencyLimit > 0 {
		sem = make(chan struct{}, b.concurrencyLimit)
	}

	httpClient := b.httpClient
	if httpClient == nil {
		transport := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: NewSafeDialContext(
				!b.ssrfProtection,
				b.timeout,
				30*time.Second,
			),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		httpClient = &http.Client{
			Transport: transport,
			Timeout:   b.timeout,
		}
	}

	return &Client{
		baseURL:             b.baseURL,
		httpClient:          httpClient,
		timeout:             b.timeout,
		maxRetries:          b.maxRetries,
		minRetryInterval:    b.minRetryInterval,
		maxRetryInterval:    b.maxRetryInterval,
		circuitBreaker:      b.circuitBreaker,
		concurrencySem:      sem,
		singleflightEnabled: b.singleflightEnabled,
		deadlinePropagation: b.deadlinePropagation,
		sfGroup:             &singleflight.Group{},
	}
}

// Get executes a resilient HTTP GET request against the specified path or URL.
func (c *Client) Get(ctx context.Context, path string) (*http.Response, error) {
	fullURL := c.resolveURL(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create GET request: %w", err)
	}
	return c.Do(req)
}

// Post executes a resilient HTTP POST request against the specified path or URL.
// Body can be an io.Reader, []byte, string, or an arbitrary Go object (which is JSON serialized).
func (c *Client) Post(ctx context.Context, path string, body any) (*http.Response, error) {
	fullURL := c.resolveURL(path)
	var bodyReader io.Reader
	var isJSON bool

	if body != nil {
		switch v := body.(type) {
		case io.Reader:
			bodyReader = v
		case []byte:
			bodyReader = bytes.NewReader(v)
		case string:
			bodyReader = strings.NewReader(v)
		default:
			jsonBytes, err := json.Marshal(body)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal JSON request body: %w", err)
			}
			bodyReader = bytes.NewReader(jsonBytes)
			isJSON = true
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create POST request: %w", err)
	}
	if isJSON && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.Do(req)
}

// Do executes a resilient HTTP request honoring retries, circuit breaking, bulkheads,
// singleflight, and context deadline propagation.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if ctx == nil {
		ctx = context.Background()
		req = req.WithContext(ctx)
	}

	// Propagate request tracing and deadline headers
	if c.deadlinePropagation {
		if d, ok := ctx.Deadline(); ok {
			rem := time.Until(d)
			if rem > 0 && req.Header.Get("X-Request-Timeout") == "" {
				req.Header.Set("X-Request-Timeout", fmt.Sprintf("%dms", rem.Milliseconds()))
			}
		}
		if reqID := middleware.GetRequestIDFromContext(ctx); reqID != "" {
			if req.Header.Get("X-Request-ID") == "" {
				req.Header.Set("X-Request-ID", reqID)
			}
		}
	}

	// Singleflight deduplication for GET requests
	if c.singleflightEnabled && req.Method == http.MethodGet {
		key := req.URL.String()
		ch := c.sfGroup.DoChan(key, func() (any, error) {
			timeout := c.timeout
			if timeout <= 0 {
				timeout = 30 * time.Second
			}
			flightCtx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			flightReq := req.Clone(flightCtx)
			return c.executeWithRetryBuffered(flightCtx, flightReq)
		})

		select {
		case res := <-ch:
			if res.Err != nil {
				return nil, res.Err
			}
			cr := res.Val.(*cachedResponse)
			return cr.toHTTPResponse(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return c.executeWithRetry(ctx, req)
}

// CircuitBreaker returns the underlying gobreaker.TwoStepCircuitBreaker if configured.
func (c *Client) CircuitBreaker() *gobreaker.TwoStepCircuitBreaker {
	return c.circuitBreaker
}

// State returns the current CircuitBreaker state (StateClosed if no circuit breaker is configured).
func (c *Client) State() gobreaker.State {
	if c.circuitBreaker == nil {
		return gobreaker.StateClosed
	}
	return c.circuitBreaker.State()
}

func (c *Client) resolveURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if c.baseURL == "" {
		return path
	}
	return c.baseURL + "/" + strings.TrimLeft(path, "/")
}

func (c *Client) executeWithRetry(ctx context.Context, req *http.Request) (*http.Response, error) {
	// Acquire bulkhead concurrency slot for actual network flight
	if c.concurrencySem != nil {
		select {
		case c.concurrencySem <- struct{}{}:
			defer func() { <-c.concurrencySem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Prepare replayable body if request body is present
	if req.Body != nil && req.GetBody == nil {
		bodyBytes, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read request body for retries: %w", err)
		}
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	var lastResp *http.Response
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with full jitter:
			// backoff = min(maxInterval, baseInterval * 2^attempt) * rand(0, 1)
			multiplier := 1 << attempt
			if attempt > 30 {
				multiplier = 1 << 30
			}
			calc := c.minRetryInterval * time.Duration(multiplier)
			if calc > c.maxRetryInterval || calc <= 0 {
				calc = c.maxRetryInterval
			}
			jitter := cryptoFloat64()
			backoff := time.Duration(float64(calc) * jitter)

			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}

		attemptReq := req.Clone(ctx)
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("failed to restore request body on retry attempt %d: %w", attempt, err)
			}
			attemptReq.Body = body
		}

		var done func(success bool)
		if c.circuitBreaker != nil {
			var cbErr error
			done, cbErr = c.circuitBreaker.Allow()
			if cbErr != nil {
				return nil, cbErr
			}
		}

		resp, err := c.httpClient.Do(attemptReq)
		if err != nil {
			if done != nil {
				done(false)
			}
			lastErr = err

			// Never retry SSRF policy violations or context cancellations
			if isSSRFError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			continue
		}

		// Handle 5xx server errors
		if resp.StatusCode >= 500 {
			if done != nil {
				done(false)
			}
			lastResp = resp
			lastErr = fmt.Errorf("server returned error status: %d", resp.StatusCode)

			// Drain and close response body before retrying to prevent connection leaks
			if attempt < c.maxRetries {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				continue
			}
			return resp, nil
		}

		// Success or 4xx client errors (do not retry 4xx)
		if done != nil {
			done(true)
		}
		return resp, nil
	}

	if lastResp != nil {
		return lastResp, nil
	}
	return nil, lastErr
}

func (c *Client) executeWithRetryBuffered(ctx context.Context, req *http.Request) (*cachedResponse, error) {
	resp, err := c.executeWithRetry(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body for singleflight coalescing: %w", err)
	}

	return &cachedResponse{
		StatusCode:    resp.StatusCode,
		Status:        resp.Status,
		Proto:         resp.Proto,
		ProtoMajor:    resp.ProtoMajor,
		ProtoMinor:    resp.ProtoMinor,
		Header:        resp.Header.Clone(),
		Body:          bodyBytes,
		ContentLength: int64(len(bodyBytes)),
	}, nil
}

type cachedResponse struct {
	StatusCode    int
	Status        string
	Proto         string
	ProtoMajor    int
	ProtoMinor    int
	Header        http.Header
	Body          []byte
	ContentLength int64
}

func (cr *cachedResponse) toHTTPResponse() *http.Response {
	return &http.Response{
		StatusCode:    cr.StatusCode,
		Status:        cr.Status,
		Proto:         cr.Proto,
		ProtoMajor:    cr.ProtoMajor,
		ProtoMinor:    cr.ProtoMinor,
		Header:        cr.Header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(cr.Body)),
		ContentLength: cr.ContentLength,
	}
}

func cryptoFloat64() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0.5
	}
	v := binary.BigEndian.Uint64(b[:]) >> 11
	return float64(v) / float64(uint64(1)<<53)
}

func isSSRFError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "blocked private IP")
}
