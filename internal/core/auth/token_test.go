package auth_test

import (
	"context"
	"sync"
	"sync/atomic"
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

func TestRevokeTokenOnceAtomicity(t *testing.T) {
	privPEM, pubPEM := generateTestRSAKeys()
	ctx := context.Background()

	t.Run("in-memory atomic revocation", func(t *testing.T) {
		mgr, err := auth.NewJWTTokenManager(privPEM, pubPEM, 15*time.Minute, 7*24*time.Hour, nil)
		require.NoError(t, err)

		pair, err := mgr.GenerateTokenPair("user-inmem", []string{"user"})
		require.NoError(t, err)

		const workers = 16
		var won atomic.Int32
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ok, revErr := mgr.RevokeTokenOnce(ctx, pair.RefreshJTI, time.Hour)
				assert.NoError(t, revErr)
				if ok {
					won.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		assert.Equal(t, int32(1), won.Load())

		_, err = mgr.ValidateRefreshToken(ctx, pair.RefreshToken)
		assert.ErrorIs(t, err, auth.ErrTokenRevoked)
	})

	t.Run("redis atomic revocation", func(t *testing.T) {
		client, cleanup := startMockRedis(t)
		defer cleanup()

		mgr, err := auth.NewJWTTokenManager(privPEM, pubPEM, 15*time.Minute, 7*24*time.Hour, client)
		require.NoError(t, err)

		pair, err := mgr.GenerateTokenPair("user-redis", []string{"user"})
		require.NoError(t, err)

		const workers = 16
		var won atomic.Int32
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ok, revErr := mgr.RevokeTokenOnce(ctx, pair.RefreshJTI, time.Hour)
				assert.NoError(t, revErr)
				if ok {
					won.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		assert.Equal(t, int32(1), won.Load())

		_, err = mgr.ValidateRefreshToken(ctx, pair.RefreshToken)
		assert.ErrorIs(t, err, auth.ErrTokenRevoked)
	})
}
