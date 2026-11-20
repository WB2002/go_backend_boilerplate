package account

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"

	"go_backend_boilerplate/internal/core/auth"
	"go_backend_boilerplate/internal/core/middleware"
)

var (
	testKeyOnce sync.Once
	testPrivPEM string
	testPubPEM  string
)

func generateTestRSAKeys() (string, string) {
	testKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		privBytes := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		})
		pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			panic(err)
		}
		pubPEMBytes := pem.EncodeToMemory(&pem.Block{
			Type:  "PUBLIC KEY",
			Bytes: pubBytes,
		})
		testPrivPEM = string(privBytes)
		testPubPEM = string(pubPEMBytes)
	})
	return testPrivPEM, testPubPEM
}

// SetupTestRouter constructs a test Gin engine with in-memory SQLite and mock auth for testing.
func SetupTestRouter(t *testing.T) *gin.Engine {
	r, _ := SetupTestRouterWithDB(t)
	return r
}

// SetupTestRouterWithDB constructs a test Gin engine with in-memory SQLite and returns both router and DB.
func SetupTestRouterWithDB(t *testing.T) (*gin.Engine, *gorm.DB) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	dbName := fmt.Sprintf("file:test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{
		Logger: gormLogger.Default.LogMode(gormLogger.Silent),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	err = db.AutoMigrate(&User{}, &PasswordReset{})
	require.NoError(t, err)

	repo := NewRepository(db)
	hasher := auth.NewArgon2idHasher(2, 4, 2*time.Second)

	privPEM, pubPEM := generateTestRSAKeys()
	tokenMgr, err := auth.NewJWTTokenManager(privPEM, pubPEM, 15*time.Minute, 7*24*time.Hour, nil)
	require.NoError(t, err)

	handler := NewHandler(repo, hasher, tokenMgr, nil)
	authMiddleware := middleware.AuthMiddleware(tokenMgr)
	handler.RegisterRoutes(r, authMiddleware)

	return r, db
}

// MockRepository provides an in-memory implementation of Repository for unit tests.
type MockRepository struct {
	mu     sync.Mutex
	users  map[string]*User
	resets map[string]*PasswordReset
}

// NewMockRepository constructs a MockRepository.
func NewMockRepository() *MockRepository {
	return &MockRepository{
		users:  make(map[string]*User),
		resets: make(map[string]*PasswordReset),
	}
}

func (m *MockRepository) Create(ctx context.Context, user *User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if strings.EqualFold(u.Email, user.Email) {
			return ErrDuplicateEmail
		}
	}
	m.users[user.ID] = user
	return nil
}

func (m *MockRepository) FindByEmail(ctx context.Context, email string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if strings.EqualFold(u.Email, email) {
			return u, nil
		}
	}
	return nil, ErrUserNotFound
}

func (m *MockRepository) FindByID(ctx context.Context, id string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	return u, nil
}

func (m *MockRepository) UpdatePassword(ctx context.Context, id, newHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return ErrUserNotFound
	}
	u.PasswordHash = newHash
	return nil
}

func (m *MockRepository) UpdatePasswordWithTimestamp(ctx context.Context, userID, newHash string, changedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	if !ok {
		return ErrUserNotFound
	}
	utcChangedAt := changedAt.UTC()
	u.PasswordHash = newHash
	u.PasswordChangedAt = &utcChangedAt
	u.UpdatedAt = utcChangedAt
	return nil
}

func (m *MockRepository) CreatePasswordReset(ctx context.Context, reset *PasswordReset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resets[reset.TokenHash] = reset
	return nil
}

func (m *MockRepository) FindValidPasswordReset(ctx context.Context, tokenHash string) (*PasswordReset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.resets[tokenHash]
	if !ok || r.UsedAt != nil || r.ExpiresAt.Before(time.Now().UTC()) {
		return nil, ErrInvalidToken
	}
	return r, nil
}

func (m *MockRepository) MarkPasswordResetUsed(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.resets {
		if r.ID == id {
			if r.UsedAt != nil {
				return errors.New("token already used")
			}
			now := time.Now().UTC()
			r.UsedAt = &now
			return nil
		}
	}
	return errors.New("token not found")
}

func (m *MockRepository) AtomicResetPassword(ctx context.Context, tokenHash, newHash string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	utcNow := now.UTC()
	r, ok := m.resets[tokenHash]
	if !ok || r.UsedAt != nil || !r.ExpiresAt.After(utcNow) {
		return ErrInvalidToken
	}
	u, ok := m.users[r.UserID]
	if !ok {
		return ErrUserNotFound
	}
	r.UsedAt = &utcNow
	u.PasswordHash = newHash
	u.PasswordChangedAt = &utcNow
	u.UpdatedAt = utcNow
	return nil
}
