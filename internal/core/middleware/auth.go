package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"go_backend_boilerplate/internal/core/auth"
)

// TokenValidator defines the minimal contract required by AuthMiddleware.
type TokenValidator interface {
	ValidateAccessToken(ctx context.Context, tokenStr string) (*auth.Claims, error)
}

// AuthMiddleware extracts Bearer tokens from the Authorization header, validates signatures,
// enforces Redis revocation checks via TokenValidator, and sets claims in the Gin context.
func AuthMiddleware(tokenManager TokenValidator) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization header format"})
			return
		}

		tokenStr := strings.TrimSpace(parts[1])
		claims, err := tokenManager.ValidateAccessToken(c.Request.Context(), tokenStr)
		if err != nil {
			if errors.Is(err, auth.ErrRevocationCheckFailed) {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "authentication service unavailable"})
				return
			}
			if errors.Is(err, auth.ErrTokenRevoked) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token has been revoked"})
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		c.Set("user", claims)
		c.Next()
	}
}

// GetUser retrieves the authenticated user claims from the Gin context.
func GetUser(c *gin.Context) (*auth.Claims, bool) {
	val, exists := c.Get("user")
	if !exists {
		return nil, false
	}
	claims, ok := val.(*auth.Claims)
	return claims, ok
}

// MustGetUser retrieves the authenticated user claims, panicking if absent.
func MustGetUser(c *gin.Context) *auth.Claims {
	claims, ok := GetUser(c)
	if !ok {
		panic("user claims not found in context")
	}
	return claims
}

// RequireRoles enforces role-based access control against the user claims in context.
func RequireRoles(roles ...string) gin.HandlerFunc {
	required := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		required[r] = struct{}{}
	}

	return func(c *gin.Context) {
		claims, ok := GetUser(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		hasRole := false
		for _, role := range claims.Roles {
			if _, exists := required[role]; exists {
				hasRole = true
				break
			}
		}

		if !hasRole {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden: insufficient permissions"})
			return
		}

		c.Next()
	}
}
