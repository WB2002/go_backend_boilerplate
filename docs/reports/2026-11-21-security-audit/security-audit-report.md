# Security Audit Report — Go Backend Boilerplate

**Date:** 2026-11-21  
**Scope:** Repository-wide (`go_backend_boilerplate`)  
**Methodology:** Multi-Phase Adversarial Security Audit (`/security-audit`) — Coverage Ledger, Coverage-Led Hunting, Independent Readonly Disprover Gate (5 subagents), and Record Verification.

---

## 1. Executive Verdict

**FAIL (ACTION REQUIRED)**

- **Confirmed Findings:** 17 total (`1 Critical`, `4 High`, `9 Medium`, `3 Low`)
- **Needs Validation:** 0
- **Rejected (Disproved / Unwired):** 4 (`SEC-AUD-018`, `SEC-AUD-019`, `SEC-AUD-020`, `SEC-AUD-021`)

### Boilerplate Verification & Test Suite Status
Prior to finalizing the audit, the full Go test suite and build/vet checks were executed read-only:
- `scripts/verify.sh`: **PASS** (`go build ./...`, `go test -v ./...`, `go vet ./...` — `golangci-lint` not installed locally).
- `go test -race -count=1 ./...`: **PASS** across all packages (no race conditions detected in existing unit/integration tests).

---

## 2. Coverage Ledger Summary

All **14 / 14 (100%)** architectural units in [`coverage-ledger.json`](coverage-ledger.json) reached `"status": "audited"` across the six threat classes (`SEC-AUTH`, `SEC-AUTHZ`, `SEC-INJ`, `SEC-EGRESS`, `SEC-SHARP`, `SEC-OPS`).

| Unit ID | Surface | Key Files Covered | Status |
| :--- | :--- | :--- | :--- |
| `UNIT-001` | `api-route \| worker \| infra-ci` | `cmd/api/main.go`, `cmd/api/swagger.go`, `cmd/worker/main.go`, `cmd/migrate/main.go`, `internal/config/config.go` | `audited` |
| `UNIT-002` | `auth-boundary` | `internal/core/auth/hasher.go`, `token.go`, `service.go` | `audited` |
| `UNIT-003` | `auth-boundary` | `internal/core/middleware/auth.go`, `csrf.go`, `cors.go`, `security.go`, `audit.go` | `audited` |
| `UNIT-004` | `api-route` | `internal/core/middleware/compression.go`, `deadline.go`, `idempotency.go`, `logger.go`, `overload.go`, `ratelimit.go`, `recovery.go`, `request_id.go` | `audited` |
| `UNIT-005` | `api-route \| data-access \| auth-boundary` | `internal/modules/account/handler.go`, `model.go`, `repository.go` | `audited` |
| `UNIT-006` | `api-route` | `internal/core/graphql/handler.go`, `complexity.go`, `schema.graphqls` | `audited` |
| `UNIT-007` | `api-route \| auth-boundary` | `internal/core/websocket/handler.go`, `hub.go`, `client.go` | `audited` |
| `UNIT-008` | `api-route \| egress-ai` | `internal/core/security/upload.go`, `clamav.go`, `captcha.go`, `geoip.go` | `audited` |
| `UNIT-009` | `egress-ai` | `internal/core/httpclient/client.go`, `ssrf.go`, `internal/core/notification/slack.go` | `audited` |
| `UNIT-010` | `egress-ai` | `internal/core/email/mailer.go`, `smtp.go`, `mock.go` | `audited` |
| `UNIT-011` | `data-access \| worker` | `internal/core/audit/*`, `internal/core/outbox/*` | `audited` |
| `UNIT-012` | `worker \| data-access` | `internal/core/jobs/*`, `ratelimit/*`, `overload/*`, `cache/*`, `featureflag/*` | `audited` |
| `UNIT-013` | `data-access \| api-route` | `internal/platform/{database,redis,mongodb,logger}/*`, `internal/core/{server,telemetry,problem}/*` | `audited` |
| `UNIT-014` | `infra-ci \| data-access` | `migrations/*.sql`, `Dockerfile`, `compose.yaml`, `.github/workflows/*.yml`, `go.mod`, `.golangci.yml`, `scripts/*` | `audited` |

---

## 3. Confirmed Findings (`SEC-AUD-NNN`)

### Critical

#### `SEC-AUD-001` — Global Idempotency Middleware Cross-User Response Hijacking & Auth Bypass
- **Location:** `internal/core/middleware/idempotency.go:63` (`cmd/api/main.go:233, 240-241`)
- **CWE / OWASP:** CWE-287 / CWE-639 / OWASP A01:2021 Broken Access Control
- **Exploit Scenario:** `middleware.Idempotency(rdb, 10*time.Minute)` is mounted globally before route-level `authMiddleware` and keys Redis strictly by `"idempotency:" + key` (`idempotency.go:63`). When a victim issues `POST /account/login` (or `/account/refresh`, `/account/change-password`, `/graphql`) with `Idempotency-Key: <key>`, the `TokenResponse` (including `accessToken` and `refreshToken`) is cached in Redis for 10 minutes. Any unauthenticated attacker sending a `POST`/`PUT`/`PATCH`/`DELETE` request to *any* path with the same `Idempotency-Key` receives the victim's cached response and aborts before `AuthMiddleware` executes (`idempotency.go:94-105`).
- **Disprover Proof:** Verified `redis.NewClient` is always non-nil (`cmd/api/main.go:77`) and no user, method, path, or body hash scoping exists anywhere in `idempotency.go`.
- **Remediation Specification:** Scope idempotency keys by authenticated user/tenant ID, HTTP method, normalized route path, and SHA-256 request body digest (rejecting mismatches with `422`/`409`); mount `Idempotency` after `AuthMiddleware` on authenticated mutation routes; and exempt credential-issuing endpoints (`/account/login`, `/account/refresh`, `/account/reset-password`) from response caching.

---

### High

#### `SEC-AUD-002` — Unrecovered Nil-Pointer Panic in Async Audit Logger on Degraded DB Startup
- **Location:** `internal/core/audit/service.go:142` (`cmd/api/main.go:84-91, 200`, `internal/modules/account/handler.go:177-189`)
- **CWE / OWASP:** CWE-476 / OWASP A04:2021 Insecure Design
- **Exploit Scenario:** `cmd/api/main.go:84-91` permits startup when PostgreSQL is unreachable (`db == nil`) and passes `nil` to `audit.NewService(db, ...)` (`main.go:200`). Any unauthenticated `POST /account/login` triggers `FindByEmail` -> nil-DB error (`repository.go:63-65`) -> `h.audit.LogAsync(...)` (`handler.go:177-189`). The background goroutine (`audit/service.go:47-54`) calls `s.db.WithContext(ctx)` without a nil check or `recover()`, panicking and terminating the API process.
- **Disprover Proof:** Verified `middleware.Recovery` (`main.go:218`) only wraps the HTTP handler goroutine, not the async audit worker goroutine.
- **Remediation Specification:** Fail fast on PostgreSQL connection failure in non-development environments, add a nil-`db` guard in `audit.service.Log` (`service.go:142`), and wrap the async audit worker goroutine (`service.go:47-54`) with panic recovery.

#### `SEC-AUD-003` — Unconfigured Gin Trusted Proxies and Unvalidated `ExtractClientIP` Allow Rate-Limit Bypass and Audit Log Dropping
- **Location:** `cmd/api/main.go:212`, `internal/core/middleware/audit.go:30-49`
- **CWE / OWASP:** CWE-290 / CWE-348 / OWASP A01:2021 Broken Access Control
- **Exploit Scenario:** `cmd/api/main.go:212` uses `gin.New()` without calling `router.SetTrustedProxies(...)`, so Gin v1.10.0 trusts `X-Forwarded-For` / `X-Real-IP` from any remote IP (`0.0.0.0/0`, `::/0`). Attackers can rotate spoofed `X-Forwarded-For` headers to bypass IP rate limiting (`main.go:227-232`, `ratelimit/limiter.go:170-175`) and unauthenticated GraphQL complexity budgets (`graphql/handler.go:447`). Furthermore, `ExtractClientIP` (`middleware/audit.go:30-49`) accepts arbitrary unvalidated strings from `X-Forwarded-For`; sending >45 characters overflows `audit_logs.ip_address VARCHAR(45)` (`migrations/000005_create_audit_logs_table.up.sql:8`), and `audit/service.go:51` discards the resulting SQL error.
- **Disprover Proof:** Verified Gin v1.10.0 default proxy trust and zero `SetTrustedProxies` calls across `cmd/` and `internal/`.
- **Remediation Specification:** Explicitly call `router.SetTrustedProxies(...)` from configuration (defaulting to `nil` when not behind a trusted proxy); replace `ExtractClientIP` raw header parsing with `c.ClientIP()` plus `net.ParseIP` validation/normalization before DB persistence.

#### `SEC-AUD-004` — GraphQL Exponential Fragment DAG Traversal Causes Unauthenticated CPU DoS
- **Location:** `internal/core/graphql/complexity.go:73, 173` (`cmd/api/main.go:244`)
- **CWE / OWASP:** CWE-400 / CWE-1333 / OWASP A05:2021 Security Misconfiguration
- **Exploit Scenario:** Unauthenticated `POST /graphql` (`main.go:244`) passes `validator.Validate` (`graphql/handler.go:237`) for non-cyclic fragment DAGs (e.g., 25–30 fragments where `Fi` spreads `Fi+1` twice via aliases). `DepthEngine.calculateSelectionSetDepth` (`complexity.go:73-84`) and `ComplexityEngine.calculateSelectionSetComplexity` (`complexity.go:173-181`) reset `visited[s.Name] = false` on exit without memoization and never abort early when exceeding `MaxDepth` / `MaxComplexity` or `ctx.Done()`, causing $O(2^N)$ CPU spin.
- **Disprover Proof:** Verified `gqlparser v2.5.16` `no_fragment_cycles.go` only rejects cyclic fragments, and `RequestDeadline` cancellation is ignored by the AST traversal loops.
- **Remediation Specification:** Memoize per-fragment depth and complexity values, enforce a hard cap on total AST node/fragment visits with immediate termination once `MaxDepth` or `MaxComplexity` is exceeded, and check `ctx.Done()`.

#### `SEC-AUD-005` — Existing Access and Refresh JWTs Remain Valid After Password Change or Password Reset
- **Location:** `internal/modules/account/handler.go:343-398, 466-513` (`internal/core/auth/token.go:58-63`)
- **CWE / OWASP:** CWE-613 / OWASP A07:2021 Identification and Authentication Failures
- **Exploit Scenario:** After a user rotates or resets a compromised password via `POST /account/change-password` or `POST /account/reset-password`, neither handler revokes active tokens nor increments a per-user `token_version` / `password_changed_at` claim. Anyone holding a pre-reset refresh token (7-day default TTL) can continue minting new access/refresh pairs via `POST /account/refresh` (`handler.go:257-285`).
- **Disprover Proof:** Verified `Refresh` only checks Redis JTI revocation and `user.Status != "active"` (`handler.go:264`).
- **Remediation Specification:** Store `password_changed_at` (or `token_version`) on `users`, embed/compare it in `Refresh` and `AuthMiddleware` (cached in Redis or checked against user state), and invalidate prior tokens upon password change/reset.

---

### Medium

#### `SEC-AUD-006` — Client-Controlled `X-API-Priority` / `X-Priority` Headers Bypass Overload Shedding
- **Location:** `internal/core/overload/manager.go:334-339` (`cmd/api/main.go:226`)
- **CWE / OWASP:** CWE-290 / CWE-770 / OWASP A04:2021 Insecure Design
- **Exploit Scenario:** `InboundOverload` runs globally (`main.go:226`) before rate limiting and route auth, and `ExtractPriority` (`manager.go:334-339`) trusts client-supplied `X-API-Priority` / `X-Priority` headers. Unauthenticated attackers can send `X-Priority: critical` to claim the 125% capacity reserve (`manager.go:230-237`) and bypass memory/GC shedding (`manager.go:203`).
- **Remediation Specification:** Remove untrusted client header inspection from `ExtractPriority` (or restrict it to authenticated internal callers / trusted gateway-signed headers), deriving priority from route registrations or verified auth claims.

#### `SEC-AUD-007` — Refresh Handler Ignores `RevokeToken` Errors Enabling Refresh Token Replay
- **Location:** `internal/modules/account/handler.go:269-280`
- **CWE / OWASP:** CWE-613 / CWE-252 / OWASP A07:2021 Identification and Authentication Failures
- **Exploit Scenario:** `Refresh` discards the error from `h.tokenManager.RevokeToken` at line 277 (`_ = ...`) and still returns `200 OK` with a new token pair. On a Redis write failure (or concurrent refresh requests racing between `ValidateRefreshToken` `EXISTS` and `RevokeToken` `SET`), the same refresh token can be replayed.
- **Remediation Specification:** Fail closed when `RevokeToken` returns an error (matching `internal/core/auth/service.go:97-99`), require non-empty `claims.ID` in `ValidateAccessToken`/`ValidateRefreshToken`, and consume refresh tokens atomically via Redis Lua/`SetNX`.

#### `SEC-AUD-008` — Password Reset TOCTOU Race Condition & Ignored Token Consumption Error
- **Location:** `internal/modules/account/handler.go:481-498`, `internal/modules/account/repository.go:120-151`
- **CWE / OWASP:** CWE-362 / CWE-640 / OWASP A04:2021 Insecure Design
- **Exploit Scenario:** `ResetPassword` validates the token with a non-locking `SELECT` (`FindValidPasswordReset`), runs Argon2id hashing (~50–100 ms), calls `UpdatePassword`, and then ignores the error from `MarkPasswordResetUsed` (`handler.go:498`). Concurrent requests with the same token can both succeed and overwrite the password, or a DB update failure leaves `used_at IS NULL`.
- **Remediation Specification:** Execute token consumption and password update in a single DB transaction, atomically claiming the token first (`UPDATE password_resets SET used_at = NOW() WHERE token_hash = ? AND used_at IS NULL AND expires_at > NOW()`) before updating `users.password_hash`.

#### `SEC-AUD-009` — `ForgotPassword` Sends Empty Email Body Without Token & `SMTPMailer` Ignores Context Deadlines
- **Location:** `internal/modules/account/handler.go:442-450`, `internal/core/email/smtp.go:30-62`
- **CWE / OWASP:** CWE-640 / CWE-400 / OWASP A04:2021 Insecure Design
- **Exploit Scenario:** `ForgotPassword` passes `Data: map[string]any{"Token": rawToken}` with empty `BodyHTML`/`BodyText` and discards errors (`handler.go:443`). `SMTPMailer.Send` (`smtp.go:56-62`) ignores `TemplateName`/`Data` (sending an empty email without the reset token) and ignores `ctx` while blocking on `smtp.SendMail`, bypassing `RequestDeadline` and creating an account-existence timing side channel when SMTP is slow. *(Note: CRLF header injection at `smtp.go:43-53` was disproved as currently exploitable because `Subject` is hardcoded, `From` is env-configured, and `net/smtp.SendMail` validates `To`/`From`.)*
- **Remediation Specification:** Render the reset email body, enqueue mail delivery via background jobs/outbox to ensure constant-time HTTP responses, honor `ctx` deadlines in `SMTPMailer.Send`, and strip `\r`/`\n` in header construction as defense-in-depth.

#### `SEC-AUD-010` — Unauthenticated `/graphql`, `/playground`, `/graphiql`, and `/swagger/*any` Exposed in All Environments
- **Location:** `cmd/api/main.go:238, 244`, `cmd/api/swagger.go:12-13`, `internal/core/graphql/handler.go:165-170`
- **CWE / OWASP:** CWE-306 / CWE-200 / OWASP A01:2021 Broken Access Control
- **Exploit Scenario:** Swagger (`/swagger/doc.json`, `/swagger/index.html`), GraphQL Playground/GraphiQL, and `/graphql` are mounted unconditionally without `cfg.App.Env` checks or `authMiddleware`. In production, unauthenticated attackers gain full OpenAPI schema visibility and direct access to `/graphql` (amplifying `SEC-AUD-004`).
- **Remediation Specification:** Mount `/swagger/*any`, `/playground`, and `/graphiql` only in non-production environments (or behind admin auth), and protect `/graphql` with `authMiddleware`.

#### `SEC-AUD-011` — Unauthenticated `/health` Discloses Raw Dependency Errors & Runtime Stats; OTel Defaults to Plaintext gRPC
- **Location:** `internal/core/telemetry/health.go:161-322` (`cmd/api/main.go:237`), `internal/core/telemetry/otel.go:55-62` (`cmd/api/main.go:66`)
- **CWE / OWASP:** CWE-209 / CWE-319 / OWASP A05:2021 Security Misconfiguration
- **Exploit Scenario:** Public `GET /health` exposes Go runtime memory/goroutine counts (`SystemStats`) and raw error strings from Postgres, Redis, and MongoDB pings. Additionally, `InitTracer` appends `otlptracegrpc.WithInsecure()` whenever `OTEL_EXPORTER_OTLP_ENDPOINT` uses standard `host:port` syntax without `https://`.
- **Remediation Specification:** Redact raw dependency error strings and `SystemStats` from unauthenticated `/health` responses (restricting deep diagnostics to authenticated/internal endpoints), and require TLS by default in `InitTracer` unless an explicit opt-in flag is set.

#### `SEC-AUD-012` — Missing Global HTTP Body Size Limit, Unbounded `LoginRequest.Password`, and Plaintext DB/Redis Transport Defaults
- **Location:** `cmd/api/main.go:212-234`, `internal/modules/account/model.go:110`, `internal/config/config.go:74`, `internal/platform/redis/redis.go:51-65`
- **CWE / OWASP:** CWE-770 / CWE-319 / OWASP A05:2021 Security Misconfiguration
- **Exploit Scenario:** Unlike GraphQL (`graphql/handler.go:193`), REST routes lack `http.MaxBytesReader`, and `LoginRequest.Password` (`model.go:110`) has no `max` tag, allowing multi-MB JSON/password payloads to reach JSON decoding and Argon2id hashing. Meanwhile, `POSTGRES_SSLMODE` defaults to `"disable"` (`config.go:74`) and `redis.NewClient` (`redis.go:51-65`) has no `TLSConfig` support.
- **Remediation Specification:** Mount a global `http.MaxBytesReader` middleware (e.g., 1 MB for JSON APIs), enforce `max=128` on `LoginRequest.Password` and `ChangePasswordRequest.OldPassword`, default `POSTGRES_SSLMODE` to `require` outside development, and add TLS configuration to `RedisConfig` / `redis.NewClient`.

#### `SEC-AUD-013` — Silent Audit Log Drops on Oversized `TargetID`/`IPAddress` or Full Queue and Logger PII Masking Gaps
- **Location:** `internal/core/audit/service.go:51, 146-152`, `internal/modules/account/handler.go:183`, `migrations/000005_create_audit_logs_table.up.sql:7-8`, `internal/platform/logger/logger.go:14-19`
- **CWE / OWASP:** CWE-778 / CWE-532 / OWASP A09:2021 Security Logging and Monitoring Failures
- **Exploit Scenario:** `Login` writes `req.Email` (up to 254+ chars) into `audit_logs.target_id VARCHAR(64)`, and `ExtractClientIP` writes unvalidated `X-Forwarded-For` into `ip_address VARCHAR(45)`. Oversized inputs fail DB `INSERT` with SQLSTATE `22001`, silently dropped at `audit/service.go:51`. Queue saturation also drops records silently (`service.go:146-152`). In `logger.MaskPII`, `password_hash`, `Authorization: Basic`, and single-label host DSNs (`postgres://user:pass@postgres:5432/db`) are not masked.
- **Remediation Specification:** Normalize/bound `TargetID` and `IPAddress` before DB insertion (or widen `target_id`), log/metricize audit persistence failures and queue drops, and extend `logger.MaskPII` to redact Basic auth, `password_hash`, and `scheme://user:pass@host` DSNs (plus recursive struct/typed-map scrubbing in `audit.ScrubMap`).

#### `SEC-AUD-014` — Failing CI Security/Verify Stubs, Unpinned Container Base Images, and Unauthenticated `0.0.0.0` Compose Datastores
- **Location:** `.github/workflows/ci.yml:36-50`, `scripts/verify-full.sh:7-8`, `Dockerfile:9, 51`, `compose.yaml:61, 68-85`
- **CWE / OWASP:** CWE-1188 / CWE-1395 / OWASP A06:2021 Vulnerable and Outdated Components
- **Exploit Scenario:** `ci.yml` jobs `static-analysis` and `dependency-audit` (and `scripts/verify-full.sh`) are non-functional `TODO` stubs that exit `1`; `Dockerfile` uses mutable image tags without `@sha256:` digests; `compose.yaml` mounts `./migrations` (including `*.down.sql` `DROP TABLE` scripts) into `/docker-entrypoint-initdb.d` and exposes unauthenticated Redis (`6379`) and MongoDB (`27017`) on `0.0.0.0`.
- **Remediation Specification:** Configure `govulncheck` and `golangci-lint`/`gosec` in `ci.yml` and complete `verify-full.sh`; pin `Dockerfile` base images by `@sha256:` digest; bind `compose.yaml` datastore ports to `127.0.0.1` with authentication enabled and run migrations via `cmd/migrate`.

---

### Low

#### `SEC-AUD-015` — Wildcard Default `HTTP_ALLOWED_ORIGINS` Permits Arbitrary Cross-Origin WebSockets and Unauthenticated CORS Reads
- **Location:** `internal/config/config.go:47`, `internal/core/websocket/handler.go:151-155`, `internal/core/middleware/cors.go:28-35`
- **CWE / OWASP:** CWE-942 / CWE-346 / OWASP A05:2021 Security Misconfiguration
- **Exploit Scenario:** `HTTP_ALLOWED_ORIGINS` defaults to `"*"`, overriding `websocket.checkOrigin`'s same-origin fallback and allowing any third-party site to open WebSockets to `/realtime` or read unauthenticated endpoints via CORS (`Allow-Credentials` is omitted when `*`).
- **Remediation Specification:** Default `HTTP_ALLOWED_ORIGINS` to empty (same-origin / deny cross-origin) in non-development environments and reject `"*"` in production configs.

#### `SEC-AUD-016` — WebSocket Auth Accepts JWTs via `?token=` Query Parameter and `NewHandler` Fails Open if `tokenManager == nil`
- **Location:** `internal/core/websocket/handler.go:86-89, 117-120` (`cmd/api/main.go:243`)
- **CWE / OWASP:** CWE-598 / CWE-287 / OWASP A07:2021 Identification and Authentication Failures
- **Exploit Scenario:** `/realtime?token=<JWT>` exposes 15-minute access JWTs in ALB/reverse-proxy access logs, browser history, and `Referer` headers (app-level `MaskPII` only covers stdout logs). Separately, `HandleWebSocket` falls back to `?user_id=` if `tokenManager == nil` (currently unreachable in `cmd/api/main.go`).
- **Remediation Specification:** Issue short-lived, single-use opaque WebSocket tickets (exchanged in Redis) or use `Sec-WebSocket-Protocol` instead of `?token=<JWT>`, and reject requests when `tokenManager == nil`.

#### `SEC-AUD-017` — Production Worker Binary Imports `testing` Package and Dead `COOKIE_SECRET` / Unbound `CSRFMiddleware`
- **Location:** `internal/core/jobs/ledger.go:10, 353`, `internal/config/config.go:54`, `internal/core/middleware/csrf.go:24-61`
- **CWE / OWASP:** CWE-489 / CWE-352 / OWASP A05:2021 Security Misconfiguration
- **Exploit Scenario:** `jobs/ledger.go:10` imports `"testing"` into the production `cmd/worker` binary (`cmd/worker/main.go:13`), exposing `-test.*` CLI flags. `COOKIE_SECRET` is `required:"true"` yet unused, while `CSRFMiddleware` is unmounted and lacks session/user binding if later enabled.
- **Remediation Specification:** Move `NewTestLedger` into a `_test.go` file; remove `COOKIE_SECRET` until cookie sessions exist; and bind `CSRFMiddleware` tokens to authenticated session/user IDs.

---

## 4. Needs Validation (`NEEDS-VALIDATION`)

None (`0` items). All candidate findings were either confirmed directly in source/wiring or rejected as unwired/mitigated.

---

## 5. Rejected Candidates Log (Disprover Gate)

| ID | Candidate | Disprover Verdict & Reason | Latent Caveat |
| :--- | :--- | :--- | :--- |
| `SEC-AUD-018` | `featureflag.Engine.evaluate` & `cache.GetOrSet` | **Rejected (Unwired):** Neither `featureflag.Engine.IsEnabled` nor `cache.GetOrSet` has any production caller in `cmd/` or `internal/`; `cache.GetOrSet` also does not panic for concrete or pointer `T`. | If wired later, `allow_users`/`allow_roles` with empty `Values` fails open (`engine.go:467-478`), and non-empty interface `T` returning `nil, nil` panics (`cache.go:414`). |
| `SEC-AUD-019` | `GeoIPService`, `UploadService`, `CaptchaVerifier` | **Rejected (Unwired):** Zero non-test callers in `cmd/` or `internal/`. | If wired later: `GeoIPService` fails open on missing/empty MMDB (`geoip.go:40-59`), `UploadService` allows all MIMEs if `AllowedMIMEs` is empty (`upload.go:160-172`), `CaptchaVerifier` ignores `res.Hostname` (`captcha.go:114`). |
| `SEC-AUD-020` | `httpclient` Singleflight & `SlackService` SSRF | **Rejected (Unwired / Non-Attacker-Controlled):** `httpclient.New()` is discarded at `cmd/api/main.go:153` and `singleflightEnabled` defaults to `false`; `SlackService` URL comes strictly from `SLACK_WEBHOOK_URL` env config. | Include auth headers in singleflight keying if enabled for multi-user clients, cap response buffering, and inject `httpclient.New()` into `SlackService`. |
| `SEC-AUD-021` | `IdentifierDeviceFingerprint` & `IdentifierGuestSession` | **Rejected (Unwired):** `cmd/api/main.go:227-232` only wires `ratelimit.IdentifierIP`. | Do not wire `IdentifierDeviceFingerprint` or `IdentifierGuestSession` alone without IP fallback or signed cookies. |

---

## 6. Threat Model Updates Required

[`docs/threat-model.md`](../../threat-model.md) (and [`docs/architecture.md`](../../architecture.md)) currently contain only empty section headings (`## Assets`, `## Entry points`, `## Trust boundaries`, `## Abuse cases`, `## Mitigations`). Before any production deployment, update `docs/threat-model.md` to document:

1. **Assets:** RS256 JWT signing keys (`JWT_PRIVATE_KEY`/`JWT_PUBLIC_KEY`), Argon2id password hashes, password reset tokens (`password_resets`), Redis revocation/idempotency store, `audit_logs`, `outbox_events`, `background_jobs`, and `SLACK_WEBHOOK_URL` / `SMTP_PASSWORD` / `HCAPTCHA_SECRET`.
2. **Entry Points:**
   - Public REST (`/account/register`, `/account/login`, `/account/refresh`, `/account/forgot-password`, `/account/reset-password`)
   - Authenticated REST (`/account/me`, `/account/change-password`)
   - Public GraphQL & Docs (`/graphql`, `/playground`, `/graphiql`, `/swagger/*any`)
   - WebSocket (`/realtime` with `?token=` / `Authorization`)
   - Health Probes (`/health`, `/health/live`, `/health/ready`)
   - Background Daemons (`cmd/worker` polling `background_jobs` and `outbox_events`)
3. **Trust Boundaries:** Reverse proxy/ALB -> Gin (`SetTrustedProxies`, `X-Forwarded-For`, `X-API-Priority`), App -> Redis (revocation, idempotency, rate limit, pub/sub), App -> PostgreSQL/MongoDB, App -> External Egress (SMTP, Slack Webhook, hCaptcha, ClamAV, OTLP collector).
4. **Abuse Cases & Required Mitigations:** Cross-user response hijacking via shared `Idempotency-Key`, priority header shedding bypass, GraphQL fragment DAG CPU DoS, proxy-header rate-limit/audit bypass, post-password-reset JWT persistence, and degraded-DB nil-audit crash.
