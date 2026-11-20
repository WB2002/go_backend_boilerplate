package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// CSRFCookieName is the standard cookie name for CSRF tokens.
	CSRFCookieName = "_csrf"
	// CSRFHeaderName is the standard header name for CSRF tokens.
	CSRFHeaderName = "X-CSRF-Token"
)

// GenerateCSRFToken creates an HMAC-SHA256 signed CSRF token using random entropy.
func GenerateCSRFToken(secret string) (string, error) {
	if secret == "" {
		return "", errors.New("csrf secret cannot be empty")
	}

	rawHex, err := randomCSRFHex()
	if err != nil {
		return "", err
	}

	return rawHex + "." + signCSRFPayload(secret, rawHex), nil
}

// GenerateBoundCSRFToken creates an HMAC-SHA256 signed CSRF token bound to a session or user identifier.
func GenerateBoundCSRFToken(secret string, sessionID string) (string, error) {
	if secret == "" {
		return "", errors.New("csrf secret cannot be empty")
	}
	if sessionID == "" {
		return "", errors.New("csrf session id cannot be empty")
	}

	rawHex, err := randomCSRFHex()
	if err != nil {
		return "", err
	}

	return rawHex + "." + signCSRFPayload(secret, sessionID+":"+rawHex), nil
}

// ValidateCSRFToken checks whether the given token string has a valid HMAC-SHA256 signature.
func ValidateCSRFToken(secret string, tokenStr string) bool {
	if secret == "" || tokenStr == "" {
		return false
	}
	return verifyCSRFToken(secret, tokenStr, func(rawHex string) string {
		return rawHex
	})
}

// ValidateBoundCSRFToken checks whether the given token string has a valid HMAC-SHA256 signature
// bound to the specified session or user identifier.
func ValidateBoundCSRFToken(secret string, sessionID string, tokenStr string) bool {
	if secret == "" || sessionID == "" || tokenStr == "" {
		return false
	}
	return verifyCSRFToken(secret, tokenStr, func(rawHex string) string {
		return sessionID + ":" + rawHex
	})
}

func randomCSRFHex() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func signCSRFPayload(secret string, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyCSRFToken(secret string, tokenStr string, payloadFn func(rawHex string) string) bool {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}

	expectedSig := signCSRFPayload(secret, payloadFn(parts[0]))
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expectedSig)) == 1
}

// CSRFMiddleware implements the Double-Submit Cookie CSRF pattern for mutative HTTP methods.
// Mutative methods (POST, PUT, DELETE, PATCH) require matching cookie and X-CSRF-Token headers
// with valid HMAC signatures, unless the request path is exempt.
func CSRFMiddleware(secret string, exemptPaths []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		path := c.Request.URL.Path

		// Safe idempotent methods only seed/refresh tokens
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions || method == "TRACE" {
			cookie, err := c.Request.Cookie(CSRFCookieName)
			if err == nil && ValidateCSRFToken(secret, cookie.Value) {
				c.Header(CSRFHeaderName, cookie.Value)
				c.Next()
				return
			}

			// Generate and seed fresh cookie & header
			token, err := GenerateCSRFToken(secret)
			if err == nil {
				isSecure := c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https"
				http.SetCookie(c.Writer, &http.Cookie{
					Name:     CSRFCookieName,
					Value:    token,
					Path:     "/",
					HttpOnly: false,
					SameSite: http.SameSiteLaxMode,
					Secure:   isSecure,
				})
				c.Header(CSRFHeaderName, token)
			}
			c.Next()
			return
		}

		// Check if request path matches exempt paths
		for _, exempt := range exemptPaths {
			if exempt == "" {
				continue
			}
			if strings.HasSuffix(exempt, "/*") {
				prefix := strings.TrimSuffix(exempt, "*")
				if strings.HasPrefix(path, prefix) {
					c.Next()
					return
				}
			} else {
				normPath := strings.TrimSuffix(path, "/")
				normExempt := strings.TrimSuffix(exempt, "/")
				if normPath == normExempt {
					c.Next()
					return
				}
			}
		}

		// Mutative method: require cookie
		cookie, err := c.Request.Cookie(CSRFCookieName)
		if err != nil || cookie.Value == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "missing csrf cookie"})
			return
		}

		// Require header
		headerToken := c.GetHeader(CSRFHeaderName)
		if headerToken == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "missing csrf token header"})
			return
		}

		// Double-submit token equality check
		if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(headerToken)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "csrf token mismatch"})
			return
		}

		// Verify cryptographic HMAC signature
		if !ValidateCSRFToken(secret, cookie.Value) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "invalid csrf token signature"})
			return
		}

		c.Next()
	}
}
