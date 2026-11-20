package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/core/auth"
)

func signCustomToken(t *testing.T, privPEM string, claims auth.Claims) string {
	t.Helper()
	privKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privPEM))
	require.NoError(t, err)

	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(privKey)
	require.NoError(t, err)
	return signed
}

func TestTokenValidationRejectsEmptyJTI(t *testing.T) {
	privPEM, pubPEM := generateTestRSAKeys()
	mgr, err := auth.NewJWTTokenManager(privPEM, pubPEM, 15*time.Minute, 7*24*time.Hour, nil)
	require.NoError(t, err)

	now := time.Now().UTC()
	ctx := context.Background()

	// Access token with empty JTI must be rejected (SEC-AUD-007)
	accessNoJTI := signCustomToken(t, privPEM, auth.Claims{
		UserID:    "user-123",
		Roles:     []string{"user"},
		TokenType: auth.TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    auth.DefaultIssuer,
			Subject:   "user-123",
			Audience:  jwt.ClaimStrings{"api"},
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        "",
		},
	})

	_, err = mgr.ValidateAccessToken(ctx, accessNoJTI)
	assert.ErrorIs(t, err, auth.ErrInvalidToken)

	// Refresh token with empty JTI must be rejected (SEC-AUD-007)
	refreshNoJTI := signCustomToken(t, privPEM, auth.Claims{
		UserID:    "user-123",
		Roles:     []string{"user"},
		TokenType: auth.TokenTypeRefresh,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    auth.DefaultIssuer,
			Subject:   "user-123",
			Audience:  jwt.ClaimStrings{"api"},
			ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        "",
		},
	})

	_, err = mgr.ValidateRefreshToken(ctx, refreshNoJTI)
	assert.ErrorIs(t, err, auth.ErrInvalidToken)
}
