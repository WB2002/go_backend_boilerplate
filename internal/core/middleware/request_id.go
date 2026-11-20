package middleware

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ContextKey is a custom type for context values to prevent collisions.
type ContextKey string

const (
	// RequestIDHeader is the HTTP header key for request tracing.
	RequestIDHeader = "X-Request-ID"
	// RequestIDKey is the Gin context key storing the request ID.
	RequestIDKey = "RequestID"
	// RequestIDContextKey is the stdlib context key storing the request ID.
	RequestIDContextKey ContextKey = "request_id"
)

// RequestID attaches or propagates a unique UUID request identifier across HTTP headers
// and Go contexts for distributed request tracing.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader(RequestIDHeader)
		if reqID == "" {
			reqID = uuid.New().String()
		}

		c.Header(RequestIDHeader, reqID)
		c.Set(RequestIDKey, reqID)

		ctx := context.WithValue(c.Request.Context(), RequestIDContextKey, reqID)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

// GetRequestID extracts the request ID from Gin context, header, or stdlib context.
func GetRequestID(c *gin.Context) string {
	if val, exists := c.Get(RequestIDKey); exists {
		if id, ok := val.(string); ok && id != "" {
			return id
		}
	}
	if id := c.GetHeader(RequestIDHeader); id != "" {
		return id
	}
	if c.Request != nil {
		return GetRequestIDFromContext(c.Request.Context())
	}
	return ""
}

// GetRequestIDFromContext extracts the request ID from a Go stdlib context.Context.
func GetRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(RequestIDContextKey).(string); ok && id != "" {
		return id
	}
	return ""
}
