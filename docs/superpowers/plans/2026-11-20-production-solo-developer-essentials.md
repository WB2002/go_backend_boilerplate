# Production Solo Developer Essentials Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the top 5 production essentials for solo developer velocity and resiliency: embedded Goose migration CLI, RFC 7807 Problem Details error mapper, OpenAPI Swagger UI & SDK generator, pluggable transactional email service with password reset flow, and Redis-backed HTTP Idempotency-Key middleware.

**Architecture:** Extend the existing clean modular architecture (`internal/core`, `internal/platform`, `internal/modules`) with zero-downtime operational tooling. Database migrations become runnable via CLI and programmatic boot checks using Goose; API errors and validation failures normalize into RFC 7807 Problem Details; endpoints gain Swagger OpenAPI specifications for instant frontend SDK generation; authentication completes the loop with a transactional mailer and password reset flow; mutating HTTP handlers receive distributed idempotency caching via Redis.

**Tech Stack:** Go 1.23+, Gin (`github.com/gin-gonic/gin`), GORM (`gorm.io/gorm`), Go-Redis v9 (`github.com/redis/go-redis/v9`), Goose v3 (`github.com/pressly/goose/v3`), Swag (`github.com/swaggo/gin-swagger`, `github.com/swaggo/files`), Zerolog.

**Spec:** [docs/product-brief.md](file:///Users/pc/Documents/personal/go_backend_boilerplate/docs/product-brief.md) & [docs/superpowers/plans/2026-11-19-go-gin-gorm-boilerplate.md](file:///Users/pc/Documents/personal/go_backend_boilerplate/docs/superpowers/plans/2026-11-19-go-gin-gorm-boilerplate.md).

## Global Constraints

- **Language & Runtime:** Go 1.23+ with strict Go modules (`go.mod`).
- **No Global Mutable State:** All configurations and clients passed via explicit constructor dependency injection.
- **Strict Context Propagation:** All database, network, cache, and long-running calls must accept and respect `context.Context`.
- **Fail-Safe & Idempotent:** Errors conform to standard RFC 7807 without leaking internal traces; network retries on mutating endpoints remain safe.
- **Test Integrity:** Every behavior change has an automated test; `./scripts/verify.sh` must pass cleanly after every task.

## Review Focus

1. **Concurrent Idempotent Mutex Race:** Two identical requests with the same `Idempotency-Key` arriving concurrently at the exact same millisecond must not execute twice; the second request must receive a 409 Conflict or wait for the first response envelope. *Owned by Task 5.*
2. **Password Reset Token Enumeration & Timing Attacks:** Password reset requests for non-existent emails must return the same generic 200/202 status and constant-time dummy latency to prevent user enumeration. *Owned by Task 4.*
3. **Goose Migration Lock Contention on Scaled API Replicas:** Multiple API instances booting concurrently must not corrupt migration version tables when running automatic schema verification. *Owned by Task 1.*
4. **Validation Error Sensitivity & Path Exposure:** RFC 7807 validation error mappers must not leak internal SQL column names, database driver errors, or internal struct pointer hierarchies to API clients. *Owned by Task 2.*
5. **OpenAPI Schema Divergence:** Generated Swagger definitions must precisely mirror Gin route parameters, authentication headers (`Bearer <token>`), and response payloads to prevent broken frontend client SDKs. *Owned by Task 3.*

---

### Task 1: Embedded Database Migration CLI & Runtime Runner (`goose`)

**Files:**
- Create: `internal/platform/database/migrate.go`
- Create: `cmd/migrate/main.go`
- Create: `internal/platform/database/migrate_test.go`
- Modify: `Makefile:1-36`
- Modify: `internal/platform/database/postgres.go:1-60`

**Interfaces:**
- Consumes: `config.DatabaseConfig`, `gorm.DB.DB()` (`*sql.DB`)
- Produces: `database.RunMigrations(db *sql.DB, migrationsDir string, command string, args ...string) error`

- [ ] **Step 1: Write the failing test**

```go
// internal/platform/database/migrate_test.go
package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestGooseMigrationLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	migFile := filepath.Join(tempDir, "00001_create_test_table.sql")
	content := `-- +goose Up
CREATE TABLE items (id TEXT PRIMARY KEY, name TEXT NOT NULL);
-- +goose Down
DROP TABLE items;
`
	err := os.WriteFile(migFile, []byte(content), 0644)
	require.NoError(t, err)

	db, err := gorm.Open(sqlite.Open(filepath.Join(tempDir, "test.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	// Up
	err = RunMigrations(sqlDB, "sqlite3", tempDir, "up")
	assert.NoError(t, err)

	// Status
	currentVer, err := GetCurrentVersion(sqlDB, "sqlite3")
	assert.NoError(t, err)
	assert.Equal(t, int64(1), currentVer)

	// Down
	err = RunMigrations(sqlDB, "sqlite3", tempDir, "down")
	assert.NoError(t, err)

	currentVerDown, err := GetCurrentVersion(sqlDB, "sqlite3")
	assert.NoError(t, err)
	assert.Equal(t, int64(0), currentVerDown)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/platform/database -run TestGooseMigrationLifecycle`
Expected: FAIL with undefined: `RunMigrations`, `GetCurrentVersion`

- [ ] **Step 3: Add goose dependency and implement `migrate.go` and `cmd/migrate/main.go`**

1. Run `go get github.com/pressly/goose/v3`
2. Implement in `internal/platform/database/migrate.go`:
   - `RunMigrations(db *sql.DB, dialect, migrationsDir, command string, args ...string) error`: Wraps `goose.SetDialect(dialect)` and dispatches commands (`up`, `down`, `status`, `version`, `reset`).
   - `GetCurrentVersion(db *sql.DB, dialect string) (int64, error)`: Calls `goose.GetDBVersion(db)`.
3. Create standalone CLI entrypoint `cmd/migrate/main.go` that loads `config.Load()`, connects to Postgres, and parses CLI commands (`up`, `down`, `status`, `create <name> sql`).
4. Update `Makefile` with targets:
   - `migrate-up`: `go run ./cmd/migrate up`
   - `migrate-down`: `go run ./cmd/migrate down`
   - `migrate-status`: `go run ./cmd/migrate status`
   - `migrate-create`: `go run ./cmd/migrate create $(name) sql`

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/platform/database -run TestGooseMigrationLifecycle`
Expected: PASS

- [ ] **Step 5: Run verify and commit**

```bash
./scripts/verify.sh
git add go.mod go.sum internal/platform/database/ cmd/migrate/ Makefile
git commit -m "feat(database): add embedded goose migration runner and CLI"
```

---

### Task 2: RFC 7807 Problem Details & Field Validation Mapper

**Files:**
- Create: `internal/core/problem/problem.go`
- Create: `internal/core/problem/validation.go`
- Create: `internal/core/problem/problem_test.go`
- Modify: `internal/core/middleware/recovery.go:25-45`
- Modify: `internal/modules/account/handler.go:55-120`

**Interfaces:**
- Consumes: `github.com/go-playground/validator/v10`
- Produces:
  - `problem.ProblemDetails` (struct with JSON tags matching RFC 7807: `type`, `title`, `status`, `detail`, `instance`, `code`, `invalid_params`)
  - `problem.InvalidParam` (`field`, `reason`, `code`)
  - `problem.Respond(c *gin.Context, status int, code, title, detail string)`
  - `problem.RespondValidation(c *gin.Context, err error)`
  - `problem.FromError(err error) *ProblemDetails`

- [ ] **Step 1: Write the failing test**

```go
// internal/core/problem/problem_test.go
package problem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type SampleRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

func TestProblemDetailsFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	r.GET("/test-error", func(c *gin.Context) {
		Respond(c, http.StatusNotFound, "USER_NOT_FOUND", "User Not Found", "The user with id 123 was not found")
	})

	r.POST("/test-validation", func(c *gin.Context) {
		var req SampleRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			RespondValidation(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	// Test Standard Problem
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test-error", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "application/problem+json; charset=utf-8", w.Header().Get("Content-Type"))

	var p ProblemDetails
	err := json.Unmarshal(w.Body.Bytes(), &p)
	require.NoError(t, err)
	assert.Equal(t, "USER_NOT_FOUND", p.Code)
	assert.Equal(t, "User Not Found", p.Title)
	assert.Equal(t, http.StatusNotFound, p.Status)
	assert.Equal(t, "The user with id 123 was not found", p.Detail)

	// Test Validation Error Mapping
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/test-validation", strings.NewReader(`{"email":"bad-email","password":"short"}`))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusUnprocessableEntity, w2.Code)
	var valProblem ProblemDetails
	err = json.Unmarshal(w2.Body.Bytes(), &valProblem)
	require.NoError(t, err)
	assert.Equal(t, "VALIDATION_FAILED", valProblem.Code)
	assert.Len(t, valProblem.InvalidParams, 2)
	assert.Equal(t, "email", valProblem.InvalidParams[0].Field)
	assert.Equal(t, "password", valProblem.InvalidParams[1].Field)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/problem -run TestProblemDetailsFormat`
Expected: FAIL with undefined package `problem`

- [ ] **Step 3: Implement `problem.go` and `validation.go`**

1. Create `internal/core/problem/problem.go`:
   - Define `ProblemDetails` struct adhering to RFC 7807 + custom `code` string and `invalid_params []InvalidParam`.
   - Implement `Respond(c *gin.Context, status int, code, title, detail string)`.
   - Set Content-Type to `application/problem+json; charset=utf-8`.
2. Create `internal/core/problem/validation.go`:
   - Inspect `validator.ValidationErrors`.
   - Translate field names (using JSON tag lookup) and validator tags (`required`, `email`, `min`, `max`, `uuid`) into human-readable descriptions.
   - Implement `RespondValidation(c *gin.Context, err error)` returning HTTP 422 Unprocessable Entity.
3. Update `internal/core/middleware/recovery.go` to return RFC 7807 for unhandled panics (`INTERNAL_SERVER_ERROR`).
4. Update `internal/modules/account/handler.go` to use `problem.RespondValidation` on bind failures and `problem.Respond` for business errors.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/problem -run TestProblemDetailsFormat`
Expected: PASS

- [ ] **Step 5: Run verify and commit**

```bash
./scripts/verify.sh
git add internal/core/problem/ internal/core/middleware/recovery.go internal/modules/account/handler.go
git commit -m "feat(errors): add RFC 7807 Problem Details and validator mapper"
```

---

### Task 3: OpenAPI (Swagger 2.0 / 3.0) Spec Generation & Frontend SDK Hook

**Files:**
- Create: `docs/swagger/` (generated swagger artifacts)
- Create: `cmd/api/docs.go` (or annotations in `cmd/api/main.go`)
- Modify: `cmd/api/main.go:200-220`
- Modify: `internal/modules/account/handler.go:40-150`
- Modify: `Makefile:25-36`

**Interfaces:**
- Consumes: Swag CLI (`github.com/swaggo/swag/cmd/swag`), Gin engine
- Produces:
  - `GET /swagger/*any` -> Swagger UI web interface
  - `GET /swagger/doc.json` -> OpenAPI JSON spec for client generators
  - `make swagger` -> Re-generates docs from code comments
  - `make sdk` -> Runs OpenAPI client generator (e.g. `npx @openapitools/openapi-generator-cli`)

- [ ] **Step 1: Write the failing test**

```go
// cmd/api/swagger_test.go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestSwaggerEndpointMount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	
	registerSwagger(r)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/swagger/index.html", nil)
	r.ServeHTTP(w, req)

	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusMovedPermanently)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./cmd/api -run TestSwaggerEndpointMount`
Expected: FAIL with undefined: `registerSwagger`

- [ ] **Step 3: Install swag dependencies, annotate handlers, and wire endpoint**

1. Install `github.com/swaggo/gin-swagger` and `github.com/swaggo/files`.
2. Add General API annotations to `cmd/api/main.go`:
   - `@title Go Backend Boilerplate API`
   - `@version 1.0`
   - `@description Production-grade Go backend API.`
   - `@securityDefinitions.apikey BearerAuth`
   - `@in header`
   - `@name Authorization`
3. Add Swagger doc comments to `internal/modules/account/handler.go` for:
   - `Register`, `Login`, `Refresh`, `Me`, `ChangePassword`
   - Document `@Summary`, `@Tags`, `@Accept json`, `@Produce json`, `@Param`, `@Success`, `@Failure` referencing `problem.ProblemDetails`.
4. Implement `registerSwagger(r *gin.Engine)` in `cmd/api/main.go` mounting `ginSwagger.WrapHandler(swaggerFiles.Handler)`.
5. Add `make swagger` target to `Makefile`: `swag init -g cmd/api/main.go -o docs/swagger`.

- [ ] **Step 4: Run test to verify it passes**

Run: `swag init -g cmd/api/main.go -o docs/swagger && go test -v ./cmd/api -run TestSwaggerEndpointMount`
Expected: PASS

- [ ] **Step 5: Run verify and commit**

```bash
./scripts/verify.sh
git add go.mod go.sum cmd/api/ docs/swagger/ internal/modules/account/ Makefile
git commit -m "feat(api): add OpenAPI swagger spec generation and Swagger UI"
```

---

### Task 4: Pluggable Transactional Email Service & Password Reset Flow

**Files:**
- Create: `internal/core/email/mailer.go`
- Create: `internal/core/email/smtp.go`
- Create: `internal/core/email/mock.go`
- Create: `internal/core/email/email_test.go`
- Create: `migrations/000003_create_password_resets_table.up.sql`
- Create: `migrations/000003_create_password_resets_table.down.sql`
- Modify: `internal/config/config.go:120-160`
- Modify: `internal/modules/account/model.go:80-120`
- Modify: `internal/modules/account/repository.go:20-80`
- Modify: `internal/modules/account/handler.go:40-200`
- Modify: `cmd/api/main.go:140-180`

**Interfaces:**
- Consumes: `config.EmailConfig`, `Repository`, `PasswordHasher`
- Produces:
  - `email.Mailer` interface: `Send(ctx context.Context, msg Message) error`
  - `POST /account/forgot-password` (accepts email, generates token, sends email, always returns 200/202)
  - `POST /account/reset-password` (validates 15-min token, hashes new password, updates user, invalidates token)

- [ ] **Step 1: Write the failing test**

```go
// internal/modules/account/password_reset_test.go
package account

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/core/email"
)

func TestForgotPasswordAndResetPasswordFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newMockRepository()
	hasher := &mockHasher{}
	tokenMgr := &mockTokenManager{}
	mockMailer := email.NewMockMailer()

	user := &User{
		ID:           "test-user-id",
		Email:        "reset@example.com",
		PasswordHash: "oldhash",
		Status:       "active",
	}
	_ = repo.Create(context.Background(), user)

	h := NewHandlerWithMailer(repo, hasher, tokenMgr, nil, mockMailer)
	r := gin.New()
	h.RegisterRoutes(r, func(c *gin.Context) {})

	// 1. Request password reset
	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"email": "reset@example.com"})
	req, _ := http.NewRequest("POST", "/account/forgot-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, mockMailer.SentMessages, 1)
	assert.Equal(t, "reset@example.com", mockMailer.SentMessages[0].To)
	resetToken := mockMailer.SentMessages[0].Data["Token"].(string)
	require.NotEmpty(t, resetToken)

	// 2. Perform password reset
	w2 := httptest.NewRecorder()
	resetBody, _ := json.Marshal(map[string]string{
		"token":        resetToken,
		"new_password": "newSecurePassword123!",
	})
	req2, _ := http.NewRequest("POST", "/account/reset-password", bytes.NewReader(resetBody))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)

	// Verify token cannot be re-used
	w3 := httptest.NewRecorder()
	req3, _ := http.NewRequest("POST", "/account/reset-password", bytes.NewReader(resetBody))
	req3.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w3, req3)
	assert.Equal(t, http.StatusBadRequest, w3.Code)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/modules/account -run TestForgotPasswordAndResetPasswordFlow`
Expected: FAIL with undefined: `NewHandlerWithMailer`, `email.NewMockMailer`

- [ ] **Step 3: Implement `email` package and password reset logic**

1. Create `internal/core/email/mailer.go`:
   - `Message` struct (`To`, `Subject`, `TemplateName`, `Data map[string]any`).
   - `Mailer` interface.
2. Create `internal/core/email/smtp.go` and `internal/core/email/mock.go`.
3. Add migration `000003_create_password_resets_table.up.sql`:
   - Columns: `id UUID`, `user_id UUID`, `token_hash VARCHAR(64) NOT NULL UNIQUE`, `expires_at TIMESTAMPTZ NOT NULL`, `used_at TIMESTAMPTZ`, `created_at TIMESTAMPTZ`.
4. Update `internal/modules/account/repository.go`:
   - Add `CreatePasswordResetToken`, `FindValidPasswordResetToken`, and `MarkPasswordResetTokenUsed`.
5. Update `internal/modules/account/handler.go`:
   - Add `ForgotPassword(c *gin.Context)` (constant-time, dummy delay if user not found to prevent timing attack enumeration).
   - Add `ResetPassword(c *gin.Context)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/modules/account -run TestForgotPasswordAndResetPasswordFlow`
Expected: PASS

- [ ] **Step 5: Run verify and commit**

```bash
./scripts/verify.sh
git add internal/core/email/ migrations/ internal/config/ internal/modules/account/ cmd/api/
git commit -m "feat(auth): add pluggable mailer and password reset flow"
```

---

### Task 5: HTTP Idempotency-Key Middleware for Mutating Endpoints

**Files:**
- Create: `internal/core/middleware/idempotency.go`
- Create: `internal/core/middleware/idempotency_test.go`
- Modify: `cmd/api/main.go:180-210`

**Interfaces:**
- Consumes: Redis Client (`*redis.Client` from `github.com/redis/go-redis/v9`)
- Produces:
  - `middleware.Idempotency(rdb *redis.Client, ttl time.Duration) gin.HandlerFunc`
  - Replays cached responses (Status, Headers, Body) for duplicate requests with the same `Idempotency-Key`.
  - Rejects concurrent requests processing with the same key using `409 Conflict`.

- [ ] **Step 1: Write the failing test**

```go
// internal/core/middleware/idempotency_test.go
package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/platform/redis"
)

func TestIdempotencyMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rdb := redis.NewMockClient() // or miniredis/redis client

	r := gin.New()
	var executionCount int64

	r.Use(Idempotency(rdb, 10*time.Minute))
	r.POST("/orders", func(c *gin.Context) {
		atomic.AddInt64(&executionCount, 1)
		time.Sleep(20 * time.Millisecond) // simulate work
		c.JSON(http.StatusCreated, gin.H{"order_id": "ord-12345", "status": "processed"})
	})

	idempotencyKey := "req-uuid-abc-123"

	// 1. Initial request
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("POST", "/orders", bytes.NewReader([]byte(`{"amount": 100}`)))
	req1.Header.Set("Idempotency-Key", idempotencyKey)
	r.ServeHTTP(w1, req1)

	assert.Equal(t, http.StatusCreated, w1.Code)
	assert.Contains(t, w1.Body.String(), "ord-12345")
	assert.Equal(t, int64(1), atomic.LoadInt64(&executionCount))

	// 2. Exact duplicate request replay
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/orders", bytes.NewReader([]byte(`{"amount": 100}`)))
	req2.Header.Set("Idempotency-Key", idempotencyKey)
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusCreated, w2.Code)
	assert.Equal(t, w1.Body.String(), w2.Body.String())
	assert.Equal(t, "true", w2.Header().Get("X-Cache-Lookup"))
	// Handler must NOT have executed a second time
	assert.Equal(t, int64(1), atomic.LoadInt64(&executionCount))
}

func TestIdempotencyConcurrentConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rdb := redis.NewMockClient()

	r := gin.New()
	r.Use(Idempotency(rdb, 10*time.Minute))
	r.POST("/mutate", func(c *gin.Context) {
		time.Sleep(100 * time.Millisecond)
		c.Status(http.StatusOK)
	})

	var wg sync.WaitGroup
	codes := make([]int, 2)
	idempotencyKey := "concurrent-key-999"

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/mutate", nil)
			req.Header.Set("Idempotency-Key", idempotencyKey)
			r.ServeHTTP(w, req)
			codes[idx] = w.Code
		}(i)
	}
	wg.Wait()

	// One must succeed (200) and the other must be rejected (409 Conflict) while in flight
	assert.Contains(t, codes, http.StatusOK)
	assert.Contains(t, codes, http.StatusConflict)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/middleware -run TestIdempotencyMiddleware`
Expected: FAIL with undefined: `Idempotency`

- [ ] **Step 3: Implement `idempotency.go`**

1. Create `internal/core/middleware/idempotency.go`:
   - Inspect request method: skip for `GET`, `HEAD`, `OPTIONS`.
   - Inspect `Idempotency-Key` header: if missing, call `c.Next()`.
   - Lock key in Redis using `SET NX key "IN_PROGRESS" EX <ttl>`.
   - If key exists with `"IN_PROGRESS"`, return HTTP 409 Conflict with RFC 7807 problem details ("A request with this idempotency key is currently being processed").
   - If key exists with serialized response payload (`status`, `headers`, `body`), short-circuit `c.Abort()` and stream cached response directly with `X-Cache-Lookup: true`.
   - Wrap `gin.ResponseWriter` with a custom buffer recorder (`bodyBuffer`, `statusCode`).
   - After `c.Next()`, if status code is successful (`< 500`), serialize and persist response in Redis under `Idempotency-Key` with TTL. If status was 5xx, delete the key to allow client retry.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/middleware -run TestIdempotencyMiddleware`
Expected: PASS

- [ ] **Step 5: Run verify and commit**

```bash
./scripts/verify.sh
git add internal/core/middleware/idempotency* cmd/api/main.go
git commit -m "feat(middleware): add Redis-backed Idempotency-Key middleware"
```
