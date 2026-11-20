package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"go_backend_boilerplate/internal/core/problem"
)

// IdempotencyStatusInProgress marks a key currently claimed by an in-flight operation.
const IdempotencyStatusInProgress = "IN_PROGRESS"

// CachedResponse stores HTTP response components for replay.
type CachedResponse struct {
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body"`
}

type idempotencyWriter struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	statusCode int
}

func (w *idempotencyWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *idempotencyWriter) WriteString(s string) (int, error) {
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

func (w *idempotencyWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

// Idempotency returns a Gin middleware enforcing safe idempotent mutations via Redis.
func Idempotency(rdb goredis.Cmdable, ttl time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only mutating methods qualify for idempotency
		method := c.Request.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			c.Next()
			return
		}

		key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if key == "" || rdb == nil {
			c.Next()
			return
		}

		redisKey := "idempotency:" + key
		ctx := c.Request.Context()

		// Attempt atomic lease reservation
		acquired, err := rdb.SetNX(ctx, redisKey, IdempotencyStatusInProgress, ttl).Result()
		if err != nil {
			// On Redis failure, fail-open to not block the request
			c.Next()
			return
		}

		if !acquired {
			// Key already exists; inspect state
			val, getErr := rdb.Get(ctx, redisKey).Result()
			if getErr != nil && !errors.Is(getErr, goredis.Nil) {
				c.Next()
				return
			}

			if val == IdempotencyStatusInProgress {
				problem.Respond(
					c,
					http.StatusConflict,
					"CONCURRENT_REQUEST",
					"Conflict",
					"A request with this idempotency key is currently being processed.",
				)
				return
			}

			// Cached final response available; replay
			var cached CachedResponse
			if err := json.Unmarshal([]byte(val), &cached); err == nil {
				for hKey, hVals := range cached.Headers {
					for _, hVal := range hVals {
						c.Writer.Header().Add(hKey, hVal)
					}
				}
				c.Writer.Header().Set("X-Cache-Lookup", "true")
				c.Status(cached.StatusCode)
				_, _ = c.Writer.Write(cached.Body)
				c.Abort()
				return
			}

			// Malformed cache entry; proceed with fresh execution
			c.Next()
			return
		}

		// First execution for this key: intercept output
		rec := &idempotencyWriter{
			ResponseWriter: c.Writer,
			body:           bytes.NewBuffer(nil),
			statusCode:     http.StatusOK,
		}
		c.Writer = rec

		c.Next()

		// Post-execution: if status was 5xx, release lease to allow retry; otherwise cache response
		if rec.statusCode >= http.StatusInternalServerError {
			_ = rdb.Del(ctx, redisKey)
			return
		}

		cached := CachedResponse{
			StatusCode: rec.statusCode,
			Headers:    c.Writer.Header().Clone(),
			Body:       rec.body.Bytes(),
		}

		if serialized, err := json.Marshal(cached); err == nil {
			_ = rdb.Set(ctx, redisKey, string(serialized), ttl)
		}
	}
}
