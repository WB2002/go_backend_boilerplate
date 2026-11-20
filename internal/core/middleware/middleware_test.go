package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/core/middleware"
	"go_backend_boilerplate/internal/platform/logger"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestRequestDeadlineTimeout(t *testing.T) {
	router := gin.New()
	router.Use(middleware.RequestDeadline(50*time.Millisecond, 200*time.Millisecond))

	router.GET("/slow", func(c *gin.Context) {
		select {
		case <-time.After(100 * time.Millisecond):
			c.String(http.StatusOK, "done")
		case <-c.Request.Context().Done():
			c.AbortWithStatus(http.StatusGatewayTimeout)
		}
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/slow", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusGatewayTimeout, w.Code)
}

func TestRequestDeadlineHeaderOverride(t *testing.T) {
	router := gin.New()
	router.Use(middleware.RequestDeadline(500*time.Millisecond, 1000*time.Millisecond))

	router.GET("/custom-timeout", func(c *gin.Context) {
		select {
		case <-time.After(150 * time.Millisecond):
			c.String(http.StatusOK, "done")
		case <-c.Request.Context().Done():
			c.AbortWithStatus(http.StatusGatewayTimeout)
		}
	})

	// Override with 50ms via X-Request-Timeout header (should timeout before 150ms)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/custom-timeout", nil)
	req.Header.Set("X-Request-Timeout", "50ms")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusGatewayTimeout, w.Code)
}

func TestRequestDeadlineCappedAtMax(t *testing.T) {
	router := gin.New()
	// default 50ms, max 100ms
	router.Use(middleware.RequestDeadline(50*time.Millisecond, 100*time.Millisecond))

	router.GET("/capped", func(c *gin.Context) {
		deadline, ok := c.Request.Context().Deadline()
		require.True(t, ok)
		remaining := time.Until(deadline)
		// Should be capped around 100ms, definitely less than 500ms
		assert.LessOrEqual(t, remaining, 120*time.Millisecond)
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/capped", nil)
	req.Header.Set("X-Request-Timeout", "500ms") // Request 500ms, but max is 100ms
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRequestDeadlineDefaultFallback(t *testing.T) {
	router := gin.New()
	// Passing <= 0 must default to safe 30s
	router.Use(middleware.RequestDeadline(0, 0))

	router.GET("/default-deadline", func(c *gin.Context) {
		deadline, ok := c.Request.Context().Deadline()
		require.True(t, ok)
		assert.Greater(t, time.Until(deadline), 25*time.Second)
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/default-deadline", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGzipCompressionThreshold(t *testing.T) {
	router := gin.New()
	router.Use(middleware.GzipCompression(1024))
	router.GET("/large", func(c *gin.Context) {
		c.String(http.StatusOK, string(make([]byte, 2048)))
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/large", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	router.ServeHTTP(w, req)

	assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
	assert.Equal(t, "Accept-Encoding", w.Header().Get("Vary"))
}

func TestGzipCompressionBelowThreshold(t *testing.T) {
	router := gin.New()
	router.Use(middleware.GzipCompression(1024))
	router.GET("/small", func(c *gin.Context) {
		c.String(http.StatusOK, "small response")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/small", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	router.ServeHTTP(w, req)

	assert.Empty(t, w.Header().Get("Content-Encoding"))
	assert.Equal(t, "small response", w.Body.String())
}

func TestGzipCompressionResponseWriterMethods(t *testing.T) {
	router := gin.New()
	router.Use(middleware.GzipCompression(1024))

	var statusBeforeFlush int
	var writtenBeforeFlush bool
	var sizeBeforeFlush int

	router.GET("/buffered-state", func(c *gin.Context) {
		c.Status(http.StatusCreated)
		statusBeforeFlush = c.Writer.Status()
		writtenBeforeFlush = c.Writer.Written()

		_, _ = c.Writer.WriteString("buffered")
		sizeBeforeFlush = c.Writer.Size()
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/buffered-state", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, statusBeforeFlush)
	assert.True(t, writtenBeforeFlush)
	assert.Equal(t, len("buffered"), sizeBeforeFlush)
	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "buffered", w.Body.String())
}

func TestGzipCompressionWithoutAcceptEncoding(t *testing.T) {
	router := gin.New()
	router.Use(middleware.GzipCompression(1024))
	router.GET("/large-no-gzip", func(c *gin.Context) {
		c.String(http.StatusOK, string(make([]byte, 2048)))
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/large-no-gzip", nil)
	router.ServeHTTP(w, req)

	assert.Empty(t, w.Header().Get("Content-Encoding"))
}

func TestRequestID(t *testing.T) {
	router := gin.New()
	router.Use(middleware.RequestID())

	var capturedCtxID string
	var capturedStdCtxID string
	router.GET("/ping", func(c *gin.Context) {
		capturedCtxID = middleware.GetRequestID(c)
		capturedStdCtxID = middleware.GetRequestIDFromContext(c.Request.Context())
		c.String(http.StatusOK, "pong")
	})

	// Case 1: Generates new UUID when header is empty
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodGet, "/ping", nil)
	router.ServeHTTP(w1, req1)

	respID1 := w1.Header().Get("X-Request-ID")
	assert.NotEmpty(t, respID1)
	assert.Equal(t, respID1, capturedCtxID)
	assert.Equal(t, respID1, capturedStdCtxID)

	// Case 2: Preserves client-supplied X-Request-ID
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, "/ping", nil)
	req2.Header.Set("X-Request-ID", "custom-client-id-123")
	router.ServeHTTP(w2, req2)

	assert.Equal(t, "custom-client-id-123", w2.Header().Get("X-Request-ID"))
	assert.Equal(t, "custom-client-id-123", capturedCtxID)
	assert.Equal(t, "custom-client-id-123", capturedStdCtxID)
}

func TestGetRequestIDFromContext(t *testing.T) {
	assert.Empty(t, middleware.GetRequestIDFromContext(context.Background()))
	assert.Empty(t, middleware.GetRequestIDFromContext(context.TODO()))
}

func TestSecurityHeaders(t *testing.T) {
	router := gin.New()
	router.Use(middleware.SecurityHeaders())
	router.GET("/test-security", func(c *gin.Context) {
		c.String(http.StatusOK, "secure")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test-security", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Equal(t, "default-src 'self'", w.Header().Get("Content-Security-Policy"))
	assert.Contains(t, w.Header().Get("Strict-Transport-Security"), "max-age=31536000")
}

func TestCORS(t *testing.T) {
	allowedOrigins := []string{"https://example.com", "https://app.example.com"}
	router := gin.New()
	router.Use(middleware.CORS(allowedOrigins))

	router.POST("/data", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// Case 1: Preflight OPTIONS request for allowed origin
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodOptions, "/data", nil)
	req1.Header.Set("Origin", "https://example.com")
	req1.Header.Set("Access-Control-Request-Method", "POST")
	router.ServeHTTP(w1, req1)

	assert.Equal(t, http.StatusNoContent, w1.Code)
	assert.Equal(t, "https://example.com", w1.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", w1.Header().Get("Access-Control-Allow-Credentials"))
	assert.Equal(t, "Origin", w1.Header().Get("Vary"))
	assert.Contains(t, w1.Header().Get("Access-Control-Allow-Methods"), "POST")

	// Case 2: Standard request with allowed origin
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/data", nil)
	req2.Header.Set("Origin", "https://app.example.com")
	router.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "https://app.example.com", w2.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", w2.Header().Get("Access-Control-Allow-Credentials"))
	assert.Equal(t, "Origin", w2.Header().Get("Vary"))

	// Case 3: Disallowed origin
	w3 := httptest.NewRecorder()
	req3, _ := http.NewRequest(http.MethodPost, "/data", nil)
	req3.Header.Set("Origin", "https://evil.com")
	router.ServeHTTP(w3, req3)

	assert.Empty(t, w3.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w3.Header().Get("Access-Control-Allow-Credentials"))
}

func TestCORSWildcardDoesNotReflectCredentials(t *testing.T) {
	router := gin.New()
	router.Use(middleware.CORS([]string{"*"}))

	router.GET("/public", func(c *gin.Context) {
		c.String(http.StatusOK, "public")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("Origin", "https://untrusted-caller.com")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"), "Wildcard CORS must never set Allow-Credentials")
}

func TestRecovery(t *testing.T) {
	var buf bytes.Buffer
	testLog := logger.NewWithWriter(&buf, "test")

	router := gin.New()
	router.Use(middleware.Recovery(testLog))

	router.GET("/panic", func(c *gin.Context) {
		panic("database connection failure")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/panic", nil)
	router.ServeHTTP(w, req)

	// Verifies standard 500 status and that error payload does NOT leak stack trace
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var body map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.Equal(t, "Internal Server Error", body["error"])
	assert.Nil(t, body["stack"])

	// Verifies panic was logged to structured logger
	logOutput := buf.String()
	assert.Contains(t, logOutput, "panic recovered")
	assert.Contains(t, logOutput, "database connection failure")
}

func TestStructuredLoggerPIIMasking(t *testing.T) {
	var buf bytes.Buffer
	testLog := logger.NewWithWriter(&buf, "test")

	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.StructuredLogger(testLog))

	router.POST("/user", func(c *gin.Context) {
		c.String(http.StatusOK, "created")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/user?email=secret@test.com&token=secret123&api_key=mykey456", bytes.NewBufferString(`{"password":"my-super-secret-password"}`))
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.dummytoken")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	logOutput := buf.String()
	assert.Contains(t, logOutput, "HTTP Request")
	// Authorization token must be masked
	assert.NotContains(t, logOutput, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.dummytoken")
	// Email must be masked in query or log
	assert.NotContains(t, logOutput, "secret@test.com")
	// Query parameters must be masked
	assert.NotContains(t, logOutput, "secret123")
	assert.NotContains(t, logOutput, "mykey456")
	assert.Contains(t, logOutput, "token=[REDACTED]")
	assert.Contains(t, logOutput, "api_key=[REDACTED]")
}
