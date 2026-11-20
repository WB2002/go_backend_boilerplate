package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	stdpath "path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"go_backend_boilerplate/internal/core/problem"
)

const (
	// IdempotencyStatusInProgress marks a key currently claimed by an in-flight operation.
	IdempotencyStatusInProgress = "IN_PROGRESS"
	// ContextKeyUserID is the Gin context key storing the authenticated user ID.
	ContextKeyUserID = "user_id"

	maxIdempotencyBodyBytes = 10 << 20 // 10 MiB
)

// DefaultIdempotencyExemptPaths lists credential-issuing and reset routes that must never be cached.
var DefaultIdempotencyExemptPaths = []string{
	"/account/login",
	"/account/refresh",
	"/account/reset-password",
}

// CachedResponse stores HTTP response components and the originating request hash for replay.
type CachedResponse struct {
	StatusCode  int                 `json:"statusCode"`
	Headers     map[string][]string `json:"headers"`
	Body        []byte              `json:"body"`
	RequestHash string              `json:"requestHash"`
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

// ScopedIdempotencyKey derives a Redis cache key scoped by actor, HTTP method, normalized path,
// and client Idempotency-Key, together with the SHA-256 hex digest of the request body.
func ScopedIdempotencyKey(actorID string, method string, path string, rawKey string, bodyBytes []byte) (cacheKey string, requestHash string) {
	actor := strings.TrimSpace(actorID)
	if actor == "" {
		actor = "anon"
	}
	normMethod := strings.ToUpper(strings.TrimSpace(method))
	normPath := cleanRequestPath(path)
	key := strings.TrimSpace(rawKey)

	sum := sha256.Sum256(bodyBytes)
	requestHash = hex.EncodeToString(sum[:])
	cacheKey = fmt.Sprintf("idempotency:%s:%s:%s:%s", actor, normMethod, normPath, key)
	return cacheKey, requestHash
}

// Idempotency returns a Gin middleware enforcing safe idempotent mutations via Redis,
// exempting default credential-issuing endpoints.
func Idempotency(rdb goredis.Cmdable, ttl time.Duration) gin.HandlerFunc {
	return IdempotencyWithConfig(rdb, ttl, DefaultIdempotencyExemptPaths)
}

// IdempotencyWithConfig returns a Gin middleware enforcing actor-, method-, path-, and
// payload-scoped idempotent mutations via Redis while skipping configured exemptPaths.
func IdempotencyWithConfig(rdb goredis.Cmdable, ttl time.Duration, exemptPaths []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only mutating methods qualify for idempotency
		method := c.Request.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions || method == http.MethodTrace {
			c.Next()
			return
		}

		if isExemptPath(c.Request.URL.Path, exemptPaths) {
			c.Next()
			return
		}

		key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if key == "" || rdb == nil {
			c.Next()
			return
		}

		bodyBytes, err := readAndRestoreBody(c)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				problem.Respond(
					c,
					http.StatusRequestEntityTooLarge,
					"PAYLOAD_TOO_LARGE",
					"Payload Too Large",
					"Request body exceeds maximum allowed size.",
				)
				return
			}
			problem.Respond(
				c,
				http.StatusBadRequest,
				"INVALID_REQUEST_BODY",
				"Bad Request",
				"Unable to read request body.",
			)
			return
		}

		actorID := resolveIdempotencyActor(c)
		redisKey, requestHash := ScopedIdempotencyKey(actorID, method, c.Request.URL.Path, key, bodyBytes)
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
			if errors.Is(getErr, goredis.Nil) {
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

			// Cached final response available; validate payload hash before replay
			var cached CachedResponse
			if err := json.Unmarshal([]byte(val), &cached); err == nil {
				if cached.RequestHash != requestHash {
					problem.Respond(
						c,
						http.StatusUnprocessableEntity,
						"IDEMPOTENCY_KEY_REUSED",
						"Unprocessable Entity",
						"Idempotency key cannot be reused with a different request payload.",
					)
					return
				}

				for hKey, hVals := range cached.Headers {
					if strings.EqualFold(hKey, "Set-Cookie") {
						continue
					}
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

		// Post-execution: release lease on 5xx or auth failures to allow retry without caching failures
		if rec.statusCode >= http.StatusInternalServerError ||
			rec.statusCode == http.StatusUnauthorized ||
			rec.statusCode == http.StatusForbidden {
			_ = rdb.Del(ctx, redisKey)
			return
		}

		headers := c.Writer.Header().Clone()
		headers.Del("Set-Cookie")

		cached := CachedResponse{
			StatusCode:  rec.statusCode,
			Headers:     headers,
			Body:        rec.body.Bytes(),
			RequestHash: requestHash,
		}

		if serialized, err := json.Marshal(cached); err == nil {
			_ = rdb.Set(ctx, redisKey, string(serialized), ttl)
		}
	}
}

func cleanRequestPath(p string) string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return "/"
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	return stdpath.Clean(trimmed)
}

func isExemptPath(reqPath string, exemptPaths []string) bool {
	if len(exemptPaths) == 0 {
		return false
	}
	normPath := cleanRequestPath(reqPath)
	for _, exempt := range exemptPaths {
		exempt = strings.TrimSpace(exempt)
		if exempt == "" {
			continue
		}
		if strings.HasSuffix(exempt, "/*") {
			prefix := strings.TrimSuffix(exempt, "*")
			if strings.HasPrefix(normPath, prefix) {
				return true
			}
			continue
		}
		if normPath == cleanRequestPath(exempt) {
			return true
		}
	}
	return false
}

func resolveIdempotencyActor(c *gin.Context) string {
	if claims, ok := GetUser(c); ok && claims != nil {
		if uid := strings.TrimSpace(claims.UserID); uid != "" {
			return "user:" + uid
		}
	}
	if uid := strings.TrimSpace(c.GetString(ContextKeyUserID)); uid != "" {
		return "user:" + uid
	}
	if uid := strings.TrimSpace(c.GetString("userID")); uid != "" {
		return "user:" + uid
	}
	// When Idempotency runs globally before per-route AuthMiddleware, hash the Authorization
	// header so distinct bearer tokens never share a cache bucket behind the same IP.
	if authHeader := strings.TrimSpace(c.GetHeader("Authorization")); authHeader != "" {
		sum := sha256.Sum256([]byte(authHeader))
		return "auth:" + hex.EncodeToString(sum[:16])
	}
	ip := strings.TrimSpace(c.ClientIP())
	if ip == "" {
		ip = "unknown"
	}
	return "anon:" + ip
}

func readAndRestoreBody(c *gin.Context) ([]byte, error) {
	if c.Request == nil || c.Request.Body == nil {
		return nil, nil
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, maxIdempotencyBodyBytes+1))
	_ = c.Request.Body.Close()
	if err != nil {
		return nil, err
	}
	if int64(len(bodyBytes)) > maxIdempotencyBodyBytes {
		return nil, &http.MaxBytesError{Limit: maxIdempotencyBodyBytes}
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return bodyBytes, nil
}
