package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/core/middleware"
)

const testCSRFSecret = "super-secret-hmac-key-for-csrf-tests"

func TestCSRFMiddleware_SafeMethods(t *testing.T) {
	router := gin.New()
	router.Use(middleware.CSRFMiddleware(testCSRFSecret, nil))
	router.GET("/hello", func(c *gin.Context) {
		c.String(http.StatusOK, "hello world")
	})

	// 1. First GET request seeds CSRF cookie and response header
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	headerToken := w.Header().Get(middleware.CSRFHeaderName)
	assert.NotEmpty(t, headerToken)
	assert.True(t, middleware.ValidateCSRFToken(testCSRFSecret, headerToken))

	cookies := w.Result().Cookies()
	var csrfCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == middleware.CSRFCookieName {
			csrfCookie = c
			break
		}
	}
	require.NotNil(t, csrfCookie)
	assert.Equal(t, headerToken, csrfCookie.Value)
	assert.True(t, csrfCookie.HttpOnly)
	assert.True(t, csrfCookie.Secure)
	assert.Equal(t, http.SameSiteLaxMode, csrfCookie.SameSite)

	// 2. Second GET with existing valid cookie preserves token
	req2 := httptest.NewRequest(http.MethodGet, "/hello", nil)
	req2.AddCookie(csrfCookie)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, csrfCookie.Value, w2.Header().Get(middleware.CSRFHeaderName))
}

func TestCSRFMiddleware_Mutative_Validation(t *testing.T) {
	router := gin.New()
	router.Use(middleware.CSRFMiddleware(testCSRFSecret, nil))
	router.POST("/action", func(c *gin.Context) {
		c.String(http.StatusOK, "action performed")
	})

	validToken, err := middleware.GenerateCSRFToken(testCSRFSecret)
	require.NoError(t, err)

	// 1. Missing cookie -> 403
	req := httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(`{}`))
	req.Header.Set(middleware.CSRFHeaderName, validToken)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "missing csrf cookie")

	// 2. Missing header -> 403
	req = httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: validToken, HttpOnly: true, Secure: true})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "missing csrf token header")

	// 3. Mismatch between header and cookie -> 403
	otherValidToken, err := middleware.GenerateCSRFToken(testCSRFSecret)
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: validToken, HttpOnly: true, Secure: true})
	req.Header.Set(middleware.CSRFHeaderName, otherValidToken)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "csrf token mismatch")

	// 4. Invalid HMAC signature (forged token) -> 403
	forgedToken, err := middleware.GenerateCSRFToken("wrong-secret-key")
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: forgedToken, HttpOnly: true, Secure: true})
	req.Header.Set(middleware.CSRFHeaderName, forgedToken)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "invalid csrf token signature")

	// 5. Valid double-submit token -> 200 OK
	req = httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: validToken, HttpOnly: true, Secure: true})
	req.Header.Set(middleware.CSRFHeaderName, validToken)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "action performed", w.Body.String())
}

func TestCSRFMiddleware_ExemptPaths(t *testing.T) {
	router := gin.New()
	exemptPaths := []string{
		"/",
		"/api/v1/auth/login",
		"/api/v1/webhook/*",
	}
	router.Use(middleware.CSRFMiddleware(testCSRFSecret, exemptPaths))

	router.POST("/", func(c *gin.Context) {
		c.String(http.StatusOK, "root ok")
	})
	router.POST("/login", func(c *gin.Context) {
		c.String(http.StatusOK, "login ok")
	})
	router.POST("/api/v1/auth/login", func(c *gin.Context) {
		c.String(http.StatusOK, "auth login ok")
	})
	router.POST("/api/v1/auth/login_admin", func(c *gin.Context) {
		c.String(http.StatusOK, "auth login admin ok")
	})
	router.POST("/api/v1/webhook/stripe", func(c *gin.Context) {
		c.String(http.StatusOK, "webhook stripe ok")
	})
	router.POST("/api/v1/webhook_leak", func(c *gin.Context) {
		c.String(http.StatusOK, "webhook leak ok")
	})
	router.POST("/api/v1/sensitive", func(c *gin.Context) {
		c.String(http.StatusOK, "sensitive ok")
	})

	// 1. Root exemption "/" exempts "/" exactly
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "root ok", w.Body.String())

	// 2. Root exemption "/" does NOT exempt other paths like "/login"
	req = httptest.NewRequest(http.MethodPost, "/login", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// 3. Exact exempt path bypasses CSRF
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "auth login ok", w.Body.String())

	// 4. Exact exempt path does NOT exempt prefix extensions like "/api/v1/auth/login_admin"
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login_admin", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// 5. Wildcard exempt path "/api/v1/webhook/*" bypasses "/api/v1/webhook/stripe"
	req = httptest.NewRequest(http.MethodPost, "/api/v1/webhook/stripe", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "webhook stripe ok", w.Body.String())

	// 6. Wildcard exempt path "/api/v1/webhook/*" does NOT match "/api/v1/webhook_leak"
	req = httptest.NewRequest(http.MethodPost, "/api/v1/webhook_leak", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// 7. Non-exempt path requires CSRF
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sensitive", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestCSRFTokenHelpers(t *testing.T) {
	// Empty secret fails
	_, err := middleware.GenerateCSRFToken("")
	assert.Error(t, err)

	assert.False(t, middleware.ValidateCSRFToken("", "token"))
	assert.False(t, middleware.ValidateCSRFToken(testCSRFSecret, ""))
	assert.False(t, middleware.ValidateCSRFToken(testCSRFSecret, "malformed-no-dot"))
	assert.False(t, middleware.ValidateCSRFToken(testCSRFSecret, "part1.part2.part3"))

	token, err := middleware.GenerateCSRFToken(testCSRFSecret)
	require.NoError(t, err)
	assert.True(t, middleware.ValidateCSRFToken(testCSRFSecret, token))
}

func TestCSRF_BoundToSession(t *testing.T) {
	secret := "secret-key"
	sessionA := "session-123"
	sessionB := "session-456"

	token, err := middleware.GenerateBoundCSRFToken(secret, sessionA)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	assert.True(t, middleware.ValidateBoundCSRFToken(secret, sessionA, token))
	assert.False(t, middleware.ValidateBoundCSRFToken(secret, sessionB, token))
	assert.False(t, middleware.ValidateBoundCSRFToken("wrong-secret", sessionA, token))
	assert.False(t, middleware.ValidateBoundCSRFToken(secret, "", token))
	assert.False(t, middleware.ValidateBoundCSRFToken(secret, sessionA, ""))

	// Unbound tokens must not pass bound validation
	unbound, err := middleware.GenerateCSRFToken(secret)
	require.NoError(t, err)
	assert.False(t, middleware.ValidateBoundCSRFToken(secret, sessionA, unbound))

	// Empty secret or empty sessionID must fail generation
	_, err = middleware.GenerateBoundCSRFToken("", sessionA)
	assert.Error(t, err)
	_, err = middleware.GenerateBoundCSRFToken(secret, "")
	assert.Error(t, err)
}
