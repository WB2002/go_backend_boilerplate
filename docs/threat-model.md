# Threat Model

This threat model reflects the current architecture and security controls of `go_backend_boilerplate`, incorporating all remediations from [`docs/reports/2026-11-21-security-audit/security-audit-report.md`](reports/2026-11-21-security-audit/security-audit-report.md) (`SEC-AUD-001` through `SEC-AUD-017`).

## Assets

| Asset | Location / Storage | Protection & Sensitivity |
| :--- | :--- | :--- |
| **RS256 JWT signing keys** | Env (`JWT_PRIVATE_KEY`, `JWT_PUBLIC_KEY`) via `config.AuthConfig` | Critical secret; signs short-lived access tokens (`JWT_ACCESS_TOKEN_TTL_SEC`, default `900`s) and refresh tokens (`JWT_REFRESH_TOKEN_TTL_SEC`, default `604800`s) in `internal/core/auth/token.go`. |
| **Argon2id password hashes** | PostgreSQL (`users.password_hash`) | Sensitive credential digest; computed via `auth.NewArgon2idHasherWithParams` (`internal/core/auth/hasher.go`) behind concurrency/queue bulkheads and `max=128` input bounds on login/password-change requests. |
| **Password reset tokens** | PostgreSQL (`password_resets.token_hash`) | High-value credential recovery secret; stored hashed with `expires_at` and single-use `used_at`, claimed atomically via `GormRepository.AtomicResetPassword`. |
| **Redis state stores** | Redis (`idempotency:*`, JTI revocations, rate limits, WebSocket pub/sub, cache, flags) | Session/control-plane integrity; keys are actor-scoped for idempotency (`ScopedIdempotencyKey`), required for JTI revocation fail-closed checks, and support TLS 1.2+ (`REDIS_USE_TLS`). |
| **Audit logs** | PostgreSQL (`audit_logs`) & structured stdout logs | Integrity and PII confidentiality; `audit.NewService` bounds `TargetID` (64 runes) and `IPAddress` (45 runes), scrubs sensitive map keys (`audit.ScrubMap`), and `logger.MaskPII` redacts tokens, Basic auth, `password_hash`, and DSNs. |
| **Outbox & background job tables** | PostgreSQL (`outbox_events`, `background_jobs`) | Operational integrity and at-least-once event/job delivery; polled by `cmd/worker/main.go` (`outbox.NewRelay`, `jobs.NewWorkerPool`). |
| **External & datastore secrets** | Env (`SLACK_WEBHOOK_URL`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `HCAPTCHA_SECRET`, `COOKIE_SECRET`, `POSTGRES_PASSWORD`, `REDIS_PASSWORD`, `MONGO_URI`) | Third-party/datastore credentials; `COOKIE_SECRET` is optional until cookie sessions are enabled (`AuthConfig`). |

## Entry points

### Public REST (`/account/*`)
- `POST /account/register`
- `POST /account/login`
- `POST /account/refresh`
- `POST /account/forgot-password`
- `POST /account/reset-password`
- Defined in `internal/modules/account/handler.go`; all pass through the global Gin middleware chain in `cmd/api/main.go`.

### Authenticated REST (`/account/*`)
- `GET /account/me`
- `POST /account/change-password`
- Protected by `middleware.AuthMiddleware(tokenManager)` (`internal/core/middleware/auth.go`), requiring a valid RS256 Bearer access token with non-empty `jti`.

### GraphQL & API Documentation
- `GET` / `POST /graphql` (`internal/core/graphql/handler.go`): enforces a 2 MB GraphQL `http.MaxBytesReader` (in addition to global 1 MB `middleware.MaxBodySize`), query validation, memoized depth/complexity engines, and per-IP/user budget limits. *(Note: `/graphql` is mounted on the root router in `cmd/api/main.go` without `authMiddleware`.)*
- `GET /playground` and `GET /graphiql` (`internal/core/graphql/handler.go`): disabled when `APP_ENV=production` unless `ENABLE_GRAPHQL_PLAYGROUND=true` (`shouldEnablePlayground` in `cmd/api/main.go`).
- `GET /swagger/*any` (`cmd/api/swagger.go`): disabled when `APP_ENV=production` unless `ENABLE_SWAGGER=true` (`registerSwagger`).

### WebSocket
- `GET /realtime` (`internal/core/websocket/handler.go`): requires `tokenManager != nil` (fails closed with `500` otherwise) and authenticates via `Authorization: Bearer <token>` or `Sec-WebSocket-Protocol` (`bearer, <token>`, `access_token, <token>`, or single-token subprotocol). Raw `?token=` query parameters are rejected with `401` when `APP_ENV=production`; `?user_id=` fallback is removed; origins are checked against `HTTP_ALLOWED_ORIGINS` (default empty / same-origin).

### Health & Readiness Probes
- `GET /health`, `GET /health/ready`, `GET /health/live` (`internal/core/telemetry/health.go`).
- Outside `APP_ENV=development`, unauthenticated `/health` responses redact raw dependency `Error` strings and omit `SystemStats` without mutating cached health state.

### Background Daemons (`cmd/worker/main.go`)
- Runs `jobs.NewWorkerPool` polling `background_jobs` (backed by PostgreSQL and optional Redis ledger) and `outbox.NewRelay` polling `outbox_events` and publishing to Redis via `outbox.NewRedisPublisher`.
- Production worker binary does not link the Go `testing` package (`NewTestLedger` resides in `internal/core/jobs/ledger_test.go`).

## Trust boundaries

1. **Reverse Proxy / ALB -> Gin (`cmd/api/main.go`)**
   - `router.SetTrustedProxies(cfg.HTTP.TrustedProxies)` (`HTTP_TRUSTED_PROXIES`, default `nil`) prevents untrusted clients from spoofing `X-Forwarded-For` or `X-Real-IP`.
   - `middleware.ExtractClientIP` delegates to `c.ClientIP()` and validates with `net.ParseIP` (falling back to `"127.0.0.1"`).
   - `overload.Manager.ExtractPriority` ignores client-supplied `X-API-Priority` and `X-Priority` headers; priority derives only from server-set context keys, route registrations, or defaults.
   - Global middlewares enforce `MaxBodySize(1<<20)`, `CORS(cfg.HTTP.AllowedOrigins)` (non-wildcard empty default), `RateLimitWithLimiter` (`ratelimit.IdentifierIP`), and `IdempotencyWithConfig`.

2. **App -> Redis**
   - Stores JWT JTI revocations, scoped idempotency caches/leases, rate-limit counters, WebSocket pub/sub messages, cache entries, and feature flags.
   - `internal/platform/redis/redis.go` enables TLS 1.2+ (`tls.VersionTLS12`) when `REDIS_USE_TLS=true`. Local `compose.yaml` enforces `REDIS_PASSWORD` authentication and binds `127.0.0.1:6379`.

3. **App -> PostgreSQL & MongoDB**
   - PostgreSQL stores `users`, `password_resets`, `audit_logs`, `outbox_events`, and `background_jobs`; `config.applyEnvironmentDefaults` defaults `POSTGRES_SSLMODE` to `require` outside `development` (`disable` in `development`).
   - MongoDB connects via `MONGO_URI` (`internal/platform/mongodb/`).
   - Local `compose.yaml` binds PostgreSQL (`5432`), Redis (`6379`), and MongoDB (`27017`) to `127.0.0.1` and relies on `cmd/migrate` rather than mounting `./migrations` (including `*.down.sql`) into `/docker-entrypoint-initdb.d`.

4. **App -> External Egress**
   - **SMTP (`internal/core/email/smtp.go`)**: `SMTPMailer.Send` bounds dial and active SMTP sessions by `ctx.Deadline()` / `ctx.Done()`, requires TLS 1.2+ on `STARTTLS`, and strips `\r`/`\n` from header values (`From`, `To`, `Subject`).
   - **OpenTelemetry (`internal/core/telemetry/otel.go`)**: `InitTracer` requires TLS by default for `OTEL_EXPORTER_OTLP_ENDPOINT` unless `OTEL_EXPORTER_OTLP_INSECURE=true` or an explicit `http://` scheme is configured.
   - **Slack Webhook (`internal/core/notification/slack.go`)**: Webhook destination is strictly server-configured via `SLACK_WEBHOOK_URL`.
   - **Unwired Security / Egress Utilities**: `httpclient`, `GeoIPService`, `UploadService`, and `CaptchaVerifier` (`HCAPTCHA_SECRET`, `CLAMAV_ADDR`) are not wired into active HTTP routes; if wired in future features, their fail-open/singleflight caveats documented in Section 5 of the audit report must be addressed first.

## Abuse cases

- **Cross-user response hijacking via shared `Idempotency-Key`**: An unauthenticated attacker sends `Idempotency-Key: <victim-key>` to steal cached login/refresh `TokenResponse` payloads or bypass `AuthMiddleware` (`SEC-AUD-001`).
- **Degraded-DB nil-audit crash**: Unauthenticated requests trigger async audit logging when PostgreSQL failed at startup (`db == nil`), panicking an unrecovered worker goroutine (`SEC-AUD-002`).
- **Proxy-header rate-limit & audit bypass**: Unconfigured trusted proxies allow attackers to rotate `X-Forwarded-For` to bypass IP rate limits and GraphQL budgets, or send >45-char headers that fail SQL inserts into `audit_logs.ip_address` (`SEC-AUD-003`).
- **GraphQL fragment DAG CPU DoS**: Non-cyclic exponential fragment DAGs (e.g., 25–30 binary-branching fragments) bypass `no_fragment_cycles` validation and cause $O(2^N)$ AST traversal CPU spin or integer overflow (`SEC-AUD-004`).
- **Post-password-reset JWT persistence**: Compromised refresh tokens remain valid after `change-password` or `reset-password` if `password_changed_at` is not enforced (`SEC-AUD-005`).
- **Priority header shedding bypass**: Clients send `X-API-Priority: critical` or `X-Priority: critical` to claim overload reserve capacity and evade 503 load shedding (`SEC-AUD-006`).
- **Refresh token replay & empty-`jti` bypass**: Ignoring Redis `RevokeToken` errors or accepting JWTs without `jti` enables refresh token replay (`SEC-AUD-007`).
- **Password reset TOCTOU race**: Concurrent `POST /account/reset-password` requests or post-update marking errors allow a single reset token to succeed multiple times (`SEC-AUD-008`).
- **Broken/blocking password reset email & timing leak**: Empty reset bodies fail account recovery, while blocking `smtp.SendMail` ignores `RequestDeadline` context cancellation (`SEC-AUD-009`).
- **Production exposure of docs/debug routes & oversized payloads**: Exposed Swagger/GraphQL Playgrounds, verbose `/health` errors, plaintext transport defaults, wildcard CORS/WS origins, `?token=` JWT URL leakage, `-test.*` flags in worker binaries, and unhardened CI/Docker/Compose defaults (`SEC-AUD-010`–`SEC-AUD-017`).

## Mitigations

| Finding ID | Threat / Abuse Case | Implemented Mitigation & Code Reference |
| :--- | :--- | :--- |
| `SEC-AUD-001` | Cross-user response hijacking via global `Idempotency-Key` | `middleware.IdempotencyWithConfig` (`internal/core/middleware/idempotency.go`, `cmd/api/main.go`) scopes cache keys via `ScopedIdempotencyKey` (`actor:METHOD:cleanPath:key`), hashes request bodies (SHA-256, `422` on mismatch), strips `Set-Cookie`, releases leases on `401`/`403`/`>=500`, and exempts `/account/login`, `/account/refresh`, and `/account/reset-password`. |
| `SEC-AUD-002` | Nil-pointer panic in async audit worker when DB is nil | `audit.service.Log` returns early when `s.db == nil`; `NewService` wraps `processEntry` with `defer recover()` and structured panic logging (`internal/core/audit/service.go`). |
| `SEC-AUD-003` | Spoofed `X-Forwarded-For` rate-limit bypass and invalid audit IPs | `cmd/api/main.go` calls `router.SetTrustedProxies(cfg.HTTP.TrustedProxies)` (default `nil`); `middleware.ExtractClientIP` (`internal/core/middleware/audit.go`) uses `c.ClientIP()` validated by `net.ParseIP`. |
| `SEC-AUD-004` | Exponential GraphQL fragment DAG CPU DoS & int overflow | `DepthEngine.CalculateDepth` and `ComplexityEngine.CalculateComplexity` (`internal/core/graphql/complexity.go`) memoize per-fragment values, enforce `maxASTTraversalVisits = 5000` via `checkContextAndVisitCap`, check `ctx.Err()`, and perform overflow-safe bounded accumulation. |
| `SEC-AUD-005` | Pre-change refresh tokens remain valid after password change/reset | Migration `000006_add_password_changed_at_to_users` adds `users.password_changed_at`; `UpdatePasswordWithTimestamp` and `AtomicResetPassword` update it; `Handler.Refresh` rejects tokens issued before `PasswordChangedAt` (`401 TOKEN_INVALIDATED`). *(Short-lived 15m access tokens remain stateless until expiry.)* |
| `SEC-AUD-006` | Untrusted `X-API-Priority` / `X-Priority` overload bypass | `overload.Manager.ExtractPriority` (`internal/core/overload/manager.go`) removed all inspection of client `X-API-Priority` and `X-Priority` headers. |
| `SEC-AUD-007` | Refresh token replay via ignored `RevokeToken` error or empty `jti` | `auth.validateToken` (`internal/core/auth/token.go`) requires non-empty `claims.ID` (`jti`); `Handler.Refresh` (`internal/modules/account/handler.go`) fails closed with `503 TOKEN_REVOCATION_FAILED` if `RevokeToken` errors. |
| `SEC-AUD-008` | Password reset TOCTOU race condition | `GormRepository.AtomicResetPassword` (`internal/modules/account/repository.go`) atomically claims `password_resets` (`used_at IS NULL AND expires_at > now`) and updates `users.password_hash` and `password_changed_at` inside one DB transaction. |
| `SEC-AUD-009` | Empty reset emails and uncancelled blocking `smtp.SendMail` | `ForgotPassword` (`internal/modules/account/handler.go`) renders `BodyText` and escaped `BodyHTML` with the reset token; `SMTPMailer.Send` (`internal/core/email/smtp.go`) uses `net.Dialer.DialContext`, `conn.SetDeadline`, `ctx.Done()` cancellation watching, TLS 1.2+ on `STARTTLS`, and `\r`/`\n` header sanitization. |
| `SEC-AUD-010` | Unconditional Swagger & GraphQL Playground exposure in production | `registerSwagger` (`cmd/api/swagger.go`) and `shouldEnablePlayground` (`cmd/api/main.go`, `graphql.WithPlayground`) disable `/swagger/*any`, `/playground`, and `/graphiql` in `production` unless `ENABLE_SWAGGER=true` / `ENABLE_GRAPHQL_PLAYGROUND=true`. |
| `SEC-AUD-011` | Verbose `/health` error/runtime leaks and plaintext OTLP default | `HealthChecker` (`internal/core/telemetry/health.go`) redacts dependency errors and omits `SystemStats` outside `development`; `InitTracer` (`internal/core/telemetry/otel.go`) requires TLS unless `OTEL_EXPORTER_OTLP_INSECURE=true` or `http://` is specified. |
| `SEC-AUD-012` | Missing HTTP body limits, unbounded password fields, plaintext DB/Redis defaults | `middleware.MaxBodySize(1<<20)` (`internal/core/middleware/request_size.go`) mounted globally in `cmd/api/main.go`; `max=128` on `LoginRequest.Password` and `ChangePasswordRequest.OldPassword` (`internal/modules/account/model.go`); `POSTGRES_SSLMODE` defaults to `require` outside `development`; `REDIS_USE_TLS` adds TLS 1.2+ in `internal/platform/redis/redis.go`. |
| `SEC-AUD-013` | Silent audit drops on oversized fields/full queues and PII mask gaps | `audit.service.Log` (`internal/core/audit/service.go`) truncates `TargetID` (64 runes) and `IPAddress` (45 runes) via `truncateRunes` and logs queue saturation warnings; `logger.MaskPII` (`internal/platform/logger/logger.go`) adds `basicAuthRegex`, `dsnRegex`, and `passwordHashRegex`. |
| `SEC-AUD-014` | Failing CI security stubs, unpinned Docker images, exposed Compose datastores | `.github/workflows/ci.yml` runs SHA-pinned `golangci-lint-action` (`v1.64.8`) and `govulncheck@v1.1.4`; `scripts/verify-full.sh` runs integration tests; `Dockerfile` pins `@sha256:` digests; `compose.yaml` binds datastores to `127.0.0.1`, enables `redis-server --requirepass`, and drops initdb `./migrations` mount. *(Note: `govulncheck` toolchain/dependency upgrades remain tracked separately.)* |
| `SEC-AUD-015` | Wildcard default `HTTP_ALLOWED_ORIGINS` allowing cross-origin WS/CORS | `config.HTTPConfig.AllowedOrigins` (`internal/config/config.go`) defaults to `""` (empty / fail-closed same-origin). |
| `SEC-AUD-016` | WebSocket `?token=` URL exposure and nil `tokenManager` fail-open | `websocket.Handler.HandleWebSocket` (`internal/core/websocket/handler.go`) fails closed when `tokenManager == nil`, removes `?user_id=` fallback, rejects `?token=` in `production`, and supports `Authorization: Bearer` and `Sec-WebSocket-Protocol`. |
| `SEC-AUD-017` | Worker `testing` import, required unused `COOKIE_SECRET`, unbound CSRF | `NewTestLedger` moved to `internal/core/jobs/ledger_test.go` (`cmd/worker` no longer imports `testing`); `COOKIE_SECRET` is optional; `internal/core/middleware/csrf.go` adds `GenerateBoundCSRFToken` and `ValidateBoundCSRFToken` bound to `sessionID`. |
