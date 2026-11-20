package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"go_backend_boilerplate/internal/platform/logger"
)

// StructuredLogger returns a Gin middleware that records HTTP requests with latency,
// status, method, path, client IP, request ID, and automated PII masking.
func StructuredLogger(loggers ...*zerolog.Logger) gin.HandlerFunc {
	var log *zerolog.Logger
	if len(loggers) > 0 && loggers[0] != nil {
		log = loggers[0]
	} else {
		log = logger.Default()
	}

	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		rawQuery := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		clientIP := c.ClientIP()
		method := c.Request.Method
		userAgent := c.Request.UserAgent()
		reqID := GetRequestID(c)
		bytesWritten := c.Writer.Size()

		maskedQuery := ""
		if rawQuery != "" {
			maskedQuery = logger.MaskPII(rawQuery)
		}

		event := log.Info()
		if status >= 500 {
			event = log.Error()
		} else if status >= 400 {
			event = log.Warn()
		}

		event.
			Str("request_id", reqID).
			Str("method", method).
			Str("path", path).
			Str("query", maskedQuery).
			Int("status", status).
			Int64("latency_ms", latency.Milliseconds()).
			Str("client_ip", clientIP).
			Str("user_agent", userAgent).
			Int("bytes_written", bytesWritten).
			Msg("HTTP Request")
	}
}
