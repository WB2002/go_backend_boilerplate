package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORS manages Cross-Origin Resource Sharing headers, preflight OPTIONS requests,
// allowed origins, credentials, and allowed/exposed headers.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowAll := false
	originsMap := make(map[string]bool)

	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o == "*" {
			allowAll = true
		} else if o != "" {
			originsMap[o] = true
		}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		if allowAll {
			// When wildcard * is configured, never reflect arbitrary origins with credentials
			c.Header("Access-Control-Allow-Origin", "*")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD")
			c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, X-Request-ID, X-Request-Timeout")
			c.Header("Access-Control-Expose-Headers", "Content-Length, X-Request-ID")
			c.Header("Access-Control-Max-Age", "86400")
		} else if origin != "" && originsMap[origin] {
			// Only allow credentials and echo origin when explicitly matched
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD")
			c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, X-Request-ID, X-Request-Timeout")
			c.Header("Access-Control-Expose-Headers", "Content-Length, X-Request-ID")
			c.Header("Access-Control-Max-Age", "86400")
			c.Header("Vary", "Origin")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
