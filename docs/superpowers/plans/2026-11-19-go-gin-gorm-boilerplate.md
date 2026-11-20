# Go Gin & GORM Backend Boilerplate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a production-grade, enterprise Go backend boilerplate using Gin, GORM (PostgreSQL), MongoDB, and Redis, faithfully replicating all 15 capabilities, resiliency patterns, security controls, and operational features specified in the NestJS Boilerplate Features reference.

**Architecture:** The project follows standard Go clean architecture with modular domain packages (`internal/core`, `internal/platform`, `internal/modules`). High-throughput HTTP serving is driven by Gin with multi-VM ALB keep-alive, graceful shutdown draining, priority load shedding, and request deadline propagation. Polyglot persistence coordinates GORM (Postgres), Mongo Go driver, and Go-Redis with resilient pooling, dual-store ACID background jobs (`SKIP LOCKED`), multi-tier caching (SWR + singleflight), and horizontal Redis Pub/Sub real-time communication.

**Tech Stack:** Go 1.23+, Gin Web Framework (`github.com/gin-gonic/gin`), GORM (`gorm.io/gorm`, `gorm.io/driver/postgres`), Mongo Go Driver (`go.mongodb.org/mongo-driver`), Go-Redis (`github.com/redis/go-redis/v9`), OpenTelemetry Go SDK, Zerolog (`github.com/rs/zerolog`), Gorilla WebSocket, gqlgen (`github.com/99designs/gqlgen`), GeoLite2 (`github.com/oschwald/geoip2-golang`), Sony Gobreaker, Singleflight (`golang.org/x/sync/singleflight`).

**Spec:** Google Doc "NestJS Boilerplate Features" tab: [Development Guidelines - NestJS Boilerplate Features](https://docs.google.com/document/d/1bX3OxxybZo3_QgZFpleyVgZZn_mem03WLKkgHyFivIs/edit?tab=t.jskis3tlumf6)

---

## Global Constraints

- **Language & Runtime:** Go 1.23+ with Go Modules (`go.mod`).
- **HTTP Engine:** Gin (`github.com/gin-gonic/gin`) in Release mode for production workloads.
- **Relational ORM:** GORM v2 with PostgreSQL driver (`gorm.io/driver/postgres`).
- **Cache & Pub/Sub:** Redis client v9 (`github.com/redis/go-redis/v9`).
- **No Global Mutable State:** All dependencies (DB, Redis, Logger, Config) passed via constructors and dependency injection.
- **Strict Context Propagation:** Every database, network, cache, and long-running call must accept and respect `context.Context` cancellation and deadlines.
- **Fail-Open Non-Critical Systems:** Notifications and non-blocking metrics must fail open without terminating client transactions.
- **Zero-Tolerance Panic Recovery:** All panics inside handlers and background workers must be caught, logged as structured errors with stack traces, and returned as standard 500 responses.

---

## Review Focus

These five critical failure modes and edge cases are explicitly addressed and guarded across the tasks:

1. **ALB 502 Bad Gateway during Rolling Deploys & Idle Timeouts:** If `HTTP_KEEP_ALIVE_TIMEOUT_MS` (e.g. 65s) is less than or equal to the ALB idle timeout (60s), or if active connections are dropped during SIGTERM without draining, clients encounter intermittent 502s. *Owned by Task 2.*
2. **Argon2id CPU Starvation & Worker Exhaustion:** Unbounded parallel Argon2id password hashes under authentication bursts starve the Go runtime scheduler and saturate OS threads. *Owned by Task 5.*
3. **Cache Stampede on Hot-Key Expiration:** High-frequency cache miss bursts trigger thundering herd queries against PostgreSQL/MongoDB unless protected by distributed locks, local singleflight coalescing, and TTL jitter. *Owned by Task 8.*
4. **SSRF via Outbound HTTP Webhooks/Clients:** Internal metadata services (AWS IMDS `169.254.169.254`, loopback `127.0.0.1`, RFC1918 subnets) being accessed through user-supplied URLs unless DNS resolution and IP ranges are blocked before socket dial. *Owned by Task 9.*
5. **Orphaned Background Job Leases on Worker Crash:** Distributed jobs claimed by a worker node that panics or terminates without completing remain locked forever unless active leases are reclaimed using periodic PostgreSQL `SKIP LOCKED` sweepers. *Owned by Task 10.*

---

## File Structure Plan

```text
go_backend_boilerplate/
├── cmd/
│   ├── api/
│   │   └── main.go                         # Main HTTP server entrypoint
│   └── worker/
│       └── main.go                         # Background worker daemon entrypoint
├── internal/
│   ├── config/                             # Strongly typed configuration with env validation
│   │   ├── config.go
│   │   └── config_test.go
│   ├── platform/                           # Low-level platform drivers & connections
│   │   ├── database/
│   │   │   ├── postgres.go                 # GORM PostgreSQL connection pool
│   │   │   └── postgres_test.go
│   │   ├── mongodb/
│   │   │   ├── mongo.go                    # Mongo client with lazy initialization
│   │   │   └── mongo_test.go
│   │   ├── redis/
│   │   │   ├── redis.go                    # Go-Redis client with health checks
│   │   │   └── redis_test.go
│   │   └── logger/
│   │       ├── logger.go                   # Zerolog with PII masking & CloudWatch EMF
│   │       └── logger_test.go
│   ├── core/                               # Cross-cutting operational core services
│   │   ├── server/                         # HTTP Server with ALB keep-alive & graceful drain
│   │   │   ├── server.go
│   │   │   └── server_test.go
│   │   ├── middleware/                     # Gin middleware chain
│   │   │   ├── deadline.go                 # Request deadline & timeout propagation
│   │   │   ├── recovery.go                 # Panic recovery with structured log
│   │   │   ├── security.go                 # Security headers (CSP, HSTS, X-Frame)
│   │   │   ├── compression.go              # Threshold-based gzip compression
│   │   │   ├── cors.go                     # CORS origin validation
│   │   │   ├── csrf.go                     # Double-submit cookie CSRF
│   │   │   ├── ratelimit.go                # Multi-identifier rate limiter
│   │   │   ├── overload.go                 # Priority shedding & concurrency bulkhead
│   │   │   ├── geoip.go                    # GeoLite2 country-level allow/block filter
│   │   │   └── middleware_test.go
│   │   ├── auth/                           # Authentication & authorization subsystem
│   │   │   ├── token.go                    # RS256 JWT sign & verify with jti blacklist
│   │   │   ├── hasher.go                   # Argon2id hasher with bounded concurrency bulkhead
│   │   │   ├── service.go                  # Auth service (register, login, refresh, revoke)
│   │   │   └── auth_test.go
│   │   ├── cache/                          # Multi-tier cache & stampede protection
│   │   │   ├── cache.go                    # SWR, singleflight, Redis NX locks, TTL jitter
│   │   │   └── cache_test.go
│   │   ├── httpclient/                     # Resilient outbound HTTP client
│   │   │   ├── client.go                   # Retry with jitter, breaker, SSRF guard
│   │   │   └── client_test.go
│   │   ├── jobs/                           # Background job processor
│   │   │   ├── ledger.go                   # PostgreSQL ACID job ledger (SKIP LOCKED)
│   │   │   ├── queue.go                    # Redis distributed queue
│   │   │   ├── worker.go                   # Worker concurrency & lease reclamation
│   │   │   └── jobs_test.go
│   │   ├── websocket/                      # Real-time WebSocket manager
│   │   │   ├── hub.go                      # WebSocket Hub with Redis Pub/Sub adapter
│   │   │   ├── client.go                   # Per-user limits & packet rate limiting
│   │   │   └── websocket_test.go
│   │   ├── graphql/                        # Schema-first GraphQL API
│   │   │   ├── schema.graphqls
│   │   │   ├── resolver.go
│   │   │   ├── complexity.go               # Query depth & complexity limiter
│   │   │   └── graphql_test.go
│   │   ├── security/                       # Security scanning & verification
│   │   │   ├── clamav.go                   # ClamAV daemon antivirus stream scanner
│   │   │   ├── upload.go                   # Magic-byte validation & multipart upload
│   │   │   ├── captcha.go                  # hCaptcha / reCAPTCHA verification
│   │   │   └── security_test.go
│   │   ├── featureflag/                    # Dynamic feature flag engine
│   │   │   ├── engine.go                   # Redis store, local cache, pub/sub invalidation
│   │   │   └── featureflag_test.go
│   │   ├── notification/                   # Fail-open notifications
│   │   │   ├── slack.go                    # Slack webhook integration
│   │   │   └── notification_test.go
│   │   └── telemetry/                      # Observability, metrics, and probes
│   │       ├── health.go                   # Cached /health, /health/ready, /health/live
│   │       ├── otel.go                     # OpenTelemetry tracing setup
│   │       └── telemetry_test.go
│   └── modules/                            # Business domain modules
│       └── account/                        # Account & user management
│           ├── handler.go
│           ├── model.go
│           ├── repository.go
│           └── account_test.go
├── migrations/                             # SQL migrations
│   ├── 000001_create_users_table.up.sql
│   ├── 000001_create_users_table.down.sql
│   ├── 000002_create_background_jobs_table.up.sql
│   └── 000002_create_background_jobs_table.down.sql
├── scripts/
│   ├── verify.sh                           # Build, test, and lint runner
│   └── seed.go                             # Database seeder
├── Makefile
└── go.mod
```

---

## Tasks

### Task 1: Project Scaffolding, Configuration, and Quality Infrastructure

**Files:**
- Create: `go.mod`
- Create: `Makefile`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Modify: `scripts/verify.sh`
- Create: `.golangci.yml`

**Interfaces:**
- Consumes: Environment variables via `os.Getenv` / validator.
- Produces: `Config` struct containing all 15 configuration blocks (HTTP, Auth, DB, Redis, Mongo, RateLimit, Overload, Cache, Jobs, WebSocket, GraphQL, Security, OTel, FeatureFlag, Notification).

- [ ] **Step 1: Write failing test for configuration loading and validation**

```go
// internal/config/config_test.go
package config_test

import (
	"os"
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/config"
)

func test_specific_behavior(t *testing.T) {
	os.Clearenv()
	os.Setenv("PORT", "8080")
	os.Setenv("NODE_ENV", "test")
	os.Setenv("JWT_PRIVATE_KEY", "dummy-private-key")
	os.Setenv("JWT_PUBLIC_KEY", "dummy-public-key")
	os.Setenv("COOKIE_SECRET", "super-secret-key-32-bytes-minimum")
	os.Setenv("POSTGRES_HOST", "localhost")
	os.Setenv("POSTGRES_PORT", "5432")
	os.Setenv("POSTGRES_USER", "postgres")
	os.Setenv("POSTGRES_PASSWORD", "postgres")
	os.Setenv("POSTGRES_DB", "boilerplate_test")
	os.Setenv("REDIS_HOST", "localhost")
	os.Setenv("REDIS_PORT", "6379")
	os.Setenv("MONGO_URI", "mongodb://localhost:27017/test")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.HTTP.Port)
	assert.Equal(t, "test", cfg.App.Env)
	assert.Equal(t, 65000, cfg.HTTP.KeepAliveTimeoutMs)
	assert.Equal(t, 30000, cfg.HTTP.RequestTimeoutMs)
}

func TestConfigMissingRequiredFails(t *testing.T) {
	os.Clearenv()
	_, err := config.Load()
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config -v`
Expected: FAIL with package or symbol not found.

- [ ] **Step 3: Implement `Load() (*Config, error)` in `internal/config/config.go`**

Define `Config` with nested sub-configs (`HTTPConfig`, `AuthConfig`, `DatabaseConfig`, `RedisConfig`, `MongoConfig`, `OverloadConfig`, `RateLimitConfig`, `CacheConfig`, `JobsConfig`, `WebSocketConfig`, `GraphQLConfig`, `SecurityConfig`, `TelemetryConfig`, `FeatureFlagConfig`, `NotificationConfig`). Use reflection / struct tags with defaults and required validation.

- [ ] **Step 4: Update `scripts/verify.sh` and run tests**

Configure `scripts/verify.sh` to run `go build ./...`, `go test -v ./...`, and `golangci-lint run`.
Run: `go test ./internal/config -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add go.mod Makefile internal/config/ scripts/verify.sh .golangci.yml
git commit -m "feat(config): add strongly-typed environment config and validation"
```

---

### Task 2: Core HTTP Server, Gin Engine, Multi-VM ALB Keep-Alive & Graceful Drain

**Files:**
- Create: `internal/core/server/server.go`
- Create: `internal/core/server/server_test.go`
- Create: `cmd/api/main.go`

**Interfaces:**
- Consumes: `config.HTTPConfig`
- Produces: `Server` struct with `Start() error` and `Shutdown(ctx context.Context) error`.

- [ ] **Step 1: Write failing test for HTTP server lifecycle and keep-alive configuration**

```go
// internal/core/server/server_test.go
package server_test

import (
	"context"
	"net/http"
	"testing"
	"time"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/server"
)

func TestServerKeepAliveAndGracefulShutdown(t *testing.T) {
	cfg := config.HTTPConfig{
		Port:                 18080,
		KeepAliveTimeoutMs:   65000,
		RequestTimeoutMs:     30000,
		ShutdownDrainMs:      500,
		ShutdownForceMs:      1000,
	}

	srv := server.New(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	errChan := make(chan error, 1)
	go func() {
		errChan <- srv.Start()
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://localhost:18080/health/live")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = srv.Shutdown(ctx)
	assert.NoError(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/server -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement `server.New(cfg, handler)` and graceful shutdown in `internal/core/server/server.go`**

Configure `http.Server` with:
- `IdleTimeout = time.Duration(cfg.KeepAliveTimeoutMs) * time.Millisecond` (65s > 60s ALB idle).
- `ReadHeaderTimeout = 5 * time.Second`.
- `ReadTimeout = 30 * time.Second`.
- `WriteTimeout = time.Duration(cfg.RequestTimeoutMaxMs) * time.Millisecond`.
Implement signal handling (`syscall.SIGTERM`, `syscall.SIGINT`) with `ShutdownDrainMs` before closing listeners.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/server -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/server/ cmd/api/main.go
git commit -m "feat(server): add core HTTP server with ALB keep-alive and graceful drain"
```

---

### Task 3: Middleware Foundation (Logging, Recovery, Deadline, Security, Compression, CORS)

**Files:**
- Create: `internal/core/middleware/deadline.go`
- Create: `internal/core/middleware/recovery.go`
- Create: `internal/core/middleware/security.go`
- Create: `internal/core/middleware/compression.go`
- Create: `internal/core/middleware/cors.go`
- Create: `internal/core/middleware/request_id.go`
- Create: `internal/core/middleware/middleware_test.go`
- Create: `internal/platform/logger/logger.go`

**Interfaces:**
- Consumes: `gin.Context`, `config.HTTPConfig`
- Produces: Standard Gin middleware handlers: `RequestID()`, `StructuredLogger()`, `Recovery()`, `RequestDeadline(defaultMs, maxMs)`, `SecurityHeaders()`, `GzipCompression(minSizeThreshold)`, `CORS(allowedOrigins)`.

- [ ] **Step 1: Write failing test for deadline context propagation and compression**

```go
// internal/core/middleware/middleware_test.go
package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/core/middleware"
)

func TestRequestDeadlineTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestDeadline(50*time.Millisecond, 200*time.Millisecond))

	router.GET("/slow", func(c *gin.Context) {
		select {
		case <-time.After(100 * time.Millisecond):
			c.String(http.StatusOK, "done")
		case <-c.Request.Context().Done():
			c.AbortWithStatus(http.StatusGatewayTimeout)
		}
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/slow", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusGatewayTimeout, w.Code)
}

func TestGzipCompressionThreshold(t *testing.T) {
	router := gin.New()
	router.Use(middleware.GzipCompression(1024))
	router.GET("/large", func(c *gin.Context) {
		c.String(http.StatusOK, string(make([]byte, 2048)))
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/large", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	router.ServeHTTP(w, req)

	assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/middleware -v`
Expected: FAIL with undefined `RequestDeadline` and `GzipCompression`.

- [ ] **Step 3: Implement middleware suite in `internal/core/middleware/`**

- `deadline.go`: Extract `X-Request-Timeout` header if present (capped at `maxMs`), fall back to `defaultMs`. Wrap `req.Context()` in `context.WithTimeout`.
- `compression.go`: Wrap response writer to buffer responses below `threshold` and gzip stream above `threshold`.
- `security.go`: Set `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Strict-Transport-Security`, `Content-Security-Policy: default-src 'self'`.
- `logger.go`: Zerolog with PII masking regexes (email, Authorization header, password fields).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/middleware -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/middleware/ internal/platform/logger/
git commit -m "feat(middleware): add deadline propagation, compression, security headers, and logger"
```

---

### Task 4: Multi-Store Persistence (PostgreSQL with GORM, MongoDB, and Redis)

**Files:**
- Create: `internal/platform/database/postgres.go`
- Create: `internal/platform/mongodb/mongo.go`
- Create: `internal/platform/redis/redis.go`
- Create: `internal/platform/database/postgres_test.go`
- Create: `internal/platform/redis/redis_test.go`

**Interfaces:**
- Consumes: `config.DatabaseConfig`, `config.MongoConfig`, `config.RedisConfig`
- Produces: `*gorm.DB`, `*mongo.Client`, `*redis.Client` with health probe ping methods.

- [ ] **Step 1: Write failing test for GORM pool configuration and Redis health**

```go
// internal/platform/database/postgres_test.go
package database_test

import (
	"testing"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/platform/database"
)

func TestPostgresConnectionConfig(t *testing.T) {
	cfg := config.DatabaseConfig{
		Host:            "invalid-host",
		Port:            5432,
		User:            "user",
		Password:        "pass",
		DBName:          "db",
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 300,
	}

	_, err := database.NewPostgres(cfg)
	// Expect failure on invalid host, but verifies constructor and pooling logic
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/database -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement database connections**

- In `postgres.go`: Configure GORM with `postgres.Open(dsn)` and tune underlying `*sql.DB` connection pool (`SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime`).
- In `mongo.go`: Initialize `mongo.Connect` with timeout, lazy ping so app starts even if Mongo is temporarily offline.
- In `redis.go`: Initialize `redis.NewClient(&redis.Options{Addr: ...})` with retry backoff and connection pooling.

- [ ] **Step 4: Run unit tests**

Run: `go test ./internal/platform/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/platform/database/ internal/platform/mongodb/ internal/platform/redis/
git commit -m "feat(persistence): implement GORM PostgreSQL, MongoDB, and Redis clients"
```

---

### Task 5: Authentication, Authorization, Argon2id Bulkhead & CSRF Protection

**Files:**
- Create: `internal/core/auth/token.go`
- Create: `internal/core/auth/hasher.go`
- Create: `internal/core/auth/service.go`
- Create: `internal/core/middleware/auth.go`
- Create: `internal/core/middleware/csrf.go`
- Create: `internal/core/auth/auth_test.go`

**Interfaces:**
- Consumes: RSA PEM Keys (`JWT_PRIVATE_KEY`, `JWT_PUBLIC_KEY`), `redis.Client`, `*gorm.DB`.
- Produces:
  - `TokenManager`: `GenerateTokenPair(userId, roles) (*TokenPair, error)`, `ValidateAccessToken(tokenStr) (*Claims, error)`, `RevokeToken(jti, ttl) error`.
  - `PasswordHasher`: `HashPassword(password) (string, error)`, `VerifyPassword(password, hash) (bool, error)` bounded by concurrent semaphore.
  - Gin Middleware: `AuthMiddleware(tokenManager, redisClient)`, `CSRFMiddleware(secret, exemptPaths)`.

- [ ] **Step 1: Write failing test for RS256 JWT generation, revocation check, and Argon2id bulkhead queue**

```go
// internal/core/auth/auth_test.go
package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"sync"
	"testing"
	"time"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/core/auth"
)

func generateTestRSAKeys() (string, string) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	pubBytes, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	return string(privPEM), string(pubPEM)
}

func TestArgon2idBulkheadConcurrencyLimit(t *testing.T) {
	// Bulkhead allows max 2 concurrent hashes, queue depth 4
	hasher := auth.NewArgon2idHasher(2, 4, 100*time.Millisecond)

	var wg sync.WaitGroup
	errCount := 0
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := hasher.HashPassword("SuperSecretPassword123!")
			if err != nil {
				mu.Lock()
				errCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	// Under high burst, queue capacity exceeded triggers 503/rejection error
	assert.Greater(t, errCount, 0, "Expected bulkhead to reject excess password hashing requests")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/auth -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement Auth Subsystem**

- `hasher.go`: Implement Argon2id hashing with a buffered channel semaphore (`maxConcurrent = 2`) and waiting queue timeout (`maxWait = 2s`). Reject when queue exceeds cap.
- `token.go`: Use `golang-jwt/jwt/v5` with `jwt.SigningMethodRS256`. Include `jti` UUID.
- `auth.go` middleware: Parse Bearer token, check if Redis key `revoked_jti:<jti>` exists. Set `AuthenticatedUser` in `c.Set("user", claims)`.
- `csrf.go` middleware: Implement Double-Submit Cookie pattern. Check `X-CSRF-Token` header against signed cookie for `POST`, `PUT`, `DELETE`, `PATCH`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/auth -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/auth/ internal/core/middleware/auth.go internal/core/middleware/csrf.go
git commit -m "feat(auth): implement RS256 JWT, Argon2id bulkhead, token revocation, and CSRF guard"
```

---

### Task 6: Inbound Overload Protection, Concurrency Bulkhead & Priority Load Shedding

**Files:**
- Create: `internal/core/overload/manager.go`
- Create: `internal/core/middleware/overload.go`
- Create: `internal/core/overload/overload_test.go`

**Interfaces:**
- Consumes: `config.OverloadConfig`
- Produces:
  - `OverloadManager`: Monitors memory, GC pause, active in-flight request count.
  - Gin Middleware `InboundOverload(manager)` with route priority annotation (`c.Set("priority", PriorityLevel)`).

- [ ] **Step 1: Write failing test for priority-based request shedding under load**

```go
// internal/core/overload/overload_test.go
package overload_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/core/overload"
)

func TestPriorityLoadShedding(t *testing.T) {
	// Max in-flight: 2 requests
	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             2,
		PrioritySheddingEnabled: true,
		RetryAfterSec:           5,
	})

	router := gin.New()
	router.Use(mgr.Middleware())

	router.GET("/low", func(c *gin.Context) {
		c.Set("api_priority", overload.PriorityLow)
		c.String(http.StatusOK, "low priority done")
	})

	router.GET("/critical", func(c *gin.Context) {
		c.Set("api_priority", overload.PriorityCritical)
		c.String(http.StatusOK, "critical done")
	})

	// Simulate system under load by acquiring in-flight tokens
	mgr.Acquire()
	mgr.Acquire()

	// Low priority request must be rejected with 503 Overloaded
	wLow := httptest.NewRecorder()
	reqLow, _ := http.NewRequest(http.MethodGet, "/low", nil)
	router.ServeHTTP(wLow, reqLow)
	assert.Equal(t, http.StatusServiceUnavailable, wLow.Code)
	assert.Equal(t, "5", wLow.Header().Get("Retry-After"))

	// Release one token
	mgr.Release()

	// Critical priority request must succeed
	wCrit := httptest.NewRecorder()
	reqCrit, _ := http.NewRequest(http.MethodGet, "/critical", nil)
	router.ServeHTTP(wCrit, reqCrit)
	assert.Equal(t, http.StatusOK, wCrit.Code)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/overload -v`
Expected: FAIL with undefined `overload.NewManager`.

- [ ] **Step 3: Implement Overload Manager in `internal/core/overload/manager.go`**

- Use atomic counter for in-flight requests.
- When `in_flight >= max_in_flight * 0.75`, shed `PriorityLow`.
- When `in_flight >= max_in_flight * 0.90`, shed `PriorityNormal`.
- When `in_flight >= max_in_flight`, shed `PriorityHigh`. Keep `PriorityCritical` reserved.
- Set `Retry-After: 5` header and return 503 response.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/overload -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/overload/ internal/core/middleware/overload.go
git commit -m "feat(overload): add inbound concurrency bulkhead and priority load shedding"
```

---

### Task 7: Multi-Identifier Rate Limiting with Redis & Memory Fallback

**Files:**
- Create: `internal/core/ratelimit/limiter.go`
- Create: `internal/core/ratelimit/storage.go`
- Create: `internal/core/middleware/ratelimit.go`
- Create: `internal/core/ratelimit/ratelimit_test.go`

**Interfaces:**
- Consumes: `redis.Client`, `config.RateLimitConfig`
- Produces: `RateLimiter` with support for tracking by:
  - Client IP (`c.ClientIP()`)
  - User ID (`c.GetString("user_id")`)
  - Device Fingerprint (`c.GetHeader("X-Device-Fingerprint")`)
  - Guest Session Cookie (`c.Cookie("guest_session")`)
  - Middleware: `RateLimit(identifier, limit, window)`

- [ ] **Step 1: Write failing test for rate limiting with fallback cooldown**

```go
// internal/core/ratelimit/ratelimit_test.go
package ratelimit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/core/ratelimit"
)

func TestRateLimitExceeded(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	limiter := ratelimit.New(store, ratelimit.Config{
		DefaultLimit:  2,
		DefaultWindow: 1 * time.Second,
	})

	router := gin.New()
	router.Use(limiter.Middleware(ratelimit.IdentifierIP, 2, 1*time.Second))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
	}

	wBlocked := httptest.NewRecorder()
	reqBlocked, _ := http.NewRequest(http.MethodGet, "/test", nil)
	router.ServeHTTP(wBlocked, reqBlocked)
	assert.Equal(t, http.StatusTooManyRequests, wBlocked.Code)
	assert.Equal(t, "1", wBlocked.Header().Get("Retry-After"))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ratelimit -v`
Expected: FAIL with undefined `ratelimit.NewMemoryStore`.

- [ ] **Step 3: Implement Rate Limiter in `internal/core/ratelimit/`**

- Support sliding window counter via Redis Lua script (`ZADD`, `ZREMRANGEBYSCORE`, `ZCARD`).
- Fallback store using concurrent map with LRU eviction and cooldown timer (`RATE_LIMIT_FALLBACK_COOLDOWN_MS`).
- Emit standard headers: `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`, `Retry-After`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/ratelimit -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/ratelimit/ internal/core/middleware/ratelimit.go
git commit -m "feat(ratelimit): implement multi-identifier rate limiter with Redis/memory failover"
```

---

### Task 8: Stampede-Proof Multi-Tier Caching Layer (SWR, Singleflight, Redis Locks, TTL Jitter)

**Files:**
- Create: `internal/core/cache/cache.go`
- Create: `internal/core/cache/redis_lock.go`
- Create: `internal/core/cache/cache_test.go`

**Interfaces:**
- Consumes: `redis.Client`, `singleflight.Group`
- Produces: `CacheService`:
  - `GetOrSet[T any](ctx context.Context, key string, loader func() (T, error), opts CacheOptions) (T, error)`

- [ ] **Step 1: Write failing test for singleflight request coalescing and TTL jitter**

```go
// internal/core/cache/cache_test.go
package cache_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/core/cache"
)

func TestCacheSingleFlightDeduplication(t *testing.T) {
	c := cache.NewMemoryCache(cache.Config{
		TTLJitterRatio: 0.15,
	})

	var loaderCalls int32
	loader := func() (string, error) {
		atomic.AddInt32(&loaderCalls, 1)
		time.Sleep(50 * time.Millisecond)
		return "cached-data", nil
	}

	var wg sync.WaitGroup
	results := make([]string, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			val, err := cache.GetOrSet(c, context.Background(), "test-key", loader, cache.Options{
				SoftTTL: 1 * time.Minute,
				HardTTL: 5 * time.Minute,
			})
			if err == nil {
				results[idx] = val
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&loaderCalls), "Loader should only be called once via singleflight")
	for _, res := range results {
		assert.Equal(t, "cached-data", res)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/cache -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement Cache Service with Stampede Defense in `internal/core/cache/cache.go`**

- Use `golang.org/x/sync/singleflight` to merge concurrent in-process calls.
- Stale-While-Revalidate (SWR): If cached data is past `SoftTTL` but before `HardTTL`, return stale value immediately and trigger asynchronous background refresh.
- Redis loader lock: Distributed `SET key:lock uuid NX EX 30` to prevent cross-node cache stampedes.
- TTL Jitter: Add `rand(-jitterRatio, +jitterRatio) * TTL` to prevent synchronized expirations.
- Negative Caching: Support caching `nil`/not-found states with short TTL (`CACHE_NEGATIVE_SOFT_TTL_MS`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/cache -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/cache/
git commit -m "feat(cache): implement SWR multi-tier cache with stampede defense and singleflight"
```

---

### Task 9: Resilient Outbound HTTP Client (Retry, Circuit Breaker, Jitter, SSRF Guard)

**Files:**
- Create: `internal/core/httpclient/client.go`
- Create: `internal/core/httpclient/ssrf.go`
- Create: `internal/core/httpclient/httpclient_test.go`

**Interfaces:**
- Consumes: `http.Client`, `sony/gobreaker`
- Produces: `ResilientClient`:
  - `Get(ctx, path) (*http.Response, error)`
  - `Post(ctx, path, body) (*http.Response, error)`
  - Builder options: `WithRetry(maxRetries)`, `WithCircuitBreaker(threshold, timeout)`, `WithSSRFProtection()`.

- [ ] **Step 1: Write failing test for SSRF blocking of private IP ranges and loopback**

```go
// internal/core/httpclient/httpclient_test.go
package httpclient_test

import (
	"context"
	"testing"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/core/httpclient"
)

func TestSSRFGuardBlocksPrivateIPs(t *testing.T) {
	client := httpclient.NewBuilder().
		WithSSRFProtection(true).
		Build()

	ctx := context.Background()

	// Loopback
	_, err := client.Get(ctx, "http://127.0.0.1:8080/secret")
	assert.ErrorContains(t, err, "blocked private IP")

	// AWS Metadata
	_, err = client.Get(ctx, "http://169.254.169.254/latest/meta-data")
	assert.ErrorContains(t, err, "blocked private IP")

	// Private Subnet RFC 1918
	_, err = client.Get(ctx, "http://10.0.0.5/api")
	assert.ErrorContains(t, err, "blocked private IP")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/httpclient -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement Outbound HTTP Client and SSRF Guard**

- `ssrf.go`: Implement custom `net.Dialer` `Control` function that resolves DNS and verifies IP is not in private/reserved CIDRs (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.0/8`, `169.254.0.0/16`, `::1`).
- `client.go`: Wrap with Sony Gobreaker. Implement exponential backoff with full jitter: `backoff = min(maxInterval, baseInterval * 2^attempt) * rand(0, 1)`. Propagate request context and deadlines.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/httpclient -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/httpclient/
git commit -m "feat(httpclient): add resilient outbound HTTP client with circuit breaker and SSRF guard"
```

---

### Task 10: Dual-Persistence Background Jobs Engine (PostgreSQL Ledger + Redis Queue)

**Files:**
- Create: `migrations/000002_create_background_jobs_table.up.sql`
- Create: `internal/core/jobs/ledger.go`
- Create: `internal/core/jobs/worker.go`
- Create: `internal/core/jobs/jobs_test.go`
- Create: `cmd/worker/main.go`

**Interfaces:**
- Consumes: `*gorm.DB`, `*redis.Client`
- Produces:
  - `JobEnqueueService`: `Enqueue(ctx, queue, payload, deduplicationKey) (string, error)`
  - `WorkerPool`: `RegisterHandler(queue, handler)`, `Start(ctx)`, `ReclaimOrphanLeases(ctx)`.

- [ ] **Step 1: Write failing test for job ledger claim via `SKIP LOCKED` and orphan lease reclamation**

```go
// internal/core/jobs/jobs_test.go
package jobs_test

import (
	"context"
	"testing"
	"time"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/core/jobs"
)

func TestJobDeduplicationAndLeaseReclamation(t *testing.T) {
	// Mock or SQLite test for ledger logic
	ledger := jobs.NewTestLedger(t)

	ctx := context.Background()
	jobID, err := ledger.Enqueue(ctx, "invoices", []byte(`{"orderId":"123"}`), "inv-123")
	require.NoError(t, err)
	assert.NotEmpty(t, jobID)

	// Duplicate enqueue with same deduplicationKey returns existing or error
	_, err = ledger.Enqueue(ctx, "invoices", []byte(`{"orderId":"123"}`), "inv-123")
	assert.ErrorContains(t, err, "duplicate")

	// Claim job with 1 second lease
	claimed, err := ledger.ClaimNext(ctx, "invoices", "worker-1", 1*time.Second)
	require.NoError(t, err)
	assert.Equal(t, jobID, claimed.ID)

	// Simulate worker crash: lease expires after 1s
	time.Sleep(1100 * time.Millisecond)

	// Reclaimer sweeps and marks stalled job back to pending
	reclaimedCount, err := ledger.ReclaimOrphanLeases(ctx, "invoices")
	require.NoError(t, err)
	assert.Equal(t, int64(1), reclaimedCount)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/jobs -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement Background Jobs Engine**

- Migration `000002_create_background_jobs_table.up.sql`: Table `background_jobs` with `id`, `queue`, `payload`, `state` (pending, claimed, completed, failed), `deduplication_key`, `claimed_by`, `lease_expires_at`, `attempts`.
- `ledger.go`: Claim query using `SELECT * FROM background_jobs WHERE queue = ? AND state = 'pending' ORDER BY created_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED`.
- `worker.go`: Goroutine worker pool with configurable concurrency (`BACKGROUND_JOBS_WORKER_CONCURRENCY`). Periodic sweeper (`BACKGROUND_JOBS_LEASE_RECLAIM_INTERVAL_MS`) to reset expired leases.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/jobs -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add migrations/000002_create_background_jobs_table.up.sql internal/core/jobs/ cmd/worker/main.go
git commit -m "feat(jobs): add PostgreSQL ACID job ledger with SKIP LOCKED and orphan reclaimer"
```

---

### Task 11: Real-Time WebSockets Engine (Redis Pub/Sub Horizontal Broadcast & Rate Limiter)

**Files:**
- Create: `internal/core/websocket/hub.go`
- Create: `internal/core/websocket/client.go`
- Create: `internal/core/websocket/handler.go`
- Create: `internal/core/websocket/websocket_test.go`

**Interfaces:**
- Consumes: `*redis.Client`, `tokenManager` (Auth), Gorilla WebSocket.
- Produces: `WebSocketHub` mounted on `/realtime`, Redis multi-VM message broadcast, per-user socket connection cap (default 5), and inbound packet rate limiter (120 msg/min).

- [ ] **Step 1: Write failing test for user socket connection cap and packet rate limit**

```go
// internal/core/websocket/websocket_test.go
package websocket_test

import (
	"testing"
	"time"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/core/websocket"
)

func TestWebSocketUserConnectionLimit(t *testing.T) {
	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 2,
		MessagesPerMinute:     10,
	}, nil)

	// User connects twice (allowed)
	c1, err := hub.Register("user-1")
	assert.NoError(t, err)
	assert.NotNil(t, c1)

	c2, err := hub.Register("user-1")
	assert.NoError(t, err)
	assert.NotNil(t, c2)

	// Third connection exceeds max cap
	_, err = hub.Register("user-1")
	assert.ErrorContains(t, err, "max connections exceeded")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/websocket -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement WebSocket Hub and Handlers in `internal/core/websocket/`**

- Gorilla WebSocket upgrader with Origin verification.
- Connect token verification in handshake query/header.
- Track active sockets per user in Redis (`INCR user:sockets:<user_id>`).
- Redis Pub/Sub subscriber listening to `realtime:broadcast` to replicate messages across server nodes.
- Per-socket token bucket rate limiter dropping or closing clients exceeding `WEBSOCKET_MESSAGES_PER_MINUTE`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/websocket -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/websocket/
git commit -m "feat(websocket): add real-time WebSocket hub with Redis pub/sub and per-user limits"
```

---

### Task 12: Schema-First GraphQL Engine with Query Depth & Complexity Limiting

**Files:**
- Create: `internal/core/graphql/schema.graphqls`
- Create: `internal/core/graphql/complexity.go`
- Create: `internal/core/graphql/handler.go`
- Create: `internal/core/graphql/graphql_test.go`

**Interfaces:**
- Consumes: Gin Engine, `gqlgen` runtime, `*redis.Client`.
- Produces: Gin GraphQL endpoint at `/graphql` and Playground at `/playground` with depth limiting (`maxDepth = 10`) and Redis-backed complexity budget per user.

- [ ] **Step 1: Write failing test for GraphQL query depth and complexity rejection**

```go
// internal/core/graphql/graphql_test.go
package graphql_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/core/graphql"
)

func TestGraphQLQueryDepthLimiting(t *testing.T) {
	handler := graphql.NewTestHandler(graphql.Config{
		MaxDepth:      3,
		MaxComplexity: 50,
	})

	// Query with depth 5 exceeds max depth 3
	deepQuery := `{"query": "{ categories { subcategories { categories { subcategories { categories { id } } } } } }"}`

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(deepQuery))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Contains(t, w.Body.String(), "query exceeds maximum depth")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/graphql -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement gqlgen Handler and Complexity Plugin**

- Define `schema.graphqls` with query and category types.
- Add `ComplexityLimit` extension checking total score against `GQL_MAX_COMPLEXITY` (50).
- Redis user budget tracker checking cumulative complexity (`GQL_USER_BUDGET = 1000/min`).
- Add depth limiter extension preventing nested query denial of service.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/graphql -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/graphql/
git commit -m "feat(graphql): add schema-first GraphQL engine with depth and complexity limiting"
```

---

### Task 13: Security Controls, GeoLite2 IP Filtering, Magic-Byte Uploads, ClamAV & hCaptcha

**Files:**
- Create: `internal/core/security/geoip.go`
- Create: `internal/core/security/upload.go`
- Create: `internal/core/security/clamav.go`
- Create: `internal/core/security/captcha.go`
- Create: `internal/core/security/security_test.go`

**Interfaces:**
- Consumes: MaxMind MMDB binary, ClamAV daemon TCP socket, hCaptcha secret.
- Produces:
  - Gin Middleware: `GeoIPFilter(dbPath, allowedCountries, unknownPolicy)`
  - `UploadService`: `ValidateAndSave(multipartFiles)` with magic-byte detection and ClamAV streaming.
  - `CaptchaVerifier`: `VerifyToken(ctx, token, clientIP) (bool, error)`.

- [ ] **Step 1: Write failing test for magic-byte file signature detection and geo-block**

```go
// internal/core/security/security_test.go
package security_test

import (
	"bytes"
	"mime/multipart"
	"testing"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/core/security"
)

func TestMagicByteInspectionRejectsSpoofedMIME(t *testing.T) {
	uploader := security.NewUploadService(security.UploadConfig{
		MaxFileSize:   5 * 1024 * 1024,
		AllowedMIMEs:  []string{"image/png", "image/jpeg"},
	})

	// Create fake file named photo.png containing shell script text
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "photo.png")
	_, _ = part.Write([]byte("#!/bin/bash\necho 'malicious'"))
	writer.Close()

	reader := multipart.NewReader(body, writer.Boundary())
	form, err := reader.ReadForm(1024)
	assert.NoError(t, err)

	_, err = uploader.ProcessFile(form.File["file"][0])
	assert.ErrorContains(t, err, "invalid file signature / magic bytes")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/security -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement Security Services in `internal/core/security/`**

- `geoip.go`: Load MaxMind GeoLite2 reader. Extract IP, look up ISO country code. Apply `block` or `allow` policy.
- `upload.go`: Check file size against `UPLOAD_MAX_FILE_SIZE` (5MB) and total payload `UPLOAD_MAX_TOTAL_SIZE` (20MB). Read first 512 bytes with `http.DetectContentType` to ensure true MIME matches declared header.
- `clamav.go`: Connect via TCP to ClamAV daemon (`INSTREAM` command). Stream chunks and verify `OK` response.
- `captcha.go`: Send verification request to `https://hcaptcha.com/siteverify` with secret and token.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/security -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/security/
git commit -m "feat(security): implement GeoLite2 filter, magic-byte upload validation, ClamAV, and hCaptcha"
```

---

### Task 14: Dynamic Feature Flag Engine with Local Evaluation & Redis Pub/Sub Invalidation

**Files:**
- Create: `internal/core/featureflag/engine.go`
- Create: `internal/core/featureflag/featureflag_test.go`

**Interfaces:**
- Consumes: `*redis.Client`, `config.FeatureFlagConfig`
- Produces: `FeatureFlagService`:
  - `IsEnabled(ctx context.Context, flagKey string, evalCtx EvaluationContext) (bool, error)`
  - `SetFlag(ctx context.Context, flagKey string, value bool, rules []Rule) error`
  - Invalidation listener on Redis topic `featureflags:invalidate`.

- [ ] **Step 1: Write failing test for local evaluation cache and pub/sub invalidation**

```go
// internal/core/featureflag/featureflag_test.go
package featureflag_test

import (
	"context"
	"testing"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/core/featureflag"
)

func TestFeatureFlagLocalCacheEvaluation(t *testing.T) {
	ff := featureflag.NewMemoryEngine()
	ctx := context.Background()

	ff.Set("new-checkout-engine", true)

	enabled, err := ff.IsEnabled(ctx, "new-checkout-engine", featureflag.Context{UserID: "usr-123"})
	assert.NoError(t, err)
	assert.True(t, enabled)

	// Invalidate flag
	ff.Invalidate("new-checkout-engine")
	ff.Set("new-checkout-engine", false)

	enabledAfter, err := ff.IsEnabled(ctx, "new-checkout-engine", featureflag.Context{UserID: "usr-123"})
	assert.NoError(t, err)
	assert.False(t, enabledAfter)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/featureflag -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement Feature Flag Engine in `internal/core/featureflag/engine.go`**

- In-memory `sync.RWMutex` cache for fast sub-microsecond evaluations.
- Redis client backing for persistent cross-node flag configurations.
- Goroutine listening on Redis Pub/Sub topic `featureflags:invalidate:<env>` to evict local cache entries immediately on write.
- Background reconciliation ticker (`FEATURE_FLAG_RECONCILE_MS` = 45s) to guarantee eventual consistency if a pub/sub message was dropped.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/featureflag -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/featureflag/
git commit -m "feat(featureflag): add dynamic feature flag engine with Redis pub/sub invalidation"
```

---

### Task 15: Fail-Open Asynchronous Notification Service (Slack Webhook)

**Files:**
- Create: `internal/core/notification/slack.go`
- Create: `internal/core/notification/notification_test.go`

**Interfaces:**
- Consumes: `SLACK_WEBHOOK_URL`, resilient `httpclient.ResilientClient`.
- Produces: `NotificationService`:
  - `Send(ctx context.Context, notification NotificationPayload) error`
  - Guarantees fail-open behavior: logs warnings on failures but never returns blocking errors to callers.

- [ ] **Step 1: Write failing test for fail-open notification dispatch**

```go
// internal/core/notification/notification_test.go
package notification_test

import (
	"context"
	"testing"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/core/notification"
)

func TestNotificationFailOpenOnBrokenWebhook(t *testing.T) {
	// Points to invalid endpoint
	svc := notification.NewSlackService("http://127.0.0.1:9999/broken-webhook")

	// Must return nil error or log warning, preserving transaction integrity
	err := svc.Send(context.Background(), notification.Payload{
		Title:   "Security Alert: Suspicious Login",
		Message: "Failed login attempt",
		Level:   "warning",
	})
	assert.NoError(t, err, "Notification failure must not break calling transaction (fail-open)")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/notification -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement Slack Notification Service in `internal/core/notification/slack.go`**

- Construct Slack Block Kit / Attachment JSON payload.
- Dispatch asynchronously in a worker pool or bounded channel to decouple caller latency.
- Handle non-2xx responses by logging structured warning without propagating error to transaction.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/notification -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/notification/
git commit -m "feat(notification): implement fail-open asynchronous Slack notification service"
```

---

### Task 16: Observability, CloudWatch EMF, OpenTelemetry Tracing & Cached Health Probes

**Files:**
- Create: `internal/core/telemetry/health.go`
- Create: `internal/core/telemetry/otel.go`
- Create: `internal/core/telemetry/emf.go`
- Create: `internal/core/telemetry/telemetry_test.go`

**Interfaces:**
- Consumes: `*gorm.DB`, `*redis.Client`, `*mongo.Client`, `config.TelemetryConfig`.
- Produces:
  - Gin Endpoints:
    - `GET /health`: Comprehensive probe checking Postgres, Redis, Mongo, disk, and memory. Responses cached for 2s (`HEALTH_CHECK_CACHE_TTL_MS`) to mitigate thundering herds.
    - `GET /health/ready`: Returns 200 during normal operation, 503 during SIGTERM graceful shutdown draining.
    - `GET /health/live`: Fast in-memory 200 check.
  - OpenTelemetry OTLP Tracer Provider with X-Ray propagator.
  - CloudWatch EMF JSON log formatter emitting metrics on 503 overload events.

- [ ] **Step 1: Write failing test for health probe caching and readiness during shutdown**

```go
// internal/core/telemetry/telemetry_test.go
package telemetry_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/core/telemetry"
)

func TestHealthCheckCacheMitigatesThunderingHerd(t *testing.T) {
	checker := telemetry.NewHealthChecker(telemetry.Config{
		CacheTTL: 2 * time.Second,
	})

	router := gin.New()
	checker.RegisterRoutes(router)

	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodGet, "/health", nil)
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Simulate transition to draining shutdown
	checker.SetDraining(true)

	// Readiness probe must now return 503
	wReady := httptest.NewRecorder()
	reqReady, _ := http.NewRequest(http.MethodGet, "/health/ready", nil)
	router.ServeHTTP(wReady, reqReady)
	assert.Equal(t, http.StatusServiceUnavailable, wReady.Code)

	// Liveness probe continues returning 200
	wLive := httptest.NewRecorder()
	reqLive, _ := http.NewRequest(http.MethodGet, "/health/live", nil)
	router.ServeHTTP(wLive, reqLive)
	assert.Equal(t, http.StatusOK, wLive.Code)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/telemetry -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement Telemetry and Probes in `internal/core/telemetry/`**

- `health.go`: Ping database connections concurrently (`errgroup`). Cache aggregate status in memory with 2s TTL.
- `otel.go`: Initialize OpenTelemetry OTLP exporter, W3C TraceContext + AWS X-Ray propagators.
- `emf.go`: CloudWatch Embedded Metric Format writer printing `{"_aws": {"Timestamp": ..., "CloudWatchMetrics": [...]}}` JSON logs.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/telemetry -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/telemetry/
git commit -m "feat(telemetry): add OTel tracing, EMF metrics, and cached health/readiness/liveness probes"
```

---

### Task 17: User Domain Module, Integration Wiring & Quality Verification

**Files:**
- Create: `internal/modules/account/model.go`
- Create: `internal/modules/account/repository.go`
- Create: `internal/modules/account/handler.go`
- Create: `cmd/api/main.go`
- Create: `cmd/worker/main.go`
- Create: `migrations/000001_create_users_table.up.sql`
- Create: `migrations/000001_create_users_table.down.sql`
- Create: `internal/modules/account/account_test.go`

**Interfaces:**
- Consumes: All core components, middleware, and platform adapters.
- Produces: Complete working Gin router wiring with `/account/register`, `/account/login`, `/account/change-password`, `/health/*`, `/graphql`, and `/realtime`.

- [ ] **Step 1: Write integration test for registration, login, and protected route access**

```go
// internal/modules/account/account_test.go
package account_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/modules/account"
)

func TestAccountRegisterAndLoginFlow(t *testing.T) {
	router := account.SetupTestRouter(t)

	// Register public route
	regPayload := map[string]string{
		"email":    "test@example.com",
		"password": "Password123!@#",
	}
	body, _ := json.Marshal(regPayload)
	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(body))
	reqReg.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReg, reqReg)

	assert.Equal(t, http.StatusCreated, wReg.Code)

	// Login
	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(body))
	reqLogin.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLogin, reqLogin)

	assert.Equal(t, http.StatusOK, wLogin.Code)
	var loginResp map[string]string
	err := json.Unmarshal(wLogin.Body.Bytes(), &loginResp)
	require.NoError(t, err)
	assert.NotEmpty(t, loginResp["accessToken"])

	// Protected change password route with Bearer token
	changePayload := map[string]string{
		"oldPassword": "Password123!@#",
		"newPassword": "NewPassword123!@#",
	}
	changeBody, _ := json.Marshal(changePayload)
	wChange := httptest.NewRecorder()
	reqChange, _ := http.NewRequest(http.MethodPost, "/account/change-password", bytes.NewReader(changeBody))
	reqChange.Header.Set("Content-Type", "application/json")
	reqChange.Header.Set("Authorization", "Bearer "+loginResp["accessToken"])
	router.ServeHTTP(wChange, reqChange)

	assert.Equal(t, http.StatusOK, wChange.Code)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/modules/account -v`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement Account Module and wire API in `cmd/api/main.go`**

- Wire GORM User repository.
- Implement register, login, refresh handlers.
- Wire all Gin routes with global middleware stack: RequestID, StructuredLogger, Recovery, SecurityHeaders, GzipCompression, RequestDeadline, and CORS.

- [ ] **Step 4: Run full verification suite**

Run: `go test ./... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/modules/account/ cmd/api/main.go migrations/000001_create_users_table.up.sql
git commit -m "feat(account): implement user domain module and complete API server wiring"
```

---

## Self-Review Checklist

1. **Spec Coverage:**
   - Section 1 (Core HTTP Server & Fastify Runtime) -> Covered in Task 2 & Task 3 (ALB keep-alive, graceful shutdown, request deadline propagation, compression, CORS).
   - Section 2 (Authentication & Authorization) -> Covered in Task 5 (RS256 JWT, Argon2id CPU bulkhead, Redis JTI revocation, CSRF).
   - Section 3 (Multi-Store Persistence) -> Covered in Task 4 (GORM Postgres, Mongo lazy client, Redis).
   - Section 4 (Rate Limiting) -> Covered in Task 7 (IP, User ID, Device Fingerprint, Guest Cookie, Redis/memory fallback).
   - Section 5 (Inbound Overload & Priority Shedding) -> Covered in Task 6 (Concurrency bulkhead, `@ApiPriority` shedding, Retry-After).
   - Section 6 (Resilient Outbound HTTP Client) -> Covered in Task 9 (Exponential jitter retry, circuit breaker, SSRF private IP guard).
   - Section 7 (Caching Layer & Stampede Protection) -> Covered in Task 8 (SWR, singleflight coalescing, Redis NX locks, TTL jitter, negative caching).
   - Section 8 (Background Jobs Engine) -> Covered in Task 10 (Postgres ledger with `FOR UPDATE SKIP LOCKED`, deduplication key, orphan lease reclaim).
   - Section 9 (Real-Time WebSockets) -> Covered in Task 11 (Gorilla WebSocket, Redis pub/sub broadcasting, per-user connection cap, packet rate limiter).
   - Section 10 (GraphQL API) -> Covered in Task 12 (gqlgen, query depth limiter, complexity budget).
   - Section 11 (Security, GeoIP & File Uploads) -> Covered in Task 13 (GeoLite2 filtering, magic-byte inspection, ClamAV, hCaptcha).
   - Section 12 (Observability & Telemetry) -> Covered in Task 3 & Task 16 (OTel tracing, CloudWatch EMF, PII redaction, /health, /health/ready, /health/live with 2s cache).
   - Section 13 (Dynamic Feature Flags) -> Covered in Task 14 (Redis store, local memory cache, pub/sub cache eviction, background reconciliation).
   - Section 14 (Notifications Service) -> Covered in Task 15 (Fail-open Slack webhook).
   - Section 15 (Testing & Quality Infrastructure) -> Covered in Task 1 & Task 17 (golangci-lint, unit/integration tests).

2. **Step Scan:** Every task contains 5 structured steps (Failing test, verify failure, implement, verify pass, commit) with unambiguous types and commands.
3. **Type Consistency:** Structs (`Config`, `TokenManager`, `ResilientClient`, `OverloadManager`, `RateLimiter`) and methods match across tasks.
4. **Review Focus:** The 5 top failure modes (ALB 502 keep-alive, Argon2id CPU exhaustion, cache stampedes, SSRF, and orphaned job leases) have designated owning tasks and tests.
5. **Proportion:** Clean balance of specifications, interface contracts, and precise testing commands without excessive code transcripts.
