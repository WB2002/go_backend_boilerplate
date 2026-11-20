package middleware

import (
	"net"

	"github.com/gin-gonic/gin"
)

const (
	// ContextKeyClientIP is the context key for the caller's IP address.
	ContextKeyClientIP = "audit.client_ip"
	// ContextKeyUserAgent is the context key for the caller's User-Agent header.
	ContextKeyUserAgent = "audit.user_agent"
)

// AuditContext middleware extracts client IP and User-Agent headers and sets them on the gin context.
func AuditContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := ExtractClientIP(c)
		ua := c.GetHeader("User-Agent")

		c.Set(ContextKeyClientIP, ip)
		c.Set(ContextKeyUserAgent, ua)

		c.Next()
	}
}

// ExtractClientIP retrieves the client's IP via Gin's trusted-proxy-aware ClientIP()
// and validates it with net.ParseIP, falling back to "127.0.0.1" if invalid.
func ExtractClientIP(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return "127.0.0.1"
	}
	ipStr := c.ClientIP()
	if ip := net.ParseIP(ipStr); ip != nil {
		return ip.String()
	}
	return "127.0.0.1"
}

// GetClientIP gets the client IP from context or extracts it if not set.
func GetClientIP(c *gin.Context) string {
	if val, exists := c.Get(ContextKeyClientIP); exists {
		if ip, ok := val.(string); ok && ip != "" {
			return ip
		}
	}
	return ExtractClientIP(c)
}

// GetUserAgent gets the User-Agent from context or extracts it from the request header.
func GetUserAgent(c *gin.Context) string {
	if val, exists := c.Get(ContextKeyUserAgent); exists {
		if ua, ok := val.(string); ok && ua != "" {
			return ua
		}
	}
	return c.GetHeader("User-Agent")
}
