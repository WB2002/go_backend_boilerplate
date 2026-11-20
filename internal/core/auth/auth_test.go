package auth_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/core/auth"
)

func generateTestRSAKeys() (string, string) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	return string(privPEM), string(pubPEM)
}

func handleMockRedisConn(c net.Conn, store map[string]string, mu *sync.Mutex) {
	defer c.Close()
	reader := bufio.NewReader(c)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.HasPrefix(line, "*") {
			continue
		}
		var numArgs int
		_, _ = fmt.Sscanf(line, "*%d", &numArgs)
		args := make([]string, 0, numArgs)
		for i := 0; i < numArgs; i++ {
			_, _ = reader.ReadString('\n')
			val, _ := reader.ReadString('\n')
			args = append(args, strings.TrimRight(val, "\r\n"))
		}
		if len(args) == 0 {
			continue
		}
		cmd := strings.ToUpper(args[0])
		switch cmd {
		case "HELLO":
			_, _ = c.Write([]byte("-ERR unknown command 'HELLO'\r\n"))
		case "PING":
			_, _ = c.Write([]byte("+PONG\r\n"))
		case "CLIENT":
			_, _ = c.Write([]byte("+OK\r\n"))
		case "SET":
			hasNX := false
			for _, a := range args[3:] {
				if strings.EqualFold(a, "NX") {
					hasNX = true
					break
				}
			}
			mu.Lock()
			reply := "+OK\r\n"
			if len(args) >= 3 {
				if _, exists := store[args[1]]; hasNX && exists {
					reply = "$-1\r\n"
				} else {
					store[args[1]] = args[2]
				}
			}
			mu.Unlock()
			_, _ = c.Write([]byte(reply))
		case "EXISTS":
			mu.Lock()
			count := 0
			for _, k := range args[1:] {
				if _, ok := store[k]; ok {
					count++
				}
			}
			mu.Unlock()
			_, _ = c.Write([]byte(fmt.Sprintf(":%d\r\n", count)))
		default:
			_, _ = c.Write([]byte("+OK\r\n"))
		}
	}
}

func startMockRedis(t *testing.T) (*goredis.Client, func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("skipping redis-dependent test: socket listen not permitted in environment")
		return nil, func() {}
	}

	store := make(map[string]string)
	var mu sync.Mutex
	closed := make(chan struct{})

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-closed:
					return
				default:
					return
				}
			}
			go handleMockRedisConn(conn, store, &mu)
		}
	}()

	client := goredis.NewClient(&goredis.Options{
		Addr:     ln.Addr().String(),
		Protocol: 2,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		_ = ln.Close()
		close(closed)
		t.Skipf("skipping redis-dependent test: local redis mock dial failed: %v", err)
		return nil, func() {}
	}

	cleanup := func() {
		close(closed)
		_ = client.Close()
		_ = ln.Close()
	}

	return client, cleanup
}

func TestArgon2idBulkheadConcurrencyLimit(t *testing.T) {
	// Bulkhead allows max 2 concurrent hashes, queue depth 4
	hasher := auth.NewArgon2idHasher(2, 4, 100*time.Millisecond)

	var wg sync.WaitGroup
	errCount := 0
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := hasher.HashPassword("SuperSecretPassword123!")
			if err != nil {
				mu.Lock()
				errCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	// Under high burst, queue capacity exceeded triggers rejection error
	assert.Greater(t, errCount, 0, "Expected bulkhead to reject excess password hashing requests")
}

func TestArgon2idHashAndVerify(t *testing.T) {
	// Use lighter parameters for test speed
	params := auth.Argon2Params{
		Memory:      1024,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
	hasher := auth.NewArgon2idHasherWithParams(params, 2, 4, 1*time.Second)

	password := "CorrectHorseBatteryStaple!"
	hash, err := hasher.HashPassword(password)
	require.NoError(t, err)
	assert.NotEmpty(t, hash)
	assert.True(t, strings.HasPrefix(hash, "$argon2id$v=19$"))

	// Correct password verification
	valid, err := hasher.VerifyPassword(password, hash)
	require.NoError(t, err)
	assert.True(t, valid, "Expected password verification to succeed with correct password")

	// Incorrect password verification
	invalid, err := hasher.VerifyPassword("WrongPassword123!", hash)
	require.NoError(t, err)
	assert.False(t, invalid, "Expected password verification to fail with wrong password")
}

func TestArgon2idVerifyInvalidHash(t *testing.T) {
	hasher := auth.NewArgon2idHasher(2, 4, 1*time.Second)

	// Corrupt hash strings
	_, err := hasher.VerifyPassword("password", "not-a-valid-hash")
	assert.ErrorIs(t, err, auth.ErrInvalidHash)

	_, err = hasher.VerifyPassword("password", "$bcrypt$v=19$m=65536,t=3,p=2$salt$key")
	assert.ErrorIs(t, err, auth.ErrInvalidHash)

	_, err = hasher.VerifyPassword("password", "$argon2id$v=99$m=65536,t=3,p=2$salt$key")
	assert.ErrorIs(t, err, auth.ErrInvalidHash)
}

func TestArgon2idBulkheadTimeout(t *testing.T) {
	// Concurrency 1, queue depth 2, wait timeout 50ms
	hasher := auth.NewArgon2idHasher(1, 2, 50*time.Millisecond)

	// Block the single slot with a long operation
	started := make(chan struct{})
	release := make(chan struct{})

	go func() {
		// Acquire slot
		_, _ = hasher.HashPassword("slow-hash")
	}()

	// Wait briefly for slot to be occupied
	time.Sleep(10 * time.Millisecond)

	// Now try hashing another password; should wait up to 50ms then timeout
	start := time.Now()
	_, err := hasher.HashPassword("timeout-hash")
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.True(t, elapsed >= 45*time.Millisecond, "Expected call to wait near maxWait duration")

	close(started)
	close(release)
}

func TestRS256TokenGenerationAndValidation(t *testing.T) {
	privPEM, pubPEM := generateTestRSAKeys()

	mgr, err := auth.NewJWTTokenManager(privPEM, pubPEM, 15*time.Minute, 7*24*time.Hour, nil)
	require.NoError(t, err)
	require.NotNil(t, mgr)

	userID := "usr_123456789"
	roles := []string{"admin", "editor"}

	tokenPair, err := mgr.GenerateTokenPair(userID, roles)
	require.NoError(t, err)
	require.NotNil(t, tokenPair)
	assert.NotEmpty(t, tokenPair.AccessToken)
	assert.NotEmpty(t, tokenPair.RefreshToken)
	assert.NotEmpty(t, tokenPair.AccessJTI)
	assert.NotEmpty(t, tokenPair.RefreshJTI)
	assert.NotEqual(t, tokenPair.AccessJTI, tokenPair.RefreshJTI)

	// Validate access token
	ctx := context.Background()
	claims, err := mgr.ValidateAccessToken(ctx, tokenPair.AccessToken)
	require.NoError(t, err)
	require.NotNil(t, claims)
	assert.Equal(t, userID, claims.UserID)
	assert.Equal(t, userID, claims.Subject)
	assert.Equal(t, roles, claims.Roles)
	assert.Equal(t, auth.TokenTypeAccess, claims.TokenType)
	assert.Equal(t, tokenPair.AccessJTI, claims.ID)

	// Refresh token cannot be validated as access token
	_, err = mgr.ValidateAccessToken(ctx, tokenPair.RefreshToken)
	assert.ErrorIs(t, err, auth.ErrInvalidTokenType)

	// Validate refresh token
	refreshClaims, err := mgr.ValidateRefreshToken(ctx, tokenPair.RefreshToken)
	require.NoError(t, err)
	require.NotNil(t, refreshClaims)
	assert.Equal(t, userID, refreshClaims.UserID)
	assert.Equal(t, auth.TokenTypeRefresh, refreshClaims.TokenType)
}

func TestRS256TokenRevocationWithRedis(t *testing.T) {
	client, cleanup := startMockRedis(t)
	defer cleanup()

	privPEM, pubPEM := generateTestRSAKeys()
	mgr, err := auth.NewJWTTokenManager(privPEM, pubPEM, 15*time.Minute, 7*24*time.Hour, client)
	require.NoError(t, err)

	ctx := context.Background()
	tokenPair, err := mgr.GenerateTokenPair("user_revocation_test", []string{"user"})
	require.NoError(t, err)

	// Token is initially valid
	claims, err := mgr.ValidateAccessToken(ctx, tokenPair.AccessToken)
	require.NoError(t, err)
	require.NotNil(t, claims)

	// Revoke token via JTI
	err = mgr.RevokeToken(ctx, tokenPair.AccessJTI, 15*time.Minute)
	require.NoError(t, err)

	// Validating revoked token returns ErrTokenRevoked
	_, err = mgr.ValidateAccessToken(ctx, tokenPair.AccessToken)
	assert.ErrorIs(t, err, auth.ErrTokenRevoked)
}

func TestRS256TokenInvalidSignatureAndExpired(t *testing.T) {
	privPEM1, pubPEM1 := generateTestRSAKeys()
	_, pubPEM2 := generateTestRSAKeys()

	// Manager with pubPEM2 won't be able to verify tokens signed with privPEM1
	mgr1, err := auth.NewJWTTokenManager(privPEM1, pubPEM1, 15*time.Minute, 7*24*time.Hour, nil)
	require.NoError(t, err)

	mgr2, err := auth.NewJWTTokenManager(privPEM1, pubPEM2, 15*time.Minute, 7*24*time.Hour, nil)
	require.NoError(t, err)

	pair, err := mgr1.GenerateTokenPair("user_1", []string{"user"})
	require.NoError(t, err)

	ctx := context.Background()
	_, err = mgr2.ValidateAccessToken(ctx, pair.AccessToken)
	assert.Error(t, err, "Expected signature mismatch error")

	// Expired token test
	shortMgr, err := auth.NewJWTTokenManager(privPEM1, pubPEM1, -1*time.Second, 1*time.Hour, nil)
	require.NoError(t, err)
	expiredPair, err := shortMgr.GenerateTokenPair("user_2", []string{"user"})
	require.NoError(t, err)

	_, err = shortMgr.ValidateAccessToken(ctx, expiredPair.AccessToken)
	assert.ErrorIs(t, err, auth.ErrTokenExpired)
}

func TestArgon2idContextCancellation(t *testing.T) {
	hasher := auth.NewArgon2idHasher(1, 2, 1*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := hasher.HashPasswordContext(ctx, "password")
	assert.ErrorIs(t, err, context.Canceled)

	_, err = hasher.VerifyPasswordContext(ctx, "password", "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$a2V5a2V5a2V5")
	assert.ErrorIs(t, err, context.Canceled)
}

type mockFailingTokenManager struct {
	auth.TokenManager
	revokeErr error
}

func (m *mockFailingTokenManager) ValidateRefreshToken(ctx context.Context, tokenStr string) (*auth.Claims, error) {
	return &auth.Claims{
		UserID:    "user_replay",
		Roles:     []string{"user"},
		TokenType: auth.TokenTypeRefresh,
	}, nil
}

func (m *mockFailingTokenManager) RevokeToken(ctx context.Context, jti string, ttl time.Duration) error {
	return m.revokeErr
}

func (m *mockFailingTokenManager) GenerateTokenPair(userID string, roles []string) (*auth.TokenPair, error) {
	return &auth.TokenPair{
		AccessToken: "should_not_issue",
	}, nil
}

func TestAuthServiceRefreshTokenReplayGuard(t *testing.T) {
	hasher := auth.NewArgon2idHasher(1, 2, 1*time.Second)
	failingMgr := &mockFailingTokenManager{
		revokeErr: fmt.Errorf("redis network partition"),
	}

	service := auth.NewService(hasher, failingMgr)
	pair, err := service.RefreshToken(context.Background(), "some_refresh_token")
	assert.Error(t, err)
	assert.Nil(t, pair, "Must not issue new tokens if revocation of old token fails")
	assert.Contains(t, err.Error(), "failed to revoke old refresh token")
}

func TestAuthServiceIntegration(t *testing.T) {
	privPEM, pubPEM := generateTestRSAKeys()
	tokenMgr, err := auth.NewJWTTokenManager(privPEM, pubPEM, 15*time.Minute, 7*24*time.Hour, nil)
	require.NoError(t, err)

	params := auth.Argon2Params{
		Memory:      1024,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
	hasher := auth.NewArgon2idHasherWithParams(params, 2, 4, 1*time.Second)

	service := auth.NewService(hasher, tokenMgr)
	require.NotNil(t, service)

	// Test password hashing and verification through service
	hash, err := service.HashPassword("SecurePassword!123")
	require.NoError(t, err)
	valid, err := service.VerifyPassword("SecurePassword!123", hash)
	require.NoError(t, err)
	assert.True(t, valid)

	// Test token pair generation and validation through service
	pair, err := service.GenerateTokenPair("svc_user_1", []string{"admin"})
	require.NoError(t, err)
	claims, err := service.ValidateAccessToken(context.Background(), pair.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, "svc_user_1", claims.UserID)

	// Test refresh token flow
	newPair, err := service.RefreshToken(context.Background(), pair.RefreshToken)
	require.NoError(t, err)
	require.NotNil(t, newPair)
	assert.NotEmpty(t, newPair.AccessToken)
	assert.NotEqual(t, pair.AccessToken, newPair.AccessToken)
}
