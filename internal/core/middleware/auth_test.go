package middleware_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/core/auth"
	"go_backend_boilerplate/internal/core/middleware"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func generateKeys() (string, string) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	return string(privPEM), string(pubPEM)
}

type mockTokenValidator struct {
	validateFunc func(ctx context.Context, tokenStr string) (*auth.Claims, error)
}

func (m *mockTokenValidator) ValidateAccessToken(ctx context.Context, tokenStr string) (*auth.Claims, error) {
	return m.validateFunc(ctx, tokenStr)
}

func TestAuthMiddleware_ValidToken(t *testing.T) {
	privPEM, pubPEM := generateKeys()
	mgr, err := auth.NewJWTTokenManager(privPEM, pubPEM, 15*time.Minute, 7*24*time.Hour, nil)
	require.NoError(t, err)

	pair, err := mgr.GenerateTokenPair("user_42", []string{"editor", "admin"})
	require.NoError(t, err)

	router := gin.New()
	router.Use(middleware.AuthMiddleware(mgr))
	router.GET("/protected", func(c *gin.Context) {
		user, ok := middleware.GetUser(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no user"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"user_id": user.UserID, "roles": user.Roles})
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "user_42")
	assert.Contains(t, w.Body.String(), "admin")
}

func TestAuthMiddleware_MissingHeader(t *testing.T) {
	validator := &mockTokenValidator{
		validateFunc: func(ctx context.Context, tokenStr string) (*auth.Claims, error) {
			return nil, nil
		},
	}

	router := gin.New()
	router.Use(middleware.AuthMiddleware(validator))
	router.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "missing authorization header")
}

func TestAuthMiddleware_InvalidFormat(t *testing.T) {
	validator := &mockTokenValidator{
		validateFunc: func(ctx context.Context, tokenStr string) (*auth.Claims, error) {
			return nil, nil
		},
	}

	router := gin.New()
	router.Use(middleware.AuthMiddleware(validator))
	router.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	testCases := []string{
		"Basic dXNlcjpwYXNz",
		"Bearer",
		"Bearer   ",
		"Token abc123",
	}

	for _, headerVal := range testCases {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", headerVal)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "invalid authorization header format")
	}
}

func TestAuthMiddleware_RevokedToken(t *testing.T) {
	validator := &mockTokenValidator{
		validateFunc: func(ctx context.Context, tokenStr string) (*auth.Claims, error) {
			return nil, auth.ErrTokenRevoked
		},
	}

	router := gin.New()
	router.Use(middleware.AuthMiddleware(validator))
	router.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer revoked-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "token has been revoked")
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	validator := &mockTokenValidator{
		validateFunc: func(ctx context.Context, tokenStr string) (*auth.Claims, error) {
			return nil, auth.ErrInvalidToken
		},
	}

	router := gin.New()
	router.Use(middleware.AuthMiddleware(validator))
	router.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer invalid-signature-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "invalid or expired token")
}

func TestAuthMiddleware_RedisInfraFailureReturns503(t *testing.T) {
	validator := &mockTokenValidator{
		validateFunc: func(ctx context.Context, tokenStr string) (*auth.Claims, error) {
			return nil, auth.ErrRevocationCheckFailed
		},
	}

	router := gin.New()
	router.Use(middleware.AuthMiddleware(validator))
	router.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer valid-looking-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "authentication service unavailable")
}

func TestRequireRoles(t *testing.T) {
	router := gin.New()

	// Helper route setting custom claims
	router.GET("/admin-only", func(c *gin.Context) {
		role := c.Query("role")
		if role != "" {
			c.Set("user", &auth.Claims{
				UserID: "usr_test",
				Roles:  []string{role},
			})
		}
	}, middleware.RequireRoles("admin"), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"access": "granted"})
	})

	// 1. Authorized role
	req := httptest.NewRequest(http.MethodGet, "/admin-only?role=admin", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "granted")

	// 2. Forbidden role
	req = httptest.NewRequest(http.MethodGet, "/admin-only?role=viewer", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "insufficient permissions")

	// 3. Unauthenticated (no user set)
	req = httptest.NewRequest(http.MethodGet, "/admin-only", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMustGetUser_PanicWhenAbsent(t *testing.T) {
	router := gin.New()
	router.GET("/panic", func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				c.String(http.StatusInternalServerError, "recovered panic")
			}
		}()
		_ = middleware.MustGetUser(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "recovered panic", w.Body.String())
}
