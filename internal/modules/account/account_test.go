package account_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
