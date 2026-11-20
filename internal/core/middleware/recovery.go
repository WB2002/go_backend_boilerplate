package middleware

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"go_backend_boilerplate/internal/core/problem"
	"go_backend_boilerplate/internal/platform/logger"
)

// Recovery returns a Gin middleware that recovers from any panics, logs a structured
// stack trace with PII protection, and returns a safe HTTP 500 JSON response without leaking internals.
func Recovery(loggers ...*zerolog.Logger) gin.HandlerFunc {
	var log *zerolog.Logger
	if len(loggers) > 0 && loggers[0] != nil {
		log = loggers[0]
	} else {
		log = logger.Default()
	}

	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				reqID := GetRequestID(c)
				stack := debug.Stack()

				log.Error().
					Str("request_id", reqID).
					Str("path", c.Request.URL.Path).
					Str("method", c.Request.Method).
					Str("panic", fmt.Sprintf("%v", r)).
					Bytes("stack", stack).
					Msg("panic recovered")

				problem.Respond(c, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "Internal Server Error", "An unexpected error occurred. Please try again later.")
			}
		}()

		c.Next()
	}
}
