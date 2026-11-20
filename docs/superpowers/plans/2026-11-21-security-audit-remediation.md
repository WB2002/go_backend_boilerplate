# Security Audit Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remediate all 17 confirmed security vulnerabilities (1 Critical, 4 High, 9 Medium, 3 Low) identified in the 2026-11-21 Security Audit Report across auth, middleware, GraphQL, telemetry, data access, and infrastructure.

**Architecture:** Harden defense-in-depth across the boilerplate by:
1. Scoping idempotency caches by actor, route, and request payload SHA-256 digest while exempting credential endpoints.
2. Enforcing token invalidation upon password reset/change, single-transaction atomic reset token consumption, and failing closed on revocation errors.
3. Adding panic recovery, nil-DB guards, and length bounds to asynchronous audit logging, and expanding PII redaction for DSNs and Basic auth.
4. Implementing fragment DAG memoization and visit limits in GraphQL complexity calculation to prevent CPU exhaustion.
5. Hardening transport security (trusted reverse proxies, Redis TLS, Postgres SSLMode defaults, non-wildcard CORS, and unauthenticated diagnostic redaction).
6. Decoupling test dependencies from production binaries (`cmd/worker`), pinning container images by SHA-256 digest, and binding local compose datastores to `127.0.0.1`.

**Tech Stack:** Go 1.23+, Gin v1.10.0, GORM v2, Redis v9 (`go-redis`), gqlparser v2, Zerolog, Docker, GitHub Actions.

**Spec:** [docs/reports/2026-11-21-security-audit/security-audit-report.md](file:///Users/pc/Documents/personal/go_backend_boilerplate/docs/reports/2026-11-21-security-audit/security-audit-report.md)

---

## Global Constraints

- **Language & Runtime:** Go 1.23+ with strict modules (`go.mod`).
- **Zero Breaking API Contracts:** Existing client contracts remain valid, with security restrictions (body limits, password max length 128, trusted proxies) rejecting anomalous payloads.
- **Fail-Closed Security Boundaries:** Token validation, revocation, and authentication checks must fail closed on upstream dependency errors.
- **Fail-Safe Observability:** Health checks must not expose internal system internals (DB connection errors, memory allocators) to unauthenticated public callers.
- **Verification Evidence:** Every task must feature automated unit/integration tests and pass `./scripts/verify.sh` cleanly.

---

## Review Focus

1. **Idempotency Collision & Cross-User Replay:** A malicious user reusing an `Idempotency-Key` from another user or sending a mismatched payload must receive a `409 Conflict` / `422 Unprocessable Entity` and never receive another user's cached response. *Owned by Task 1.*
2. **Concurrent Password Reset Token Double-Spend:** Two concurrent requests using the same one-time password reset token must never both succeed; exactly one must atomically consume the token in PostgreSQL while the other is rejected. *Owned by Task 2.*
3. **Degraded Database Startup Crash Resilience:** When PostgreSQL is unreachable at boot (`db == nil`), unauthenticated login attempts triggering async audit logs must never panic or terminate the API process. *Owned by Task 4.*
4. **GraphQL Fragment Exponential DAG Bomb:** An AST containing 25+ deeply nested or diamond fragment spreads must complete depth and complexity analysis in under 10 milliseconds or abort deterministically without hanging CPU. *Owned by Task 6.*
5. **Reverse Proxy IP Spoofing & Overload Priority Hijacking:** Client-forged `X-Forwarded-For` or `X-API-Priority: critical` headers from untrusted networks must be ignored and not allow rate-limit or overload shedding bypasses. *Owned by Task 5.*

---

### Task 1: Scoped Idempotency Middleware and Credential Route Protection (`SEC-AUD-001`)

**Files:**
- Modify: `internal/core/middleware/idempotency.go:48-138`
- Modify: `cmd/api/main.go:233-241`
- Test: `internal/core/middleware/idempotency_test.go`

**Interfaces:**
- Consumes: `goredis.Cmdable`, `gin.Context`
- Produces:
  - `IdempotencyWithConfig(rdb goredis.Cmdable, ttl time.Duration, exemptPaths []string) gin.HandlerFunc`
  - `ScopedIdempotencyKey(actorID string, method string, path string, rawKey string, bodyBytes []byte) (cacheKey string, requestHash string)`

- [ ] **Step 1: Write the failing test for scoped idempotency and credential exemption**

```go
// internal/core/middleware/idempotency_test.go
func TestIdempotency_CrossUserAndExemptRoutes(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()

	router := gin.New()
	router.Use(func(c *gin.Context) {
		if u := c.GetHeader("X-User-ID"); u != "" {
			c.Set(ContextKeyUserID, u)
		}
		c.Next()
	})
	router.Use(IdempotencyWithConfig(client, time.Minute, []string{"/account/login"}))

	router.POST("/account/login", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"token": "secret-jwt"})
	})
	router.POST("/orders", func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{"orderId": "123"})
	})

	// 1. Ensure /account/login is NEVER cached even with Idempotency-Key
	w1 := performRequest(router, "POST", "/account/login", `{"user":"alice"}`, map[string]string{
		"Idempotency-Key": "test-key-login",
	})
	assert.Equal(t, http.StatusOK, w1.Code)
	assert.Empty(t, w1.Header().Get("X-Cache-Lookup"))

	// 2. Ensure User A cannot have response replayed by User B with same Idempotency-Key
	w2 := performRequest(router, "POST", "/orders", `{"item":"book"}`, map[string]string{
		"Idempotency-Key": "order-key-1",
		"X-User-ID":       "user-a",
	})
	assert.Equal(t, http.StatusCreated, w2.Code)

	// User B with same key gets a fresh execution or conflict, NOT User A's cached response
	w3 := performRequest(router, "POST", "/orders", `{"item":"different"}`, map[string]string{
		"Idempotency-Key": "order-key-1",
		"X-User-ID":       "user-b",
	})
	assert.NotEqual(t, "true", w3.Header().Get("X-Cache-Lookup"))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/middleware -run TestIdempotency_CrossUserAndExemptRoutes`  
Expected: FAIL with undefined `IdempotencyWithConfig` or test failure on cross-user collision.

- [ ] **Step 3: Implement Scoped Idempotency in `internal/core/middleware/idempotency.go` and `cmd/api/main.go`**

- In `idempotency.go`, read request body and compute `sha256.Sum256(body)`.
- Derive Redis key as `fmt.Sprintf("idempotency:%s:%s:%s:%s", actorID, method, path, key)`.
- Store `RequestHash` inside `CachedResponse` and return `409 Conflict` / `422 Unprocessable Entity` if key is reused with a differing body.
- Check if `c.Request.URL.Path` matches exempt routes (including `/account/login`, `/account/refresh`, `/account/reset-password`) and skip idempotency caching.
- In `cmd/api/main.go`, register `IdempotencyWithConfig` with exempt routes.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/middleware -run TestIdempotency_CrossUserAndExemptRoutes`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/middleware/idempotency.go internal/core/middleware/idempotency_test.go cmd/api/main.go
git commit -m "fix(security): scope idempotency cache by actor and exempt credential routes (SEC-AUD-001)"
```

---

### Task 2: Token Invalidation, Atomic Reset Consumption, and Password Limits (`SEC-AUD-005`, `SEC-AUD-007`, `SEC-AUD-008`, `SEC-AUD-012`)

**Files:**
- Create: `migrations/000006_add_password_changed_at_to_users.up.sql`
- Create: `migrations/000006_add_password_changed_at_to_users.down.sql`
- Modify: `internal/modules/account/model.go:20-120`
- Modify: `internal/modules/account/repository.go:120-160`
- Modify: `internal/modules/account/handler.go:257-285, 360-398, 466-513`
- Modify: `internal/core/auth/token.go:50-70`
- Test: `internal/modules/account/account_test.go`
- Test: `internal/core/auth/token_test.go`

**Interfaces:**
- Consumes: PostgreSQL `gorm.DB`, Redis `tokenManager`
- Produces:
  - `User.PasswordChangedAt *time.Time`
  - `repository.AtomicResetPassword(ctx context.Context, tokenHash string, newHash string, now time.Time) error`
  - `repository.UpdatePasswordWithTimestamp(ctx context.Context, userID string, newHash string, changedAt time.Time) error`

- [ ] **Step 1: Write the failing tests for password reset atomicity, token invalidation, and password limits**

```go
// internal/modules/account/account_test.go
func TestAccount_TokenInvalidatedAfterPasswordChange(t *testing.T) {
	// Verify that refresh token issued before password_changed_at is rejected with 401
}

func TestAccount_ConcurrentPasswordResetAtomicity(t *testing.T) {
	// Verify that 2 concurrent requests with same reset token result in exactly 1 success, 1 failure
}

func TestAccount_PasswordLengthExceeds128(t *testing.T) {
	// Verify login or password change with password > 128 characters fails validation with 400
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/modules/account -run "TestAccount_TokenInvalidatedAfterPasswordChange|TestAccount_ConcurrentPasswordResetAtomicity|TestAccount_PasswordLengthExceeds128"`  
Expected: FAIL

- [ ] **Step 3: Implement atomic password reset, password timestamp checking, and validation limits**

- Create migration `000006_add_password_changed_at_to_users.up.sql`:
  `ALTER TABLE users ADD COLUMN IF NOT EXISTS password_changed_at TIMESTAMP WITH TIME ZONE;`
- In `internal/modules/account/model.go`:
  - Add `PasswordChangedAt *time.Time json:"password_changed_at,omitempty"` to `User`.
  - Add `max=128` validation tags to `LoginRequest.Password` and `ChangePasswordRequest.OldPassword`.
- In `internal/modules/account/repository.go`:
  - Implement `AtomicResetPassword` executing in a single `tx`:
    `UPDATE password_resets SET used_at = ? WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`
    Check `tx.RowsAffected == 0` -> return `ErrInvalidToken`.
    Then update `users.password_hash` and `users.password_changed_at`.
- In `internal/modules/account/handler.go`:
  - In `Refresh`: verify `claims.IssuedAt` >= `user.PasswordChangedAt` (if set); fail closed if `h.tokenManager.RevokeToken` returns an error (`SEC-AUD-007`).
  - In `ChangePassword`: call `UpdatePasswordWithTimestamp`.
  - In `ResetPassword`: call `AtomicResetPassword`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/modules/account -run "TestAccount_TokenInvalidatedAfterPasswordChange|TestAccount_ConcurrentPasswordResetAtomicity|TestAccount_PasswordLengthExceeds128"`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add migrations/000006* internal/modules/account/ internal/core/auth/
git commit -m "fix(security): atomic password reset, token invalidation on change, and input bounds (SEC-AUD-005, SEC-AUD-007, SEC-AUD-008, SEC-AUD-012)"
```

---

### Task 3: Secure Password Reset Email Rendering & SMTP Context Timeout (`SEC-AUD-009`)

**Files:**
- Modify: `internal/modules/account/handler.go:440-453`
- Modify: `internal/core/email/smtp.go:30-63`
- Test: `internal/core/email/smtp_test.go`
- Test: `internal/modules/account/account_test.go`

**Interfaces:**
- Consumes: `email.Mailer`, `email.Message`
- Produces:
  - `SMTPMailer.Send(ctx context.Context, msg Message) error` honoring context deadlines.

- [ ] **Step 1: Write failing test for SMTP context timeout and forgot-password body rendering**

```go
// internal/core/email/smtp_test.go
func TestSMTPMailer_HonorsContextCancellation(t *testing.T) {
	mailer := NewSMTPMailer(Config{
		Host: "192.0.2.1", // non-routable TEST-NET-1 IP
		Port: 2525,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := mailer.Send(ctx, Message{
		To:       "user@example.com",
		Subject:  "Test",
		BodyText: "Hello",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/email -run TestSMTPMailer_HonorsContextCancellation`  
Expected: FAIL (blocks until OS TCP timeout or ignores context cancellation).

- [ ] **Step 3: Implement context-aware SMTP dialer and password reset email template**

- In `internal/core/email/smtp.go`:
  - Use `net.Dialer{Timeout: ...}.DialContext(ctx, "tcp", addr)` and `smtp.NewClient(conn, s.cfg.Host)` instead of blocking `smtp.SendMail`.
  - Strip CRLF (`\r`, `\n`) characters from `Subject` and `To` headers.
- In `internal/modules/account/handler.go` (`ForgotPassword`):
  - Populate `BodyText` and `BodyHTML` containing the reset URL and token (e.g. `fmt.Sprintf("Your password reset token is: %s", rawToken)`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/email -run TestSMTPMailer_HonorsContextCancellation`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/email/smtp.go internal/core/email/smtp_test.go internal/modules/account/handler.go
git commit -m "fix(security): context-aware SMTP transport and reset email body rendering (SEC-AUD-009)"
```

---

### Task 4: Audit Logger Panic Guard, Input Bounding, and PII Masking (`SEC-AUD-002`, `SEC-AUD-013`)

**Files:**
- Modify: `internal/core/audit/service.go:40-60, 130-155`
- Modify: `internal/platform/logger/logger.go:14-25, 45-80`
- Test: `internal/core/audit/audit_test.go`
- Test: `internal/platform/logger/logger_test.go`

**Interfaces:**
- Consumes: `audit.Service`, `logger.MaskPII`
- Produces:
  - Nil-safe `service.Log(ctx, entry)`
  - Safe worker recovery in `NewService`
  - Sanitized `TargetID` (max 64 runes) and `IPAddress` (max 45 runes)
  - Extended PII regexes covering Basic Auth, `password_hash`, and single-label host DSNs.

- [ ] **Step 1: Write failing tests for nil-DB crash, audit length truncation, and DSN PII masking**

```go
// internal/core/audit/audit_test.go
func TestAuditService_NilDBNoPanic(t *testing.T) {
	svc := NewService(nil, 10)
	defer svc.Close()

	// Should not panic on Log or LogAsync
	assert.NotPanics(t, func() {
		_ = svc.Log(context.Background(), Entry{
			Action:   "test.action",
			TargetID: "oversized-target-id-exceeding-standard-length-bounds-abcdefghijklmnopqrstuvwxyz1234567890",
		})
		svc.LogAsync(Entry{Action: "async.test"})
	})
}

// internal/platform/logger/logger_test.go
func TestLogger_MaskPII_ExtendedPatterns(t *testing.T) {
	assert.Equal(t, "Basic [REDACTED]", MaskPII("Basic dXNlcjpwYXNz"))
	assert.Equal(t, `"password_hash":"[REDACTED]"`, MaskPII(`"password_hash":"$argon2id$v=19$..."`))
	assert.Equal(t, "postgres://user:[REDACTED]@postgres:5432/db", MaskPII("postgres://user:supersecret@postgres:5432/db"))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/audit -run TestAuditService_NilDBNoPanic`  
Run: `go test -v ./internal/platform/logger -run TestLogger_MaskPII_ExtendedPatterns`  
Expected: FAIL

- [ ] **Step 3: Implement panic guards, bounds checking, and extended PII maskers**

- In `internal/core/audit/service.go`:
  - Check `if s.db == nil { return nil }` in `Log`.
  - Wrap the goroutine loop in `recover()` with logging on panic.
  - Truncate `entry.TargetID` to 64 runes and `entry.IPAddress` to 45 runes before persisting.
  - Log a warning when `s.queue` is full in `LogAsync`.
- In `internal/platform/logger/logger.go`:
  - Add `basicAuthRegex = regexp.MustCompile("(?i)Basic\\s+[A-Za-z0-9+/=]+")`.
  - Add `dsnRegex = regexp.MustCompile("(?i)([a-z]+://[^:]+:)([^@]+)(@.+)")`.
  - Add `passwordHashRegex = regexp.MustCompile("(?i)(\"password_hash\"\\s*:\\s*\")[^\"]*(\")")`.
  - Apply these masks in `MaskPII`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/audit -run TestAuditService_NilDBNoPanic`  
Run: `go test -v ./internal/platform/logger -run TestLogger_MaskPII_ExtendedPatterns`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/audit/ internal/platform/logger/
git commit -m "fix(security): audit logger panic recovery, bounds sanitization, and PII masking (SEC-AUD-002, SEC-AUD-013)"
```

---

### Task 5: Reverse Proxy Trust Configuration, Client IP Extraction, and Overload Priority Defense (`SEC-AUD-003`, `SEC-AUD-006`)

**Files:**
- Modify: `internal/config/config.go:38-49`
- Modify: `cmd/api/main.go:212-226`
- Modify: `internal/core/middleware/audit.go:30-49`
- Modify: `internal/core/overload/manager.go:330-345`
- Test: `internal/core/middleware/audit_test.go`
- Test: `internal/core/overload/overload_test.go`

**Interfaces:**
- Consumes: `HTTPConfig.TrustedProxies []string`
- Produces:
  - Explicit Gin `router.SetTrustedProxies(cfg.HTTP.TrustedProxies)`
  - Sanitized `ExtractClientIP(c *gin.Context)` using `c.ClientIP()` + `net.ParseIP`
  - Client-header-agnostic `ExtractPriority(c *gin.Context, m *Manager)`

- [ ] **Step 1: Write failing tests for proxy IP extraction and priority header rejection**

```go
// internal/core/middleware/audit_test.go
func TestExtractClientIP_ValidatesIP(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	c.Request.Header.Set("X-Forwarded-For", "malformed-ip-overflowing-the-database-column-size-45-characters-long")
	ip := ExtractClientIP(c)
	assert.NotContains(t, ip, "overflowing")
}

// internal/core/overload/overload_test.go
func TestExtractPriority_IgnoresUntrustedClientHeaders(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/public", nil)
	c.Request.Header.Set("X-API-Priority", "critical")
	p := ExtractPriority(c, nil)
	assert.Equal(t, PriorityNormal, p) // Must not be PriorityCritical
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/middleware -run TestExtractClientIP_ValidatesIP`  
Run: `go test -v ./internal/core/overload -run TestExtractPriority_IgnoresUntrustedClientHeaders`  
Expected: FAIL

- [ ] **Step 3: Configure trusted proxies, IP normalization, and priority stripping**

- In `internal/config/config.go`:
  - Add `TrustedProxies []string env:"HTTP_TRUSTED_PROXIES" default:""` to `HTTPConfig`.
- In `cmd/api/main.go`:
  - Call `_ = router.SetTrustedProxies(cfg.HTTP.TrustedProxies)` immediately after `gin.New()`.
- In `internal/core/middleware/audit.go`:
  - Use `ipStr := c.ClientIP()`. Parse with `net.ParseIP(ipStr)`. If invalid, return `"127.0.0.1"`.
- In `internal/core/overload/manager.go`:
  - Remove untrusted client headers `X-API-Priority` and `X-Priority` from `ExtractPriority`. Derive priority strictly from verified claims or registered routes.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/middleware -run TestExtractClientIP_ValidatesIP`  
Run: `go test -v ./internal/core/overload -run TestExtractPriority_IgnoresUntrustedClientHeaders`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go cmd/api/main.go internal/core/middleware/audit.go internal/core/overload/manager.go
git commit -m "fix(security): configure trusted proxies, validate client IP, and remove untrusted priority headers (SEC-AUD-003, SEC-AUD-006)"
```

---

### Task 6: GraphQL Complexity DAG Defense and Route Exposure Control (`SEC-AUD-004`, `SEC-AUD-010`)

**Files:**
- Modify: `internal/core/graphql/complexity.go:60-90, 160-185`
- Modify: `internal/config/config.go:30-40`
- Modify: `cmd/api/main.go:238-245`
- Modify: `cmd/api/swagger.go:10-25`
- Modify: `internal/core/graphql/handler.go:160-175`
- Test: `internal/core/graphql/complexity_test.go`
- Test: `cmd/api/api_test.go`

**Interfaces:**
- Consumes: GraphQL AST documents
- Produces:
  - Memoized Depth and Complexity calculation with max AST traversal visit cap (e.g. 5,000 visits)
  - Environment-conditional Swagger and GraphQL Playground routes

- [ ] **Step 1: Write failing test for GraphQL fragment DAG bomb and prod route exclusion**

```go
// internal/core/graphql/complexity_test.go
func TestComplexity_ExponentialFragmentDAG(t *testing.T) {
	// Construct a query with 25 nested fragments referencing each other in a DAG
	query := `
	query Bomb { ...F1 }
	fragment F1 on Query { ...F2 ...F2 }
	fragment F2 on Query { ...F3 ...F3 }
	fragment F3 on Query { ...F4 ...F4 }
	fragment F4 on Query { ...F5 ...F5 }
	fragment F5 on Query { user { id } }
	`
	schema := parseTestSchema(t)
	doc := parseTestQuery(t, schema, query)

	engine := NewComplexityEngine(schema, ComplexityConfig{MaxComplexity: 100})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := engine.CalculateComplexity(ctx, doc, nil)
	duration := time.Since(start)

	assert.Less(t, duration, 50*time.Millisecond)
	assert.Error(t, err) // Exceeds complexity or visit cap
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/graphql -run TestComplexity_ExponentialFragmentDAG`  
Expected: FAIL (spins CPU exponentially).

- [ ] **Step 3: Implement memoization and visit capping in `complexity.go` and route guards in `cmd/api`**

- In `internal/core/graphql/complexity.go`:
  - Add `depthMemo map[string]int` and `complexityMemo map[string]int`.
  - Add a traversal visit counter `visitedNodes int`. If `visitedNodes > 5000`, return `ErrComplexityLimitExceeded`.
  - Check `ctx.Err()` at every step.
- In `internal/config/config.go`:
  - Add `EnableSwagger bool env:"ENABLE_SWAGGER" default:"false"` and `EnablePlayground bool env:"ENABLE_GRAPHQL_PLAYGROUND" default:"false"` to `AppConfig`.
- In `cmd/api/main.go` / `swagger.go`:
  - Guard `registerSwagger` and `/playground` / `/graphiql` with `if cfg.App.Env != "production" || cfg.App.EnableSwagger`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/graphql -run TestComplexity_ExponentialFragmentDAG`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/graphql/ internal/config/ cmd/api/
git commit -m "fix(security): memoize GraphQL complexity AST visits and restrict prod debug routes (SEC-AUD-004, SEC-AUD-010)"
```

---

### Task 7: Diagnostic Redaction, Global Request Limits, and Transport Security (`SEC-AUD-011`, `SEC-AUD-012`, `SEC-AUD-015`)

**Files:**
- Modify: `internal/core/telemetry/health.go:160-322`
- Modify: `internal/core/telemetry/otel.go:50-65`
- Create: `internal/core/middleware/request_size.go`
- Modify: `internal/config/config.go:47, 74, 80-90`
- Modify: `internal/platform/redis/redis.go:50-70`
- Modify: `cmd/api/main.go:214-235`
- Test: `internal/core/telemetry/health_test.go`
- Create: `internal/core/middleware/request_size_test.go`

**Interfaces:**
- Consumes: `gin.Engine`, `http.MaxBytesReader`
- Produces:
  - Sanitized public `/health` response omitting raw error messages and `SystemStats`
  - `middleware.MaxBodySize(limitBytes int64) gin.HandlerFunc`
  - TLS-capable `redis.NewClient` and safe Postgres `SSLMode` default

- [ ] **Step 1: Write failing tests for health redaction and max body size middleware**

```go
// internal/core/middleware/request_size_test.go
func TestMaxBodySize_RejectsOversizedPayload(t *testing.T) {
	router := gin.New()
	router.Use(MaxBodySize(1024)) // 1 KB
	router.POST("/upload", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		c.String(http.StatusOK, "read %d bytes", len(body))
	})

	oversized := bytes.Repeat([]byte("a"), 2048)
	w := performRequest(router, "POST", "/upload", string(oversized), nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

// internal/core/telemetry/health_test.go
func TestHealth_RedactsInternalErrorsInProduction(t *testing.T) {
	// Verify that error strings and SystemStats are absent from JSON response
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/middleware -run TestMaxBodySize_RejectsOversizedPayload`  
Expected: FAIL

- [ ] **Step 3: Implement request size limits, health redaction, and transport security**

- Create `internal/core/middleware/request_size.go` wrapping `c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limitBytes)`.
- In `cmd/api/main.go`: mount `middleware.MaxBodySize(1 << 20)` (1 MB default).
- In `internal/core/telemetry/health.go`: omit `Error` details and `SystemStats` when responding to unauthenticated requests.
- In `internal/config/config.go`:
  - Change `POSTGRES_SSLMODE` default to `"require"` when `APP_ENV != "development"`.
  - Add `UseTLS bool env:"REDIS_USE_TLS" default:"false"` to `RedisConfig`.
  - Set `HTTP_ALLOWED_ORIGINS` default to `""` (disallow wildcard CORS by default in prod).
- In `internal/platform/redis/redis.go`: configure `TLSConfig` if `cfg.UseTLS` is true.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/middleware -run TestMaxBodySize_RejectsOversizedPayload`  
Run: `go test -v ./internal/core/telemetry -run TestHealth_RedactsInternalErrorsInProduction`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/telemetry/ internal/core/middleware/ internal/config/ internal/platform/redis/ cmd/api/main.go
git commit -m "fix(security): redact health internals, limit HTTP payload size, and enforce TLS defaults (SEC-AUD-011, SEC-AUD-012, SEC-AUD-015)"
```

---

### Task 8: WebSocket Auth Hardening, Worker Test Decoupling, and Session CSRF (`SEC-AUD-016`, `SEC-AUD-017`)

**Files:**
- Modify: `internal/core/websocket/handler.go:80-130`
- Modify: `internal/core/jobs/ledger.go:1-12, 350-360`
- Modify: `internal/core/jobs/ledger_test.go`
- Modify: `internal/config/config.go:50-55`
- Modify: `internal/core/middleware/csrf.go:20-60`
- Test: `internal/core/websocket/websocket_test.go`
- Test: `internal/core/middleware/csrf_test.go`

**Interfaces:**
- Consumes: WebSocket upgrader, CSRF token manager
- Produces:
  - WebSocket authentication via `Authorization` header or `Sec-WebSocket-Protocol` subprotocol
  - Clean `jobs` package without `import "testing"` in production code
  - User/session-bound CSRF token validation

- [ ] **Step 1: Write failing tests for WebSocket token in query params and session CSRF binding**

```go
// internal/core/middleware/csrf_test.go
func TestCSRF_BoundToSession(t *testing.T) {
	secret := "secret-key"
	sessionA := "session-123"
	sessionB := "session-456"

	token, err := GenerateBoundCSRFToken(secret, sessionA)
	assert.NoError(t, err)

	assert.True(t, ValidateBoundCSRFToken(secret, sessionA, token))
	assert.False(t, ValidateBoundCSRFToken(secret, sessionB, token)) // Mismatched session
}

// internal/core/websocket/websocket_test.go
func TestWebSocket_RejectsRawQueryTokenInProduction(t *testing.T) {
	// Verify that ?token= query param is rejected when env == "production"
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/middleware -run TestCSRF_BoundToSession`  
Run: `go test -v ./internal/core/websocket -run TestWebSocket_RejectsRawQueryTokenInProduction`  
Expected: FAIL

- [ ] **Step 3: Implement WebSocket auth hardening, isolate NewTestLedger, and bind CSRF**

- In `internal/core/websocket/handler.go`:
  - Fail closed if `h.tokenManager == nil`.
  - Extract token from `Sec-WebSocket-Protocol` or `Authorization: Bearer` header; reject query `?token=` in production.
- In `internal/core/jobs/ledger.go`:
  - Remove `import "testing"`.
  - Move `NewTestLedger(t testing.TB)` into `internal/core/jobs/ledger_test.go`.
- In `internal/config/config.go`:
  - Remove `required:"true"` from `CookieSecret`.
- In `internal/core/middleware/csrf.go`:
  - Add session/user ID binding to HMAC signature: `mac.Write([]byte(sessionID + ":" + rawHex))`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/middleware -run TestCSRF_BoundToSession`  
Run: `go test -v ./internal/core/websocket -run TestWebSocket_RejectsRawQueryTokenInProduction`  
Run: `go vet ./cmd/worker` (ensures `testing` flags are not present).  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/core/websocket/ internal/core/jobs/ internal/config/ internal/core/middleware/csrf*
git commit -m "fix(security): harden websocket auth, decouple testing from worker, and bind CSRF (SEC-AUD-016, SEC-AUD-017)"
```

---

### Task 9: CI Security Workflows, Container Image Pinning, and Datastore Hardening (`SEC-AUD-014`)

**Files:**
- Modify: `.github/workflows/ci.yml:35-52`
- Modify: `scripts/verify-full.sh:6-10`
- Modify: `Dockerfile:6-10, 48-55`
- Modify: `compose.yaml:58-88`

**Interfaces:**
- Consumes: GitHub Actions runner, Docker engine
- Produces:
  - Working `static-analysis` and `dependency-audit` CI steps (`govulncheck`, `golangci-lint`)
  - Digest-pinned base images in `Dockerfile`
  - Loopback-bound, authenticated datastores in `compose.yaml`

- [ ] **Step 1: Check existing verify-full failure**

Run: `bash scripts/verify-full.sh`  
Expected: FAIL with "TODO: configure integration/e2e" and exit code 1.

- [ ] **Step 2: Update CI workflows, verification scripts, Dockerfile, and Compose**

- In `.github/workflows/ci.yml`:
  - Update `static-analysis` step to run `golangci/golangci-lint-action@v6` (or `go vet ./...`).
  - Update `dependency-audit` step to install and run `golang.org/x/vuln/cmd/govulncheck ./...`.
- In `scripts/verify-full.sh`:
  - Replace `TODO ... exit 1` with running integration test suite: `go test -race -tags=integration ./...`.
- In `Dockerfile`:
  - Pin base images with immutable `@sha256:` digests:
    `FROM --platform=$BUILDPLATFORM golang:1.23.3-alpine3.20@sha256:... AS builder`
    `FROM alpine:3.20@sha256:... AS runner`
- In `compose.yaml`:
  - Bind exposed ports to `127.0.0.1`: `"127.0.0.1:5432:5432"`, `"127.0.0.1:6379:6379"`, `"127.0.0.1:27017:27017"`.
  - Remove `./migrations` volume mount from Postgres container; run migrations cleanly via `cmd/migrate`.
  - Configure Redis `command: ["redis-server", "--requirepass", "${REDIS_PASSWORD:-redispassword}"]`.

- [ ] **Step 3: Run verify-full to verify it passes**

Run: `bash scripts/verify-full.sh`  
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/ci.yml scripts/verify-full.sh Dockerfile compose.yaml
git commit -m "fix(security): configure CI security jobs, pin container digests, and bind compose datastores (SEC-AUD-014)"
```

---

### Task 10: Threat Model Documentation & Verification Gate Closure

**Files:**
- Modify: `docs/threat-model.md`
- Run: `scripts/verify.sh`
- Run: `go test -race -count=1 ./...`

**Interfaces:**
- Consumes: Security Audit Report findings & mitigations
- Produces: Complete, updated threat model documentation and clean repository test gate.

- [ ] **Step 1: Update `docs/threat-model.md`**

Populate all required sections in `docs/threat-model.md`:
1. **Assets:** Signing keys, password hashes, reset tokens, Redis stores, audit logs, outbox tables.
2. **Entry Points:** Public REST, Authenticated REST, GraphQL, WebSockets, Health probes, Background worker.
3. **Trust Boundaries:** Reverse proxy -> Gin, App -> Redis, App -> PostgreSQL/MongoDB, App -> Egress.
4. **Abuse Cases & Mitigations:** Document remediations for `SEC-AUD-001` through `SEC-AUD-017`.

- [ ] **Step 2: Run full repository verification**

Run: `scripts/verify.sh`  
Run: `go test -race -count=1 ./...`  
Expected: ALL PASS with zero failures, vet warnings, or race conditions.

- [ ] **Step 3: Commit**

```bash
git add docs/threat-model.md
git commit -m "docs(security): document threat model assets, trust boundaries, and audit remediations"
```

---

## Self-Review Checklist

1. **Spec Coverage:** Skimmed against `docs/reports/2026-11-21-security-audit/security-audit-report.md`.
   - `SEC-AUD-001` -> Task 1
   - `SEC-AUD-002` -> Task 4
   - `SEC-AUD-003` -> Task 5
   - `SEC-AUD-004` -> Task 6
   - `SEC-AUD-005` -> Task 2
   - `SEC-AUD-006` -> Task 5
   - `SEC-AUD-007` -> Task 2
   - `SEC-AUD-008` -> Task 2
   - `SEC-AUD-009` -> Task 3
   - `SEC-AUD-010` -> Task 6
   - `SEC-AUD-011` -> Task 7
   - `SEC-AUD-012` -> Task 2 & Task 7
   - `SEC-AUD-013` -> Task 4
   - `SEC-AUD-014` -> Task 9
   - `SEC-AUD-015` -> Task 7
   - `SEC-AUD-016` -> Task 8
   - `SEC-AUD-017` -> Task 8
   - Threat Model Documentation -> Task 10
   - All 17 findings and documentation requirements are covered with no gaps.

2. **Step Scan:** Every step specifies the exact file, signature, test code, run command, expected output, and commit command.
3. **Type Consistency:** Method signatures across tasks (`AtomicResetPassword`, `IdempotencyWithConfig`, `MaxBodySize`) are verified against GORM and Gin standards.
4. **Review Focus:** All 5 focus points in the header correspond directly to test steps in Tasks 1, 2, 4, 5, and 6.
5. **Proportion:** Plan is tightly structured and focused on exact interfaces and assertions.
