package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Service defines high-level authentication operations orchestrating hashing and token lifecycles.
type Service interface {
	HashPassword(password string) (string, error)
	VerifyPassword(password, hash string) (bool, error)
	HashPasswordContext(ctx context.Context, password string) (string, error)
	VerifyPasswordContext(ctx context.Context, password, hash string) (bool, error)
	GenerateTokenPair(userID string, roles []string) (*TokenPair, error)
	ValidateAccessToken(ctx context.Context, tokenStr string) (*Claims, error)
	RefreshToken(ctx context.Context, refreshTokenStr string) (*TokenPair, error)
	RevokeToken(ctx context.Context, jti string, ttl time.Duration) error
}

// authService implements Service by orchestrating PasswordHasher and TokenManager.
type authService struct {
	hasher       PasswordHasher
	tokenManager TokenManager
}

// NewService constructs a Service instance.
func NewService(hasher PasswordHasher, tokenManager TokenManager) Service {
	return &authService{
		hasher:       hasher,
		tokenManager: tokenManager,
	}
}

// HashPassword hashes a plain-text password using the configured bounded hasher.
func (s *authService) HashPassword(password string) (string, error) {
	return s.HashPasswordContext(context.Background(), password)
}

// HashPasswordContext hashes a plain-text password with context cancellation support.
func (s *authService) HashPasswordContext(ctx context.Context, password string) (string, error) {
	if s.hasher == nil {
		return "", errors.New("hasher is not configured")
	}
	return s.hasher.HashPasswordContext(ctx, password)
}

// VerifyPassword verifies a password against a hash using the configured bounded hasher.
func (s *authService) VerifyPassword(password, hash string) (bool, error) {
	return s.VerifyPasswordContext(context.Background(), password, hash)
}

// VerifyPasswordContext verifies a password with context cancellation support.
func (s *authService) VerifyPasswordContext(ctx context.Context, password, hash string) (bool, error) {
	if s.hasher == nil {
		return false, errors.New("hasher is not configured")
	}
	return s.hasher.VerifyPasswordContext(ctx, password, hash)
}

// GenerateTokenPair signs access and refresh tokens for a user.
func (s *authService) GenerateTokenPair(userID string, roles []string) (*TokenPair, error) {
	if s.tokenManager == nil {
		return nil, errors.New("token manager is not configured")
	}
	return s.tokenManager.GenerateTokenPair(userID, roles)
}

// ValidateAccessToken parses and verifies an access token.
func (s *authService) ValidateAccessToken(ctx context.Context, tokenStr string) (*Claims, error) {
	if s.tokenManager == nil {
		return nil, errors.New("token manager is not configured")
	}
	return s.tokenManager.ValidateAccessToken(ctx, tokenStr)
}

// RefreshToken validates a refresh token, revokes it, and issues a new token pair.
func (s *authService) RefreshToken(ctx context.Context, refreshTokenStr string) (*TokenPair, error) {
	if s.tokenManager == nil {
		return nil, errors.New("token manager is not configured")
	}

	claims, err := s.tokenManager.ValidateRefreshToken(ctx, refreshTokenStr)
	if err != nil {
		return nil, err
	}

	// Revoke old refresh token JTI for remaining TTL to prevent token replay
	var remainingTTL time.Duration
	if claims.ExpiresAt != nil {
		remainingTTL = time.Until(claims.ExpiresAt.Time)
	}
	if remainingTTL <= 0 {
		remainingTTL = 24 * time.Hour
	}
	if err := s.tokenManager.RevokeToken(ctx, claims.ID, remainingTTL); err != nil {
		return nil, fmt.Errorf("failed to revoke old refresh token: %w", err)
	}

	return s.tokenManager.GenerateTokenPair(claims.UserID, claims.Roles)
}

// RevokeToken marks a token JTI as revoked.
func (s *authService) RevokeToken(ctx context.Context, jti string, ttl time.Duration) error {
	if s.tokenManager == nil {
		return errors.New("token manager is not configured")
	}
	return s.tokenManager.RevokeToken(ctx, jti, ttl)
}
