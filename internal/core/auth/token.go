package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

var (
	// ErrInvalidToken indicates the token format, signature, or claims are invalid.
	ErrInvalidToken = errors.New("invalid token")
	// ErrTokenRevoked indicates the token JTI was found in the revocation blacklist.
	ErrTokenRevoked = errors.New("token has been revoked")
	// ErrTokenExpired indicates the token has passed its expiration time.
	ErrTokenExpired = errors.New("token has expired")
	// ErrInvalidTokenType indicates an unexpected token type (e.g. refresh token provided where access token expected).
	ErrInvalidTokenType = errors.New("invalid token type")
	// ErrRevocationCheckFailed indicates an error communicating with the revocation store (e.g. Redis).
	ErrRevocationCheckFailed = errors.New("failed to check token revocation")
)

const (
	// TokenTypeAccess marks an access token.
	TokenTypeAccess = "access"
	// TokenTypeRefresh marks a refresh token.
	TokenTypeRefresh = "refresh"
	// RevokedJTIPrefix is the Redis key prefix for revoked token JTIs.
	RevokedJTIPrefix = "revoked_jti:"
	// DefaultIssuer is the standard JWT issuer name.
	DefaultIssuer = "go_backend_boilerplate"
)

func init() {
	// Preserve millisecond precision on IssuedAt/ExpiresAt so password_changed_at
	// comparisons distinguish tokens minted before vs. after a change within the same second.
	jwt.TimePrecision = time.Millisecond
}

// Claims represents strongly-typed JWT claims with user attributes and UUID JTI.
type Claims struct {
	UserID    string   `json:"user_id"`
	Roles     []string `json:"roles"`
	TokenType string   `json:"token_type"`
	jwt.RegisteredClaims
}

// TokenPair bundles newly generated access and refresh tokens with metadata.
type TokenPair struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	AccessJTI    string    `json:"access_jti"`
	RefreshJTI   string    `json:"refresh_jti"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// TokenManager specifies the interface for asymmetric JWT operations and revocation.
type TokenManager interface {
	GenerateTokenPair(userID string, roles []string) (*TokenPair, error)
	ValidateAccessToken(ctx context.Context, tokenStr string) (*Claims, error)
	ValidateRefreshToken(ctx context.Context, tokenStr string) (*Claims, error)
	RevokeToken(ctx context.Context, jti string, ttl time.Duration) error
	RevokeTokenOnce(ctx context.Context, tokenID string, ttl time.Duration) (bool, error)
}

// JWTTokenManager implements RS256 token issuance and verification with Redis revocation checking.
type JWTTokenManager struct {
	privateKey   *rsa.PrivateKey
	publicKey    *rsa.PublicKey
	accessTTL    time.Duration
	refreshTTL   time.Duration
	redisClient  *goredis.Client
	inMemRevoked sync.Map
}

// NewRS256TokenManager creates a new RS256 JWTTokenManager from PEM strings (alias for NewJWTTokenManager).
func NewRS256TokenManager(privateKeyPEM, publicKeyPEM string, accessTTL, refreshTTL time.Duration, redisClient *goredis.Client) (*JWTTokenManager, error) {
	return NewJWTTokenManager(privateKeyPEM, publicKeyPEM, accessTTL, refreshTTL, redisClient)
}

// NewJWTTokenManager creates a new RS256 JWTTokenManager from PEM strings.
func NewJWTTokenManager(privateKeyPEM, publicKeyPEM string, accessTTL, refreshTTL time.Duration, redisClient *goredis.Client) (*JWTTokenManager, error) {
	if privateKeyPEM == "" {
		return nil, errors.New("private key PEM cannot be empty")
	}
	if publicKeyPEM == "" {
		return nil, errors.New("public key PEM cannot be empty")
	}

	privKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("failed to parse RSA private key: %w", err)
	}

	pubKey, err := jwt.ParseRSAPublicKeyFromPEM([]byte(publicKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("failed to parse RSA public key: %w", err)
	}

	if accessTTL == 0 {
		accessTTL = 15 * time.Minute
	}
	if refreshTTL == 0 {
		refreshTTL = 7 * 24 * time.Hour
	}

	return &JWTTokenManager{
		privateKey:  privKey,
		publicKey:   pubKey,
		accessTTL:   accessTTL,
		refreshTTL:  refreshTTL,
		redisClient: redisClient,
	}, nil
}

// GenerateTokenPair signs and returns an RS256 access and refresh token pair.
func (m *JWTTokenManager) GenerateTokenPair(userID string, roles []string) (*TokenPair, error) {
	now := time.Now().UTC()

	accessJTI := uuid.NewString()
	accessExpiry := now.Add(m.accessTTL)
	accessClaims := Claims{
		UserID:    userID,
		Roles:     roles,
		TokenType: TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    DefaultIssuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{"api"},
			ExpiresAt: jwt.NewNumericDate(accessExpiry),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        accessJTI,
		},
	}

	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodRS256, accessClaims).SignedString(m.privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	refreshJTI := uuid.NewString()
	refreshExpiry := now.Add(m.refreshTTL)
	refreshClaims := Claims{
		UserID:    userID,
		Roles:     roles,
		TokenType: TokenTypeRefresh,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    DefaultIssuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{"api"},
			ExpiresAt: jwt.NewNumericDate(refreshExpiry),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        refreshJTI,
		},
	}

	refreshToken, err := jwt.NewWithClaims(jwt.SigningMethodRS256, refreshClaims).SignedString(m.privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		AccessJTI:    accessJTI,
		RefreshJTI:   refreshJTI,
		ExpiresAt:    accessExpiry,
	}, nil
}

// ValidateAccessToken verifies the RS256 signature, expiration, issuer, audience, and checks Redis revocation.
func (m *JWTTokenManager) ValidateAccessToken(ctx context.Context, tokenStr string) (*Claims, error) {
	return m.validateToken(ctx, tokenStr, TokenTypeAccess)
}

// ValidateRefreshToken verifies RS256 signature, issuer, audience, and validates refresh token claims.
func (m *JWTTokenManager) ValidateRefreshToken(ctx context.Context, tokenStr string) (*Claims, error) {
	return m.validateToken(ctx, tokenStr, TokenTypeRefresh)
}

func (m *JWTTokenManager) validateToken(ctx context.Context, tokenStr, expectedType string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return m.publicKey, nil
	},
		jwt.WithIssuer(DefaultIssuer),
		jwt.WithAudience("api"),
		jwt.WithValidMethods([]string{"RS256"}),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	if claims.TokenType != expectedType {
		return nil, ErrInvalidTokenType
	}

	if claims.ID == "" {
		return nil, ErrInvalidToken
	}

	// Check Redis revocation blacklist or in-memory fallback
	if m.redisClient != nil {
		revocationKey := RevokedJTIPrefix + claims.ID
		exists, err := m.redisClient.Exists(ctx, revocationKey).Result()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRevocationCheckFailed, err)
		}
		if exists > 0 {
			return nil, ErrTokenRevoked
		}
	} else {
		if expVal, ok := m.inMemRevoked.Load(claims.ID); ok {
			if expTime, ok := expVal.(time.Time); ok && time.Now().Before(expTime) {
				return nil, ErrTokenRevoked
			}
		}
	}

	return claims, nil
}

// RevokeToken marks a JTI as revoked in Redis with a TTL (or in-memory when Redis is unconfigured).
func (m *JWTTokenManager) RevokeToken(ctx context.Context, jti string, ttl time.Duration) error {
	if jti == "" {
		return errors.New("jti cannot be empty")
	}
	if ttl <= 0 {
		ttl = m.refreshTTL
	}

	if m.redisClient == nil {
		m.inMemRevoked.Store(jti, time.Now().Add(ttl))
		return nil
	}

	key := RevokedJTIPrefix + jti
	if err := m.redisClient.Set(ctx, key, "1", ttl).Err(); err != nil {
		return fmt.Errorf("failed to record token revocation in redis: %w", err)
	}
	return nil
}

// RevokeTokenOnce atomically marks a JTI as revoked if it has not already been revoked.
// Returns true if this call newly revoked the token, or false if it was already revoked.
func (m *JWTTokenManager) RevokeTokenOnce(ctx context.Context, tokenID string, ttl time.Duration) (bool, error) {
	if tokenID == "" {
		return false, errors.New("jti cannot be empty")
	}
	if ttl <= 0 {
		ttl = m.refreshTTL
	}

	if m.redisClient == nil {
		if _, loaded := m.inMemRevoked.LoadOrStore(tokenID, time.Now().Add(ttl)); loaded {
			return false, nil
		}
		return true, nil
	}

	key := RevokedJTIPrefix + tokenID
	revoked, err := m.redisClient.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("failed to record atomic token revocation in redis: %w", err)
	}
	return revoked, nil
}
