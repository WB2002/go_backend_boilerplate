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

	"go_backend_boilerplate/internal/core/auth"
	"go_backend_boilerplate/internal/core/email"
	"go_backend_boilerplate/internal/modules/account"
)

func TestForgotPasswordAndResetPasswordFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := account.NewMockRepository()
	hasher := auth.NewArgon2idHasher(2, 4, 2*time.Second)
	mockMailer := email.NewMockMailer()

	user := &account.User{
		ID:           uuid.NewString(),
		Email:        "reset@example.com",
		PasswordHash: "oldhash",
		Status:       "active",
	}
	err := repo.Create(context.Background(), user)
	require.NoError(t, err)

	handler := account.NewHandlerWithMailer(repo, hasher, nil, nil, mockMailer)
	r := gin.New()
	handler.RegisterRoutes(r, func(c *gin.Context) {})

	// 1. Request password reset
	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"email": "reset@example.com"})
	req, _ := http.NewRequest("POST", "/account/forgot-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, mockMailer.SentMessages, 1)
	assert.Equal(t, "reset@example.com", mockMailer.SentMessages[0].To)
	resetToken, ok := mockMailer.SentMessages[0].Data["Token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, resetToken)

	// Timing attack prevention: non-existent email also returns 200 without email sent
	wAnon := httptest.NewRecorder()
	anonBody, _ := json.Marshal(map[string]string{"email": "nonexistent@example.com"})
	reqAnon, _ := http.NewRequest("POST", "/account/forgot-password", bytes.NewReader(anonBody))
	reqAnon.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wAnon, reqAnon)
	assert.Equal(t, http.StatusOK, wAnon.Code)
	assert.Len(t, mockMailer.SentMessages, 1) // still only 1 message sent

	// 2. Perform password reset with valid token
	wReset := httptest.NewRecorder()
	resetPayload, _ := json.Marshal(map[string]string{
		"token":       resetToken,
		"newPassword": "NewSecurePassword123!",
	})
	reqReset, _ := http.NewRequest("POST", "/account/reset-password", bytes.NewReader(resetPayload))
	reqReset.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wReset, reqReset)

	assert.Equal(t, http.StatusOK, wReset.Code)

	// 3. Replay attack: using the same token again must fail
	wReplay := httptest.NewRecorder()
	reqReplay, _ := http.NewRequest("POST", "/account/reset-password", bytes.NewReader(resetPayload))
	reqReplay.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wReplay, reqReplay)
	assert.Equal(t, http.StatusBadRequest, wReplay.Code)
}
