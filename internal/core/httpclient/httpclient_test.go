package httpclient_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sony/gobreaker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/core/httpclient"
	"go_backend_boilerplate/internal/core/middleware"
)

func TestSSRFGuardBlocksPrivateIPs(t *testing.T) {
	client := httpclient.NewBuilder().
		WithSSRFProtection(true).
		Build()

	ctx := context.Background()

	// Loopback IPv4
	_, err := client.Get(ctx, "http://127.0.0.1:8080/secret")
	assert.ErrorContains(t, err, "blocked private IP")

	// AWS Metadata / Link-local
	_, err = client.Get(ctx, "http://169.254.169.254/latest/meta-data")
	assert.ErrorContains(t, err, "blocked private IP")

	// Private Subnet RFC 1918 (10.0.0.0/8)
	_, err = client.Get(ctx, "http://10.0.0.5/api")
	assert.ErrorContains(t, err, "blocked private IP")

	// Private Subnet RFC 1918 (172.16.0.0/12)
	_, err = client.Get(ctx, "http://172.16.0.1/admin")
	assert.ErrorContains(t, err, "blocked private IP")

	// Private Subnet RFC 1918 (192.168.0.0/16)
	_, err = client.Get(ctx, "http://192.168.1.1/router")
	assert.ErrorContains(t, err, "blocked private IP")

	// CGNAT (100.64.0.0/10)
	_, err = client.Get(ctx, "http://100.64.0.1/internal")
	assert.ErrorContains(t, err, "blocked private IP")

	// Unspecified address
	_, err = client.Get(ctx, "http://0.0.0.0:8080")
	assert.ErrorContains(t, err, "blocked private IP")

	// Localhost hostname resolution
	_, err = client.Get(ctx, "http://localhost:8080/internal")
	assert.ErrorContains(t, err, "blocked private IP")
}

func TestSSRFGuardToggleEnvVar(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	// When SSRF protection is enabled and env var is not set, dialing local test server is blocked
	clientBlocked := httpclient.NewBuilder().WithSSRFProtection(true).Build()
	_, err := clientBlocked.Get(context.Background(), server.URL)
	assert.ErrorContains(t, err, "blocked private IP")

	// When HTTP_CLIENT_ALLOW_PRIVATE_URLS is set to true, dialing local server is allowed
	t.Setenv("HTTP_CLIENT_ALLOW_PRIVATE_URLS", "true")
	clientAllowed := httpclient.NewBuilder().WithSSRFProtection(true).Build()
	resp, err := clientAllowed.Get(context.Background(), server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// When WithSSRFProtection(false) is configured, private URLs are also allowed
	t.Setenv("HTTP_CLIENT_ALLOW_PRIVATE_URLS", "false")
	clientExplicitAllowed := httpclient.NewBuilder().WithSSRFProtection(false).Build()
	resp2, err := clientExplicitAllowed.Get(context.Background(), server.URL)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
}

func TestRetryWithBackoffAndJitter(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := attempts.Add(1)
		if current < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"transient failure"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"recovered"}`))
	}))
	defer server.Close()

	client := httpclient.NewBuilder().
		WithSSRFProtection(false).
		WithRetry(3, 10*time.Millisecond, 50*time.Millisecond).
		Build()

	resp, err := client.Get(context.Background(), server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(3), attempts.Load(), "should have succeeded on attempt 3 after 2 retries")
}

func TestRetryAvoids4xxErrors(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer server.Close()

	client := httpclient.NewBuilder().
		WithSSRFProtection(false).
		WithRetry(3, 10*time.Millisecond, 50*time.Millisecond).
		Build()

	resp, err := client.Get(context.Background(), server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, int32(1), attempts.Load(), "4xx errors must not trigger retries")
}

func TestRetryPOSTWithBodyReplay(t *testing.T) {
	var attempts atomic.Int32
	var capturedBodies []string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		capturedBodies = append(capturedBodies, string(body))
		mu.Unlock()

		current := attempts.Add(1)
		if current < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	defer server.Close()

	client := httpclient.NewBuilder().
		WithSSRFProtection(false).
		WithRetry(2, 5*time.Millisecond, 20*time.Millisecond).
		Build()

	payload := map[string]string{"username": "testuser", "action": "sync"}
	resp, err := client.Post(context.Background(), server.URL, payload)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, int32(2), attempts.Load())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, capturedBodies, 2)
	assert.Contains(t, capturedBodies[0], "testuser")
	assert.Equal(t, capturedBodies[0], capturedBodies[1], "request body must be replayed identically on retry")
}

func TestCircuitBreakerTripsAndRecovers(t *testing.T) {
	var failServer atomic.Bool
	failServer.Store(true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failServer.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Configure circuit breaker with failure ratio 0.5, trip after 3 failures, timeout 100ms
	client := httpclient.NewBuilder().
		WithSSRFProtection(false).
		WithCircuitBreaker("test-service", 0.5, 100*time.Millisecond).
		Build()

	// Initial state: Closed
	assert.Equal(t, gobreaker.StateClosed, client.State())

	// Cause consecutive failures to trip circuit
	for i := 0; i < 4; i++ {
		_, _ = client.Get(context.Background(), server.URL)
	}

	// Breaker should now be Open
	assert.Equal(t, gobreaker.StateOpen, client.State())

	// Requests while Open must fail-fast without calling the server
	_, err := client.Get(context.Background(), server.URL)
	assert.ErrorIs(t, err, gobreaker.ErrOpenState)

	// Wait for open timeout to transition into Half-Open
	time.Sleep(120 * time.Millisecond)

	// Allow server to succeed
	failServer.Store(false)

	// Next request in Half-Open state succeeds and closes the circuit
	resp, err := client.Get(context.Background(), server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Circuit breaker is closed again
	assert.Equal(t, gobreaker.StateClosed, client.State())
}

func TestSingleflightCoalescesDuplicateRequests(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		time.Sleep(50 * time.Millisecond) // Simulate slow backend call
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"coalesced":true}`))
	}))
	defer server.Close()

	client := httpclient.NewBuilder().
		WithSSRFProtection(false).
		WithSingleflight(true).
		Build()

	const concurrentCallers = 5
	var wg sync.WaitGroup
	wg.Add(concurrentCallers)

	bodies := make([]string, concurrentCallers)
	for i := 0; i < concurrentCallers; i++ {
		idx := i
		go func() {
			defer wg.Done()
			resp, err := client.Get(context.Background(), server.URL)
			if assert.NoError(t, err) {
				defer resp.Body.Close()
				data, readErr := io.ReadAll(resp.Body)
				if assert.NoError(t, readErr) {
					bodies[idx] = string(data)
				}
			}
		}()
	}

	wg.Wait()

	// Backend server must have received only 1 request despite 5 concurrent callers
	assert.Equal(t, int32(1), requestCount.Load(), "singleflight must coalesce concurrent identical GET requests")

	// All 5 callers must have received identical valid response bodies
	for i := 0; i < concurrentCallers; i++ {
		assert.Equal(t, `{"coalesced":true}`, bodies[i])
	}
}

func TestConcurrencyLimitBulkhead(t *testing.T) {
	var inFlight atomic.Int32
	var maxObservedInFlight atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := inFlight.Add(1)
		defer inFlight.Add(-1)

		for {
			oldMax := maxObservedInFlight.Load()
			if current <= oldMax || maxObservedInFlight.CompareAndSwap(oldMax, current) {
				break
			}
		}

		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	const maxBulkhead = 2
	client := httpclient.NewBuilder().
		WithSSRFProtection(false).
		WithConcurrencyLimit(maxBulkhead).
		Build()

	const totalRequests = 6
	var wg sync.WaitGroup
	wg.Add(totalRequests)

	for i := 0; i < totalRequests; i++ {
		go func() {
			defer wg.Done()
			resp, err := client.Get(context.Background(), server.URL)
			if assert.NoError(t, err) {
				_ = resp.Body.Close()
			}
		}()
	}

	wg.Wait()

	assert.LessOrEqual(t, maxObservedInFlight.Load(), int32(maxBulkhead), "concurrency limit must bound in-flight requests")
}

func TestContextCancellationAndDeadlinePropagation(t *testing.T) {
	var receivedTimeoutHeader string
	var receivedReqIDHeader string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedTimeoutHeader = r.Header.Get("X-Request-Timeout")
		receivedReqIDHeader = r.Header.Get("X-Request-ID")
		mu.Unlock()

		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := httpclient.NewBuilder().
		WithSSRFProtection(false).
		WithDeadlinePropagation(true).
		Build()

	// 1. Verify header propagation with context deadline and request ID
	ctxTimeout, cancelTimeout := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelTimeout()
	ctxWithReqID := context.WithValue(ctxTimeout, middleware.RequestIDContextKey, "trace-abc-123")

	resp, err := client.Get(ctxWithReqID, server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	mu.Lock()
	assert.NotEmpty(t, receivedTimeoutHeader, "X-Request-Timeout should be propagated")
	assert.Equal(t, "trace-abc-123", receivedReqIDHeader, "X-Request-ID should be propagated")
	mu.Unlock()

	// 2. Verify cancellation halts request immediately
	ctxCancelled, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	_, cancelErr := client.Get(ctxCancelled, server.URL)
	assert.ErrorIs(t, cancelErr, context.Canceled)
}

func TestBaseURLAndPathResolution(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := httpclient.NewBuilder().
		WithSSRFProtection(false).
		WithBaseURL(server.URL + "/api/v1").
		Build()

	// Relative path
	resp, err := client.Get(context.Background(), "/users/list")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "/api/v1/users/list", requestedPath)

	// Relative path without leading slash
	resp2, err := client.Get(context.Background(), "profile")
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, "/api/v1/profile", requestedPath)
}

func TestSingleflightInitiatorCancellationDoesNotCancelFollowers(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		time.Sleep(60 * time.Millisecond) // slow flight
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"follower":"success"}`))
	}))
	defer server.Close()

	client := httpclient.NewBuilder().
		WithSSRFProtection(false).
		WithSingleflight(true).
		Build()

	var wg sync.WaitGroup
	wg.Add(2)

	var initiatorErr error
	var followerRespBody string
	var followerErr error

	// Caller 1 (Initiator) with short timeout that will cancel early
	ctxInitiator, cancelInitiator := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelInitiator()

	// Caller 2 (Follower) with generous timeout
	ctxFollower, cancelFollower := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancelFollower()

	go func() {
		defer wg.Done()
		_, initiatorErr = client.Get(ctxInitiator, server.URL)
	}()

	// Ensure initiator starts flight first
	time.Sleep(5 * time.Millisecond)

	go func() {
		defer wg.Done()
		resp, err := client.Get(ctxFollower, server.URL)
		followerErr = err
		if err == nil {
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			followerRespBody = string(b)
		}
	}()

	wg.Wait()

	// Initiator must have failed due to context deadline exceeded
	assert.ErrorIs(t, initiatorErr, context.DeadlineExceeded)

	// Follower must succeed because singleflight is decoupled from initiator context
	require.NoError(t, followerErr)
	assert.Equal(t, `{"follower":"success"}`, followerRespBody)
	assert.Equal(t, int32(1), requestCount.Load(), "only 1 request made to backend")
}

func TestBulkheadSlotAcquiredAfterSingleflight(t *testing.T) {
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		curr := inFlight.Add(1)
		defer inFlight.Add(-1)

		for {
			old := maxInFlight.Load()
			if curr <= old || maxInFlight.CompareAndSwap(old, curr) {
				break
			}
		}

		time.Sleep(40 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Concurrency limit of 1, but singleflight enabled with 5 concurrent requests for same URL
	client := httpclient.NewBuilder().
		WithSSRFProtection(false).
		WithConcurrencyLimit(1).
		WithSingleflight(true).
		Build()

	const concurrentCallers = 5
	var wg sync.WaitGroup
	wg.Add(concurrentCallers)

	for i := 0; i < concurrentCallers; i++ {
		go func() {
			defer wg.Done()
			resp, err := client.Get(context.Background(), server.URL)
			if assert.NoError(t, err) {
				_ = resp.Body.Close()
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, int32(1), maxInFlight.Load(), "exactly 1 flight reached server and 1 slot consumed")
}
