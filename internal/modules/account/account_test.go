package account_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	var loginResp map[string]interface{}
	err := json.Unmarshal(wLogin.Body.Bytes(), &loginResp)
	require.NoError(t, err)
	assert.NotEmpty(t, loginResp["accessToken"])
	accessToken, ok := loginResp["accessToken"].(string)
	require.True(t, ok)

	// Profile /account/me
	wMe := httptest.NewRecorder()
	reqMe, _ := http.NewRequest(http.MethodGet, "/account/me", nil)
	reqMe.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(wMe, reqMe)

	assert.Equal(t, http.StatusOK, wMe.Code)
	var meResp map[string]interface{}
	err = json.Unmarshal(wMe.Body.Bytes(), &meResp)
	require.NoError(t, err)
	assert.Equal(t, "test@example.com", meResp["email"])

	// Protected change password route with Bearer token
	changePayload := map[string]string{
		"oldPassword": "Password123!@#",
		"newPassword": "NewPassword123!@#",
	}
	changeBody, _ := json.Marshal(changePayload)
	wChange := httptest.NewRecorder()
	reqChange, _ := http.NewRequest(http.MethodPost, "/account/change-password", bytes.NewReader(changeBody))
	reqChange.Header.Set("Content-Type", "application/json")
	reqChange.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(wChange, reqChange)

	assert.Equal(t, http.StatusOK, wChange.Code)

	// Login with old password should fail
	wLoginOld := httptest.NewRecorder()
	reqLoginOld, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(body))
	reqLoginOld.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLoginOld, reqLoginOld)
	assert.Equal(t, http.StatusUnauthorized, wLoginOld.Code)

	// Login with new password should succeed
	newLoginPayload := map[string]string{
		"email":    "test@example.com",
		"password": "NewPassword123!@#",
	}
	newLoginBody, _ := json.Marshal(newLoginPayload)
	wLoginNew := httptest.NewRecorder()
	reqLoginNew, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(newLoginBody))
	reqLoginNew.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLoginNew, reqLoginNew)
	assert.Equal(t, http.StatusOK, wLoginNew.Code)
}

func TestAccountDuplicateRegistration(t *testing.T) {
	router := account.SetupTestRouter(t)

	regPayload := map[string]string{
		"email":    "duplicate@example.com",
		"password": "Password123!@#",
	}
	body, _ := json.Marshal(regPayload)

	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusCreated, w1.Code)

	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusConflict, w2.Code)
}

func TestAccountInvalidPasswordLogin(t *testing.T) {
	router := account.SetupTestRouter(t)

	regPayload := map[string]string{
		"email":    "invalidpass@example.com",
		"password": "Password123!@#",
	}
	body, _ := json.Marshal(regPayload)

	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(body))
	reqReg.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReg, reqReg)
	assert.Equal(t, http.StatusCreated, wReg.Code)

	badLoginPayload := map[string]string{
		"email":    "invalidpass@example.com",
		"password": "WrongPassword999!",
	}
	badBody, _ := json.Marshal(badLoginPayload)

	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(badBody))
	reqLogin.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLogin, reqLogin)
	assert.Equal(t, http.StatusUnauthorized, wLogin.Code)
}

func TestAccountUnauthorizedAccess(t *testing.T) {
	router := account.SetupTestRouter(t)

	// Me without token
	wMe := httptest.NewRecorder()
	reqMe, _ := http.NewRequest(http.MethodGet, "/account/me", nil)
	router.ServeHTTP(wMe, reqMe)
	assert.Equal(t, http.StatusUnauthorized, wMe.Code)

	// Change password without token
	changePayload := map[string]string{
		"oldPassword": "Password123!@#",
		"newPassword": "NewPassword123!@#",
	}
	changeBody, _ := json.Marshal(changePayload)
	wChange := httptest.NewRecorder()
	reqChange, _ := http.NewRequest(http.MethodPost, "/account/change-password", bytes.NewReader(changeBody))
	reqChange.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wChange, reqChange)
	assert.Equal(t, http.StatusUnauthorized, wChange.Code)
}

func TestAccountInvalidOldPasswordChange(t *testing.T) {
	router := account.SetupTestRouter(t)

	regPayload := map[string]string{
		"email":    "changepass@example.com",
		"password": "Password123!@#",
	}
	body, _ := json.Marshal(regPayload)

	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(body))
	reqReg.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReg, reqReg)
	assert.Equal(t, http.StatusCreated, wReg.Code)

	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(body))
	reqLogin.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLogin, reqLogin)
	assert.Equal(t, http.StatusOK, wLogin.Code)

	var loginResp map[string]interface{}
	_ = json.Unmarshal(wLogin.Body.Bytes(), &loginResp)
	accessToken := loginResp["accessToken"].(string)

	badChangePayload := map[string]string{
		"oldPassword": "WrongOldPassword123!",
		"newPassword": "NewPassword123!@#",
	}
	badChangeBody, _ := json.Marshal(badChangePayload)
	wChange := httptest.NewRecorder()
	reqChange, _ := http.NewRequest(http.MethodPost, "/account/change-password", bytes.NewReader(badChangeBody))
	reqChange.Header.Set("Content-Type", "application/json")
	reqChange.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(wChange, reqChange)

	assert.Equal(t, http.StatusBadRequest, wChange.Code)
}

func TestAccountRefreshFlow(t *testing.T) {
	router := account.SetupTestRouter(t)

	regPayload := map[string]string{
		"email":    "refresh@example.com",
		"password": "Password123!@#",
	}
	body, _ := json.Marshal(regPayload)

	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(body))
	reqReg.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReg, reqReg)
	assert.Equal(t, http.StatusCreated, wReg.Code)

	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(body))
	reqLogin.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLogin, reqLogin)
	assert.Equal(t, http.StatusOK, wLogin.Code)

	var loginResp map[string]interface{}
	_ = json.Unmarshal(wLogin.Body.Bytes(), &loginResp)
	refreshToken := loginResp["refreshToken"].(string)

	refreshPayload := map[string]string{
		"refreshToken": refreshToken,
	}
	refBody, _ := json.Marshal(refreshPayload)
	wRef := httptest.NewRecorder()
	reqRef, _ := http.NewRequest(http.MethodPost, "/account/refresh", bytes.NewReader(refBody))
	reqRef.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wRef, reqRef)

	assert.Equal(t, http.StatusOK, wRef.Code)
	var refResp map[string]interface{}
	err := json.Unmarshal(wRef.Body.Bytes(), &refResp)
	require.NoError(t, err)
	assert.NotEmpty(t, refResp["accessToken"])
	assert.NotEmpty(t, refResp["refreshToken"])
}

func TestAccountRefreshTokenReplayRejection(t *testing.T) {
	router := account.SetupTestRouter(t)

	regPayload := map[string]string{
		"email":    "replay@example.com",
		"password": "Password123!@#",
	}
	body, _ := json.Marshal(regPayload)

	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(body))
	reqReg.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReg, reqReg)
	assert.Equal(t, http.StatusCreated, wReg.Code)

	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(body))
	reqLogin.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLogin, reqLogin)
	assert.Equal(t, http.StatusOK, wLogin.Code)

	var loginResp map[string]interface{}
	err := json.Unmarshal(wLogin.Body.Bytes(), &loginResp)
	require.NoError(t, err)
	refreshToken := loginResp["refreshToken"].(string)

	refreshPayload := map[string]string{
		"refreshToken": refreshToken,
	}
	refBody, _ := json.Marshal(refreshPayload)

	// First refresh must succeed
	wRef1 := httptest.NewRecorder()
	reqRef1, _ := http.NewRequest(http.MethodPost, "/account/refresh", bytes.NewReader(refBody))
	reqRef1.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wRef1, reqRef1)
	assert.Equal(t, http.StatusOK, wRef1.Code)

	// Replay of the same refresh token must be rejected with 401
	wRef2 := httptest.NewRecorder()
	reqRef2, _ := http.NewRequest(http.MethodPost, "/account/refresh", bytes.NewReader(refBody))
	reqRef2.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wRef2, reqRef2)
	assert.Equal(t, http.StatusUnauthorized, wRef2.Code)
}

func TestAccountPasswordLengthBoundaries(t *testing.T) {
	router := account.SetupTestRouter(t)

	// 1. Password shorter than 8 characters (7 chars) -> 400 Bad Request
	shortPayload := map[string]string{
		"email":    "short@example.com",
		"password": "Pass1!a",
	}
	shortBody, _ := json.Marshal(shortPayload)
	wShort := httptest.NewRecorder()
	reqShort, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(shortBody))
	reqShort.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wShort, reqShort)
	assert.Equal(t, http.StatusBadRequest, wShort.Code)

	// 2. Password longer than 128 characters (129 chars) -> 400 Bad Request
	longPass := "Password123!" + strings.Repeat("a", 117)
	assert.Equal(t, 129, len(longPass))
	longPayload := map[string]string{
		"email":    "toolong@example.com",
		"password": longPass,
	}
	longBody, _ := json.Marshal(longPayload)
	wLong := httptest.NewRecorder()
	reqLong, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(longBody))
	reqLong.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLong, reqLong)
	assert.Equal(t, http.StatusBadRequest, wLong.Code)

	// 3. Exactly 8 characters (lower boundary) -> 201 Created
	exact8Pass := "Pass123!"
	assert.Equal(t, 8, len(exact8Pass))
	exact8Payload := map[string]string{
		"email":    "exact8@example.com",
		"password": exact8Pass,
	}
	exact8Body, _ := json.Marshal(exact8Payload)
	wExact8 := httptest.NewRecorder()
	reqExact8, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(exact8Body))
	reqExact8.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wExact8, reqExact8)
	assert.Equal(t, http.StatusCreated, wExact8.Code)

	// 4. Exactly 128 characters (upper boundary) -> 201 Created
	exact128Pass := "Password123!" + strings.Repeat("b", 116)
	assert.Equal(t, 128, len(exact128Pass))
	exact128Payload := map[string]string{
		"email":    "exact128@example.com",
		"password": exact128Pass,
	}
	exact128Body, _ := json.Marshal(exact128Payload)
	wExact128 := httptest.NewRecorder()
	reqExact128, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(exact128Body))
	reqExact128.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wExact128, reqExact128)
	assert.Equal(t, http.StatusCreated, wExact128.Code)

	// 5. ChangePassword boundaries (<8 and >128)
	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(exact8Body))
	reqLogin.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLogin, reqLogin)
	assert.Equal(t, http.StatusOK, wLogin.Code)
	var loginResp map[string]interface{}
	_ = json.Unmarshal(wLogin.Body.Bytes(), &loginResp)
	token := loginResp["accessToken"].(string)

	// Change with <8 chars -> 400
	shortChange, _ := json.Marshal(map[string]string{"oldPassword": exact8Pass, "newPassword": "short"})
	wChangeShort := httptest.NewRecorder()
	reqChangeShort, _ := http.NewRequest(http.MethodPost, "/account/change-password", bytes.NewReader(shortChange))
	reqChangeShort.Header.Set("Content-Type", "application/json")
	reqChangeShort.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(wChangeShort, reqChangeShort)
	assert.Equal(t, http.StatusBadRequest, wChangeShort.Code)

	// Change with >128 chars -> 400
	longChange, _ := json.Marshal(map[string]string{"oldPassword": exact8Pass, "newPassword": longPass})
	wChangeLong := httptest.NewRecorder()
	reqChangeLong, _ := http.NewRequest(http.MethodPost, "/account/change-password", bytes.NewReader(longChange))
	reqChangeLong.Header.Set("Content-Type", "application/json")
	reqChangeLong.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(wChangeLong, reqChangeLong)
	assert.Equal(t, http.StatusBadRequest, wChangeLong.Code)
}

func TestAccountDeactivatedUserRejection(t *testing.T) {
	router, db := account.SetupTestRouterWithDB(t)

	regPayload := map[string]string{
		"email":    "deactivated@example.com",
		"password": "Password123!@#",
	}
	body, _ := json.Marshal(regPayload)

	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(body))
	reqReg.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReg, reqReg)
	assert.Equal(t, http.StatusCreated, wReg.Code)

	// Login while active to obtain a refresh token
	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(body))
	reqLogin.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLogin, reqLogin)
	assert.Equal(t, http.StatusOK, wLogin.Code)

	var loginResp map[string]interface{}
	err := json.Unmarshal(wLogin.Body.Bytes(), &loginResp)
	require.NoError(t, err)
	refreshToken := loginResp["refreshToken"].(string)

	// Deactivate user in database
	res := db.Model(&account.User{}).Where("email = ?", "deactivated@example.com").Update("status", "inactive")
	require.NoError(t, res.Error)
	assert.Equal(t, int64(1), res.RowsAffected)

	// 1. Attempt login with deactivated user -> must return 401
	wLoginDeactivated := httptest.NewRecorder()
	reqLoginDeactivated, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(body))
	reqLoginDeactivated.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLoginDeactivated, reqLoginDeactivated)
	assert.Equal(t, http.StatusUnauthorized, wLoginDeactivated.Code)

	// 2. Attempt refresh with valid token belonging to deactivated user -> must return 401
	refreshPayload := map[string]string{
		"refreshToken": refreshToken,
	}
	refBody, _ := json.Marshal(refreshPayload)
	wRefDeactivated := httptest.NewRecorder()
	reqRefDeactivated, _ := http.NewRequest(http.MethodPost, "/account/refresh", bytes.NewReader(refBody))
	reqRefDeactivated.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wRefDeactivated, reqRefDeactivated)
	assert.Equal(t, http.StatusUnauthorized, wRefDeactivated.Code)
}

func TestAccount_TokenInvalidatedAfterPasswordChange(t *testing.T) {
	router, db := account.SetupTestRouterWithDB(t)

	email := "tokinvalidate@example.com"
	password := "Password123!@#"
	newPassword := "NewPassword123!@#"

	regBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})
	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(regBody))
	reqReg.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReg, reqReg)
	require.Equal(t, http.StatusCreated, wReg.Code)

	// 1. Login to obtain pre-change tokens
	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(regBody))
	reqLogin.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLogin, reqLogin)
	require.Equal(t, http.StatusOK, wLogin.Code)

	var loginResp map[string]interface{}
	require.NoError(t, json.Unmarshal(wLogin.Body.Bytes(), &loginResp))
	accessToken1 := loginResp["accessToken"].(string)
	refreshToken1 := loginResp["refreshToken"].(string)

	// 2. Change password -> sets password_changed_at
	changeBody, _ := json.Marshal(map[string]string{
		"oldPassword": password,
		"newPassword": newPassword,
	})
	wChange := httptest.NewRecorder()
	reqChange, _ := http.NewRequest(http.MethodPost, "/account/change-password", bytes.NewReader(changeBody))
	reqChange.Header.Set("Content-Type", "application/json")
	reqChange.Header.Set("Authorization", "Bearer "+accessToken1)
	router.ServeHTTP(wChange, reqChange)
	require.Equal(t, http.StatusOK, wChange.Code)

	// 3. Pre-change refresh token must be rejected with 401
	refBody1, _ := json.Marshal(map[string]string{"refreshToken": refreshToken1})
	wRef1 := httptest.NewRecorder()
	reqRef1, _ := http.NewRequest(http.MethodPost, "/account/refresh", bytes.NewReader(refBody1))
	reqRef1.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wRef1, reqRef1)
	assert.Equal(t, http.StatusUnauthorized, wRef1.Code)

	// 4. Login with new password -> post-change refresh token must succeed
	newLoginBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": newPassword,
	})
	wLogin2 := httptest.NewRecorder()
	reqLogin2, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(newLoginBody))
	reqLogin2.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLogin2, reqLogin2)
	require.Equal(t, http.StatusOK, wLogin2.Code)

	var loginResp2 map[string]interface{}
	require.NoError(t, json.Unmarshal(wLogin2.Body.Bytes(), &loginResp2))
	refreshToken2 := loginResp2["refreshToken"].(string)

	refBody2, _ := json.Marshal(map[string]string{"refreshToken": refreshToken2})
	wRef2 := httptest.NewRecorder()
	reqRef2, _ := http.NewRequest(http.MethodPost, "/account/refresh", bytes.NewReader(refBody2))
	reqRef2.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wRef2, reqRef2)
	assert.Equal(t, http.StatusOK, wRef2.Code)

	// 5. Password reset must also invalidate pre-reset refresh tokens
	var loginResp3 map[string]interface{}
	require.NoError(t, json.Unmarshal(wRef2.Body.Bytes(), &loginResp3))
	refreshToken3 := loginResp3["refreshToken"].(string)

	var user account.User
	require.NoError(t, db.Where("email = ?", email).First(&user).Error)

	rawResetToken := uuid.NewString()
	sum := sha256.Sum256([]byte(rawResetToken))
	require.NoError(t, db.Create(&account.PasswordReset{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		TokenHash: hex.EncodeToString(sum[:]),
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}).Error)

	resetBody, _ := json.Marshal(map[string]string{
		"token":       rawResetToken,
		"newPassword": "ResetPassword123!@#",
	})
	wReset := httptest.NewRecorder()
	reqReset, _ := http.NewRequest(http.MethodPost, "/account/reset-password", bytes.NewReader(resetBody))
	reqReset.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReset, reqReset)
	require.Equal(t, http.StatusOK, wReset.Code)

	refBody3, _ := json.Marshal(map[string]string{"refreshToken": refreshToken3})
	wRef3 := httptest.NewRecorder()
	reqRef3, _ := http.NewRequest(http.MethodPost, "/account/refresh", bytes.NewReader(refBody3))
	reqRef3.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wRef3, reqRef3)
	assert.Equal(t, http.StatusUnauthorized, wRef3.Code)
}

func TestAccount_ConcurrentPasswordResetAtomicity(t *testing.T) {
	router, db := account.SetupTestRouterWithDB(t)

	email := "concurrent-reset@example.com"
	regBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": "Password123!@#",
	})
	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(regBody))
	reqReg.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReg, reqReg)
	require.Equal(t, http.StatusCreated, wReg.Code)

	var user account.User
	require.NoError(t, db.Where("email = ?", email).First(&user).Error)

	rawToken := uuid.NewString() + "-" + uuid.NewString()
	sum := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(sum[:])

	require.NoError(t, db.Create(&account.PasswordReset{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}).Error)

	resetBody, _ := json.Marshal(map[string]string{
		"token":       rawToken,
		"newPassword": "ConcurrentNewPass123!",
	})

	start := make(chan struct{})
	var wg sync.WaitGroup
	codes := make([]int, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/account/reset-password", bytes.NewReader(resetBody))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			codes[idx] = w.Code
		}(i)
	}

	close(start)
	wg.Wait()

	successCount := 0
	badReqCount := 0
	for _, code := range codes {
		switch code {
		case http.StatusOK:
			successCount++
		case http.StatusBadRequest:
			badReqCount++
		}
	}
	assert.Equal(t, 1, successCount, "expected exactly 1 concurrent reset to succeed, got codes=%v", codes)
	assert.Equal(t, 1, badReqCount, "expected exactly 1 concurrent reset to fail with 400, got codes=%v", codes)

	var updatedReset account.PasswordReset
	require.NoError(t, db.Where("token_hash = ?", tokenHash).First(&updatedReset).Error)
	assert.NotNil(t, updatedReset.UsedAt)
}

func TestAccount_PasswordLengthExceeds128(t *testing.T) {
	router := account.SetupTestRouter(t)

	email := "maxpass@example.com"
	password := "Password123!@#"
	regBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})
	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(regBody))
	reqReg.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReg, reqReg)
	require.Equal(t, http.StatusCreated, wReg.Code)

	over128 := "Password123!" + strings.Repeat("a", 117)
	require.Len(t, over128, 129)

	// 1. Login with password > 128 characters -> 400 Bad Request
	badLoginBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": over128,
	})
	wBadLogin := httptest.NewRecorder()
	reqBadLogin, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(badLoginBody))
	reqBadLogin.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wBadLogin, reqBadLogin)
	assert.Equal(t, http.StatusBadRequest, wBadLogin.Code)

	// 2. Valid login to obtain access token
	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(regBody))
	reqLogin.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLogin, reqLogin)
	require.Equal(t, http.StatusOK, wLogin.Code)

	var loginResp map[string]interface{}
	require.NoError(t, json.Unmarshal(wLogin.Body.Bytes(), &loginResp))
	accessToken := loginResp["accessToken"].(string)

	// 3. ChangePassword with oldPassword > 128 characters -> 400 Bad Request
	badChangeBody, _ := json.Marshal(map[string]string{
		"oldPassword": over128,
		"newPassword": "NewPassword123!@#",
	})
	wBadChange := httptest.NewRecorder()
	reqBadChange, _ := http.NewRequest(http.MethodPost, "/account/change-password", bytes.NewReader(badChangeBody))
	reqBadChange.Header.Set("Content-Type", "application/json")
	reqBadChange.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(wBadChange, reqBadChange)
	assert.Equal(t, http.StatusBadRequest, wBadChange.Code)
}

type failingRevokeTokenManager struct {
	auth.TokenManager
	claims    *auth.Claims
	revokeErr error
}

func (m *failingRevokeTokenManager) ValidateRefreshToken(ctx context.Context, tokenStr string) (*auth.Claims, error) {
	return m.claims, nil
}

func (m *failingRevokeTokenManager) RevokeToken(ctx context.Context, jti string, ttl time.Duration) error {
	return m.revokeErr
}

func (m *failingRevokeTokenManager) RevokeTokenOnce(ctx context.Context, tokenID string, ttl time.Duration) (bool, error) {
	if m.revokeErr != nil {
		return false, m.revokeErr
	}
	return true, nil
}

func (m *failingRevokeTokenManager) GenerateTokenPair(userID string, roles []string) (*auth.TokenPair, error) {
	return &auth.TokenPair{
		AccessToken:  "should-not-be-issued",
		RefreshToken: "should-not-be-issued",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
	}, nil
}

func TestAccount_RefreshFailsClosedOnRevokeError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := account.NewMockRepository()
	hasher := auth.NewArgon2idHasher(2, 4, 2*time.Second)

	user := &account.User{
		ID:           uuid.NewString(),
		Email:        "failclosed@example.com",
		PasswordHash: "hash",
		Status:       "active",
		Roles:        account.Roles{"user"},
	}
	require.NoError(t, repo.Create(context.Background(), user))

	t.Run("revoke error fails closed", func(t *testing.T) {
		tm := &failingRevokeTokenManager{
			claims: &auth.Claims{
				UserID:    user.ID,
				Roles:     []string{"user"},
				TokenType: auth.TokenTypeRefresh,
			},
			revokeErr: errors.New("redis unavailable"),
		}
		tm.claims.ID = "jti-123"

		r := gin.New()
		h := account.NewHandler(repo, hasher, tm, nil)
		h.RegisterRoutes(r, func(c *gin.Context) {})

		body, _ := json.Marshal(map[string]string{"refreshToken": "some-refresh-token"})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/account/refresh", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.NotContains(t, w.Body.String(), "should-not-be-issued")
	})

	t.Run("empty claims.ID rejected with 401", func(t *testing.T) {
		tm := &failingRevokeTokenManager{
			claims: &auth.Claims{
				UserID:    user.ID,
				Roles:     []string{"user"},
				TokenType: auth.TokenTypeRefresh,
			},
			revokeErr: nil,
		}

		r := gin.New()
		h := account.NewHandler(repo, hasher, tm, nil)
		h.RegisterRoutes(r, func(c *gin.Context) {})

		body, _ := json.Marshal(map[string]string{"refreshToken": "some-refresh-token"})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/account/refresh", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.NotContains(t, w.Body.String(), "should-not-be-issued")
	})
}

func TestAccount_ForgotPasswordRendersEmailBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := account.NewMockRepository()
	hasher := auth.NewArgon2idHasher(2, 4, 2*time.Second)
	mockMailer := email.NewMockMailer()

	user := &account.User{
		ID:           uuid.NewString(),
		Email:        "reset-render@example.com",
		PasswordHash: "hashed",
		Status:       "active",
	}
	require.NoError(t, repo.Create(context.Background(), user))

	h := account.NewHandlerWithMailer(repo, hasher, nil, nil, mockMailer)
	r := gin.New()
	h.RegisterRoutes(r, func(c *gin.Context) {})

	body, _ := json.Marshal(map[string]string{"email": "reset-render@example.com"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/account/forgot-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, mockMailer.SentMessages, 1)

	msg := mockMailer.SentMessages[0]
	token, ok := msg.Data["Token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, token)

	assert.NotEmpty(t, msg.BodyText)
	assert.NotEmpty(t, msg.BodyHTML)
	assert.Contains(t, msg.BodyText, token)
	assert.Contains(t, msg.BodyHTML, token)
}

func TestAccount_ConcurrentRefreshAtomicity(t *testing.T) {
	router := account.SetupTestRouter(t)

	regPayload := map[string]string{
		"email":    "concurrent-refresh@example.com",
		"password": "Password123!@#",
	}
	body, _ := json.Marshal(regPayload)

	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/account/register", bytes.NewReader(body))
	reqReg.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReg, reqReg)
	require.Equal(t, http.StatusCreated, wReg.Code)

	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodPost, "/account/login", bytes.NewReader(body))
	reqLogin.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wLogin, reqLogin)
	require.Equal(t, http.StatusOK, wLogin.Code)

	var loginResp map[string]interface{}
	require.NoError(t, json.Unmarshal(wLogin.Body.Bytes(), &loginResp))
	refreshToken, ok := loginResp["refreshToken"].(string)
	require.True(t, ok)
	require.NotEmpty(t, refreshToken)

	refBody, _ := json.Marshal(map[string]string{
		"refreshToken": refreshToken,
	})

	const concurrency = 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	codes := make([]int, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/account/refresh", bytes.NewReader(refBody))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			codes[idx] = w.Code
		}(i)
	}

	close(start)
	wg.Wait()

	successCount := 0
	unauthorizedCount := 0
	for _, code := range codes {
		switch code {
		case http.StatusOK:
			successCount++
		case http.StatusUnauthorized:
			unauthorizedCount++
		}
	}

	assert.Equal(t, 1, successCount, "expected exactly 1 concurrent refresh to succeed with 200, got codes=%v", codes)
	assert.Equal(t, concurrency-1, unauthorizedCount, "expected all other concurrent refreshes to fail with 401, got codes=%v", codes)
}
