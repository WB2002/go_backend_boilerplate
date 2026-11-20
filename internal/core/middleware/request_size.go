package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"go_backend_boilerplate/internal/core/problem"
)

const defaultMaxBodyBytes int64 = 1 << 20 // 1 MiB

// MaxBodySize enforces a maximum HTTP request body size using http.MaxBytesReader
// and rejects requests whose declared Content-Length exceeds limitBytes with 413 Payload Too Large.
func MaxBodySize(limitBytes int64) gin.HandlerFunc {
	if limitBytes <= 0 {
		limitBytes = defaultMaxBodyBytes
	}

	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}

		if c.Request.ContentLength > limitBytes {
			problem.Respond(
				c,
				http.StatusRequestEntityTooLarge,
				"PAYLOAD_TOO_LARGE",
				"Payload Too Large",
				"Request body exceeds maximum allowed size.",
			)
			return
		}

		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limitBytes)
		}

		c.Next()
	}
}
