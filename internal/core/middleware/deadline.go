package middleware

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// RequestTimeoutHeader is the HTTP header inspected for per-request deadline overrides.
	RequestTimeoutHeader = "X-Request-Timeout"
)

// RequestDeadline extracts the X-Request-Timeout header (capped at maxTimeout) or falls back
// to defaultTimeout, wrapping the request context with a timeout so downstream handlers
// and platform queries can respect cancellation deadlines.
func RequestDeadline(defaultTimeout, maxTimeout time.Duration) gin.HandlerFunc {
	if defaultTimeout <= 0 {
		defaultTimeout = 30 * time.Second
	}

	return func(c *gin.Context) {
		timeout := defaultTimeout
		if hdr := c.GetHeader(RequestTimeoutHeader); hdr != "" {
			if d, err := time.ParseDuration(hdr); err == nil && d > 0 {
				timeout = d
			} else if ms, err := strconv.ParseInt(hdr, 10, 64); err == nil && ms > 0 {
				timeout = time.Duration(ms) * time.Millisecond
			}
		}

		if maxTimeout > 0 && timeout > maxTimeout {
			timeout = maxTimeout
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)
		c.Next()

		if errors.Is(ctx.Err(), context.DeadlineExceeded) && !c.Writer.Written() {
			c.AbortWithStatus(http.StatusGatewayTimeout)
		}
	}
}
