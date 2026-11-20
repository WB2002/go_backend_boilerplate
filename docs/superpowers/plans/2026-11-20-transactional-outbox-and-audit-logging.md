# Transactional Outbox Pattern & Structured Audit Logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a production-grade Transactional Outbox Pattern for guaranteed at-least-once event/job dispatch and a tamper-evident Structured Audit Logging system for regulatory compliance and operational visibility.

**Architecture:** Extend `internal/core/outbox` and `internal/platform/database` with transactional boundary management (`database.WithTransaction`), allowing domain mutations and outbox events/jobs to be committed atomically in PostgreSQL. An autonomous background Outbox Relay (`SKIP LOCKED`) sweeps pending outbox entries and publishes them across Redis Pub/Sub and job queues with exponential backoff. A dedicated `internal/core/audit` subsystem captures actor identities, IP addresses, user agents, action names, and JSON diffs, recording immutable audit records asynchronously without blocking HTTP client request lifecycles.

**Tech Stack:** Go 1.23+, GORM v2 (`gorm.io/gorm`), PostgreSQL 16 (`gorm.io/driver/postgres`), Go-Redis v9 (`github.com/redis/go-redis/v9`), Zerolog, Gin.

**Spec:** [docs/architecture.md](file:///Users/pc/Documents/personal/go_backend_boilerplate/docs/architecture.md) & [docs/nfr.md](file:///Users/pc/Documents/personal/go_backend_boilerplate/docs/nfr.md).

## Global Constraints

- **Language & Runtime:** Go 1.23+ with strict Go modules (`go.mod`).
- **No Dual-Write Inconsistency:** Domain state changes and outbox records must be committed in the same database transaction.
- **Fail-Open Audit Ingestion:** An audit log storage failure must log a structured error but not abort customer-facing read requests. Critical compliance writes must support synchronous enforcement.
- **Zero Lock Contention:** Outbox batch polling must utilize PostgreSQL `SELECT ... FOR UPDATE SKIP LOCKED`.
- **Strict Verification:** Every task must feature automated unit/integration tests; `./scripts/verify.sh` must pass cleanly after each task.

## Review Focus

1. **Transaction Rollback Dual-Write Protection:** If a business transaction fails (e.g. invalid user state, constraint error), no outbox event or background job must ever be published or leaked to Redis. *Owned by Task 1.*
2. **Outbox Relay Crash & At-Least-Once Delivery:** If an outbox relay node crashes halfway through publishing a batch, claimed outbox events must have their leases reclaimed or be retried without permanent stalling. *Owned by Task 2.*
3. **Actor Context Extraction & Masking:** Audit logs must correctly extract authenticated actor metadata (`user_id`, `email`, `roles`, client `IP`, `User-Agent`) while strictly redacting sensitive fields like raw passwords, tokens, and credit card numbers from `changes` payloads. *Owned by Task 3.*
4. **Outbox Poison Pill Dead-Lettering:** Malformed payloads or perpetually failing external event consumers must not block the entire outbox queue; failures must increment `retry_count` and move to `failed` state after max retries. *Owned by Task 2.*
5. **High-Throughput Audit DB Exhaustion:** Bursts of concurrent audit logging must not starve the primary PostgreSQL connection pool; writes must leverage batching or asynchronous channel buffers with bounded queues. *Owned by Task 4.*

---

### Task 1: Transactional Boundary Helper & Outbox Store

**Files:**
- Create: `migrations/000004_create_outbox_events_table.up.sql`
- Create: `migrations/000004_create_outbox_events_table.down.sql`
- Create: `internal/platform/database/tx.go`
- Create: `internal/core/outbox/model.go`
- Create: `internal/core/outbox/store.go`
- Create: `internal/core/outbox/outbox_test.go`
- Modify: `internal/core/jobs/ledger.go:60-120`

**Interfaces:**
- Consumes: `*gorm.DB`
- Produces:
  - `database.WithTransaction(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error`
  - `outbox.Event` (struct with `ID`, `AggregateType`, `AggregateID`, `EventType`, `Payload`, `Headers`, `State`, `RetryCount`, `CreatedAt`, `PublishedAt`)
  - `outbox.Store` interface: `CreateEvent(ctx context.Context, tx *gorm.DB, event *Event) error`
  - `jobs.Ledger.EnqueueTx(ctx context.Context, tx *gorm.DB, queue string, payload []byte, dedupKey string) (string, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/core/outbox/outbox_test.go
package outbox

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"go_backend_boilerplate/internal/platform/database"
)

func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	err = db.AutoMigrate(&Event{})
	require.NoError(t, err)
	return db
}

func TestTransactionalOutboxRollback(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	ctx := context.Background()

	// 1. Transaction that fails and rolls back
	err := database.WithTransaction(ctx, db, func(tx *gorm.DB) error {
		evt := &Event{
			ID:            uuid.NewString(),
			AggregateType: "user",
			AggregateID:   "usr-123",
			EventType:     "user.created",
			Payload:       []byte(`{"email":"test@example.com"}`),
			State:         StatePending,
		}
		if err := store.CreateEvent(ctx, tx, evt); err != nil {
			return err
		}
		return errors.New("simulated business error")
	})
	assert.Error(t, err)

	// Verify no outbox record exists
	var count int64
	db.Model(&Event{}).Count(&count)
	assert.Equal(t, int64(0), count, "Outbox event must be rolled back on error")

	// 2. Transaction that succeeds and commits
	err = database.WithTransaction(ctx, db, func(tx *gorm.DB) error {
		evt := &Event{
			ID:            uuid.NewString(),
			AggregateType: "user",
			AggregateID:   "usr-456",
			EventType:     "user.created",
			Payload:       []byte(`{"email":"committed@example.com"}`),
			State:         StatePending,
		}
		return store.CreateEvent(ctx, tx, evt)
	})
	assert.NoError(t, err)

	db.Model(&Event{}).Count(&count)
	assert.Equal(t, int64(1), count, "Outbox event must persist on successful commit")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/outbox -run TestTransactionalOutboxRollback`
Expected: FAIL with undefined: `Event`, `NewStore`, `database.WithTransaction`

- [ ] **Step 3: Implement database transaction helper, outbox migration, and store**

1. Create `internal/platform/database/tx.go`:
   - `WithTransaction(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error`: wraps `db.WithContext(ctx).Transaction(fn)`.
2. Create `migrations/000004_create_outbox_events_table.up.sql` and `down.sql`:
   - Columns: `id UUID PRIMARY KEY`, `aggregate_type VARCHAR(64) NOT NULL`, `aggregate_id VARCHAR(64) NOT NULL`, `event_type VARCHAR(128) NOT NULL`, `payload BYTEA NOT NULL`, `headers JSONB`, `state VARCHAR(32) NOT NULL DEFAULT 'pending'`, `retry_count INT NOT NULL DEFAULT 0`, `last_error TEXT`, `created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`, `published_at TIMESTAMPTZ`.
   - Index on `(state, created_at)`.
3. Create `internal/core/outbox/model.go`:
   - `type State string` (`StatePending`, `StatePublished`, `StateFailed`).
   - `type Event struct { ... }`.
4. Create `internal/core/outbox/store.go`:
   - `type Store interface { CreateEvent(ctx context.Context, tx *gorm.DB, evt *Event) error }`.
   - `GormStore` implementation using `tx.Create(evt)`.
5. Modify `internal/core/jobs/ledger.go`:
   - Add `EnqueueTx(ctx context.Context, tx *gorm.DB, queue string, payload []byte, deduplicationKey string) (string, error)` to `Ledger` interface and `PostgresLedger`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/outbox -run TestTransactionalOutboxRollback`
Expected: PASS

- [ ] **Step 5: Run verify and commit**

```bash
./scripts/verify.sh
git add migrations/000004* internal/platform/database/tx* internal/core/outbox/ internal/core/jobs/
git commit -m "feat(outbox): add transactional outbox schema, store, and EnqueueTx"
```

---

### Task 2: Autonomous Outbox Relay & Background Publisher Daemon

**Files:**
- Create: `internal/core/outbox/relay.go`
- Create: `internal/core/outbox/publisher.go`
- Create: `internal/core/outbox/relay_test.go`
- Modify: `cmd/worker/main.go:50-70`

**Interfaces:**
- Consumes: `*gorm.DB`, `goredis.Cmdable`, `outbox.Store`
- Produces:
  - `outbox.Publisher` interface: `Publish(ctx context.Context, event *Event) error`
  - `outbox.RedisPublisher`: Publishes events to Redis channel `events:<event_type>`
  - `outbox.Relay`: Autonomous poller `Start(ctx context.Context) error` sweeping pending events with `SELECT ... FOR UPDATE SKIP LOCKED` and marking them published.

- [ ] **Step 1: Write the failing test**

```go
// internal/core/outbox/relay_test.go
package outbox

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type mockPublisher struct {
	mu        sync.Mutex
	published []*Event
}

func (m *mockPublisher) Publish(ctx context.Context, event *Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, event)
	return nil
}

func TestOutboxRelayProcessing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	err = db.AutoMigrate(&Event{})
	require.NoError(t, err)

	store := NewStore(db)
	pub := &mockPublisher{}

	// Insert 2 pending events
	ctx := context.Background()
	_ = store.CreateEvent(ctx, db, &Event{
		ID:            uuid.NewString(),
		AggregateType: "order",
		AggregateID:   "ord-1",
		EventType:     "order.paid",
		Payload:       []byte(`{"total": 50}`),
		State:         StatePending,
		CreatedAt:     time.Now(),
	})
	_ = store.CreateEvent(ctx, db, &Event{
		ID:            uuid.NewString(),
		AggregateType: "order",
		AggregateID:   "ord-2",
		EventType:     "order.shipped",
		Payload:       []byte(`{"carrier": "fedex"}`),
		State:         StatePending,
		CreatedAt:     time.Now(),
	})

	relay := NewRelay(db, pub, RelayConfig{
		BatchSize:    10,
		PollInterval: 10 * time.Millisecond,
		MaxRetries:   3,
	})

	relayCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_ = relay.Start(relayCtx)

	// Verify both published
	pub.mu.Lock()
	assert.Len(t, pub.published, 2)
	pub.mu.Unlock()

	// Verify state in DB transitioned to published
	var pendingCount int64
	db.Model(&Event{}).Where("state = ?", StatePublished).Count(&pendingCount)
	assert.Equal(t, int64(2), pendingCount)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/outbox -run TestOutboxRelayProcessing`
Expected: FAIL with undefined: `NewRelay`, `RelayConfig`, `Publisher`

- [ ] **Step 3: Implement Publisher, RedisPublisher, and Outbox Relay**

1. Create `internal/core/outbox/publisher.go`:
   - `Publisher` interface: `Publish(ctx context.Context, event *Event) error`.
   - `RedisPublisher` publishing to Redis channel `fmt.Sprintf("outbox:events:%s", event.EventType)` and optional fan-out to jobs queue.
2. Create `internal/core/outbox/relay.go`:
   - `RelayConfig` (`BatchSize int`, `PollInterval time.Duration`, `MaxRetries int`).
   - `Relay` struct managing background ticker and worker lock.
   - Batch claim using `tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state = ?", StatePending).Limit(batchSize).Find(&events)`.
   - On publish success: update `state = StatePublished`, `published_at = NOW()`.
   - On publish failure: increment `retry_count`, if `retry_count >= maxRetries` set `state = StateFailed`, record `last_error`.
3. Update `cmd/worker/main.go` to launch `outboxRelay.Start(ctx)` as an autonomous goroutine alongside the background jobs pool.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/outbox -run TestOutboxRelayProcessing`
Expected: PASS

- [ ] **Step 5: Run verify and commit**

```bash
./scripts/verify.sh
git add internal/core/outbox/ cmd/worker/main.go
git commit -m "feat(outbox): add autonomous outbox relay daemon with SKIP LOCKED claiming"
```

---

### Task 3: Structured Audit Log Schema & Repository

**Files:**
- Create: `migrations/000005_create_audit_logs_table.up.sql`
- Create: `migrations/000005_create_audit_logs_table.down.sql`
- Create: `internal/core/audit/model.go`
- Create: `internal/core/audit/service.go`
- Create: `internal/core/audit/audit_test.go`

**Interfaces:**
- Consumes: `*gorm.DB`, Zerolog logger
- Produces:
  - `audit.Entry` (struct: `ID`, `ActorID`, `ActorEmail`, `Action`, `TargetType`, `TargetID`, `IPAddress`, `UserAgent`, `Changes map[string]any`, `Status`, `ErrorMessage`, `CreatedAt`)
  - `audit.Service` interface: `Log(ctx context.Context, entry Entry) error` and `LogAsync(entry Entry)`
  - Automatic PII/secret scrubbing of sensitive keys (`password`, `token`, `secret`, `authorization`, `credit_card`) in `Changes`.

- [ ] **Step 1: Write the failing test**

```go
// internal/core/audit/audit_test.go
package audit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAuditLogPersistenceAndScrubbing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	err = db.AutoMigrate(&LogRecord{})
	require.NoError(t, err)

	service := NewService(db, Config{
		BufferSize: 100,
	})
	defer service.Close()

	ctx := context.Background()

	entry := Entry{
		ActorID:    "usr-admin-1",
		ActorEmail: "admin@example.com",
		Action:     "user.password_reset",
		TargetType: "user",
		TargetID:   "usr-target-2",
		IPAddress:  "192.168.1.1",
		UserAgent:  "Mozilla/5.0",
		Status:     "success",
		Changes: map[string]any{
			"password":     "SuperSecretPlainPassword123!",
			"accessToken":  "jwt.token.string",
			"target_email": "target@example.com",
			"role":         "editor",
		},
	}

	err = service.Log(ctx, entry)
	assert.NoError(t, err)

	var record LogRecord
	err = db.First(&record, "actor_id = ?", "usr-admin-1").Error
	require.NoError(t, err)

	assert.Equal(t, "user.password_reset", record.Action)
	assert.Equal(t, "192.168.1.1", record.IPAddress)

	// Verify sensitive fields are masked in JSON payload
	changesJSON := string(record.Changes)
	assert.NotContains(t, changesJSON, "SuperSecretPlainPassword123!")
	assert.NotContains(t, changesJSON, "jwt.token.string")
	assert.Contains(t, changesJSON, "[REDACTED]")
	assert.Contains(t, changesJSON, "target@example.com")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/core/audit -run TestAuditLogPersistenceAndScrubbing`
Expected: FAIL with undefined: `LogRecord`, `NewService`, `Entry`

- [ ] **Step 3: Implement Audit models, migration, and service with secret scrubbing**

1. Create `migrations/000005_create_audit_logs_table.up.sql` and `down.sql`:
   - Columns: `id UUID PRIMARY KEY`, `actor_id VARCHAR(64) NOT NULL`, `actor_email VARCHAR(255)`, `action VARCHAR(128) NOT NULL`, `target_type VARCHAR(64) NOT NULL`, `target_id VARCHAR(64) NOT NULL`, `ip_address VARCHAR(45)`, `user_agent TEXT`, `changes JSONB`, `status VARCHAR(32) NOT NULL DEFAULT 'success'`, `error_message TEXT`, `created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`.
   - Indexes on `(action, created_at)`, `(actor_id, created_at)`, and `(target_type, target_id)`.
2. Create `internal/core/audit/model.go`:
   - `LogRecord` GORM model and `Entry` DTO.
3. Create `internal/core/audit/service.go`:
   - Deep key-value sanitizer scrubbing keys matching `password`, `token`, `secret`, `auth`, `api_key`, `key` with `[REDACTED]`.
   - `Log(ctx context.Context, entry Entry) error`: Synchronous GORM insert.
   - `LogAsync(entry Entry)`: Non-blocking buffered channel queue with background worker drain.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/core/audit -run TestAuditLogPersistenceAndScrubbing`
Expected: PASS

- [ ] **Step 5: Run verify and commit**

```bash
./scripts/verify.sh
git add migrations/000005* internal/core/audit/
git commit -m "feat(audit): add structured audit logging schema and sanitized persistence service"
```

---

### Task 4: Audit Context Middleware & Account Domain Integration

**Files:**
- Create: `internal/core/middleware/audit.go`
- Create: `internal/core/middleware/audit_test.go`
- Modify: `internal/modules/account/handler.go:40-350`
- Modify: `cmd/api/main.go:190-230`

**Interfaces:**
- Consumes: `audit.Service`, `gin.Context`
- Produces:
  - `middleware.AuditContext() gin.HandlerFunc`: Attaches client IP and User-Agent to context.
  - Integration in Account Handlers:
    - Audit `account.login`: Logs success with user ID/email, or logs failure with invalid email.
    - Audit `account.change_password`: Logs password rotation event.
    - Audit `account.reset_password`: Logs password recovery completion.

- [ ] **Step 1: Write the failing test**

```go
// internal/modules/account/audit_integration_test.go
package account_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/core/audit"
	"go_backend_boilerplate/internal/core/auth"
	"go_backend_boilerplate/internal/modules/account"
)

type mockAuditService struct {
	entries []audit.Entry
}

func (m *mockAuditService) Log(ctx context.Context, entry audit.Entry) error {
	m.entries = append(m.entries, entry)
	return nil
}

func (m *mockAuditService) LogAsync(entry audit.Entry) {
	m.entries = append(m.entries, entry)
}

func (m *mockAuditService) Close() error { return nil }

func TestAccountHandlerAuditTrail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := account.NewMockRepository()
	hasher := auth.NewArgon2idHasher(2, 4, 2*time.Second)
	auditSvc := &mockAuditService{}

	hash, _ := hasher.HashPasswordContext(context.Background(), "Password123!")
	user := &account.User{
		ID:           uuid.NewString(),
		Email:        "audituser@example.com",
		PasswordHash: hash,
		Status:       "active",
		Roles:        account.Roles{"user"},
	}
	_ = repo.Create(context.Background(), user)

	handler := account.NewHandlerWithAudit(repo, hasher, nil, nil, nil, auditSvc)
	r := gin.New()
	handler.RegisterRoutes(r, func(c *gin.Context) {})

	// Test Login Audit
	loginPayload, _ := json.Marshal(map[string]string{
		"email":    "audituser@example.com",
		"password": "WrongPassword999!",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/account/login", bytes.NewReader(loginPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TestBrowser/1.0")
	req.RemoteAddr = "203.0.113.195:1234"
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	require.NotEmpty(t, auditSvc.entries)
	lastEntry := auditSvc.entries[len(auditSvc.entries)-1]
	assert.Equal(t, "account.login", lastEntry.Action)
	assert.Equal(t, "failure", lastEntry.Status)
	assert.Equal(t, "audituser@example.com", lastEntry.ActorEmail)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/modules/account -run TestAccountHandlerAuditTrail`
Expected: FAIL with undefined: `NewHandlerWithAudit`

- [ ] **Step 3: Implement Audit middleware and wire into Account Handlers & API main**

1. Create `internal/core/middleware/audit.go`:
   - Helper extracting real client IP (respecting `X-Forwarded-For` / `X-Real-IP`) and `User-Agent`.
2. Update `internal/modules/account/handler.go`:
   - Add `audit audit.Service` field to `Handler`.
   - Add `NewHandlerWithAudit(...)` constructor.
   - In `Login`: record `account.login` audit entry for both success and failure with IP and User-Agent.
   - In `ChangePassword`: record `account.change_password` audit entry.
   - In `ResetPassword`: record `account.reset_password` audit entry.
3. Update `cmd/api/main.go` to initialize `audit.NewService(db)` and inject into `accountHandler`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/modules/account -run TestAccountHandlerAuditTrail`
Expected: PASS

- [ ] **Step 5: Run verify and commit**

```bash
./scripts/verify.sh
git add internal/core/middleware/audit* internal/modules/account/ cmd/api/main.go
git commit -m "feat(audit): integrate audit logging middleware and account domain activity tracking"
```
