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
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/core/audit"
	"go_backend_boilerplate/internal/core/auth"
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
	repo := NewMockRepository()
	hasher := auth.NewArgon2idHasher(2, 4, 2*time.Second)
	auditSvc := &mockAuditService{}

	privPEM, pubPEM := generateTestRSAKeys()
	tokenMgr, err := auth.NewJWTTokenManager(privPEM, pubPEM, 15*time.Minute, 7*24*time.Hour, nil)
	require.NoError(t, err)

	hash, _ := hasher.HashPasswordContext(context.Background(), "Password123!")
	user := &User{
		ID:           uuid.NewString(),
		Email:        "audituser@example.com",
		PasswordHash: hash,
		Status:       "active",
		Roles:        Roles{"user"},
	}
	_ = repo.Create(context.Background(), user)

	handler := NewHandlerWithAudit(repo, hasher, tokenMgr, nil, nil, auditSvc)
	r := gin.New()
	handler.RegisterRoutes(r, func(c *gin.Context) {
		c.Set("user", &auth.Claims{UserID: user.ID})
	})

	// 1. Test Login Failure Audit
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

	// 2. Test Login Success Audit
	successLoginPayload, _ := json.Marshal(map[string]string{
		"email":    "audituser@example.com",
		"password": "Password123!",
	})
	wSuccess := httptest.NewRecorder()
	reqSuccess, _ := http.NewRequest("POST", "/account/login", bytes.NewReader(successLoginPayload))
	reqSuccess.Header.Set("Content-Type", "application/json")
	reqSuccess.Header.Set("User-Agent", "Mozilla/5.0 SuccessAgent")
	r.ServeHTTP(wSuccess, reqSuccess)

	assert.Equal(t, http.StatusOK, wSuccess.Code)
	lastEntry = auditSvc.entries[len(auditSvc.entries)-1]
	assert.Equal(t, "account.login", lastEntry.Action)
	assert.Equal(t, "success", lastEntry.Status)
	assert.Equal(t, user.ID, lastEntry.ActorID)
	assert.Equal(t, user.Email, lastEntry.ActorEmail)

	// 3. Test Change Password Audit
	changePayload, _ := json.Marshal(map[string]string{
		"oldPassword": "Password123!",
		"newPassword": "NewSecurePassword456!",
	})
	wChange := httptest.NewRecorder()
	reqChange, _ := http.NewRequest("POST", "/account/change-password", bytes.NewReader(changePayload))
	reqChange.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wChange, reqChange)

	assert.Equal(t, http.StatusOK, wChange.Code)
	lastEntry = auditSvc.entries[len(auditSvc.entries)-1]
	assert.Equal(t, "account.change_password", lastEntry.Action)
	assert.Equal(t, "success", lastEntry.Status)
	assert.Equal(t, user.ID, lastEntry.ActorID)
}
