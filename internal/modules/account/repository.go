package account

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

var (
	// ErrUserNotFound indicates that the requested user account does not exist.
	ErrUserNotFound = errors.New("user not found")
	// ErrDuplicateEmail indicates that the email address is already registered.
	ErrDuplicateEmail = errors.New("email already registered")
	// ErrInvalidToken indicates that a password reset token is invalid, expired, or already used.
	ErrInvalidToken = errors.New("invalid or expired token")
)

// Repository specifies persistence operations for user accounts and password recovery.
type Repository interface {
	Create(ctx context.Context, user *User) error
	FindByEmail(ctx context.Context, email string) (*User, error)
	FindByID(ctx context.Context, id string) (*User, error)
	UpdatePassword(ctx context.Context, id, newHash string) error
	UpdatePasswordWithTimestamp(ctx context.Context, userID, newHash string, changedAt time.Time) error
	CreatePasswordReset(ctx context.Context, reset *PasswordReset) error
	FindValidPasswordReset(ctx context.Context, tokenHash string) (*PasswordReset, error)
	MarkPasswordResetUsed(ctx context.Context, id string) error
	AtomicResetPassword(ctx context.Context, tokenHash, newHash string, now time.Time) error
}

// GormRepository implements Repository using GORM.
type GormRepository struct {
	db *gorm.DB
}

// NewRepository creates a new GORM-backed account repository.
func NewRepository(db *gorm.DB) Repository {
	return NewGormRepository(db)
}

// NewGormRepository constructs a GormRepository.
func NewGormRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db}
}

// Create inserts a new user record.
func (r *GormRepository) Create(ctx context.Context, user *User) error {
	if r.db == nil {
		return errors.New("database connection is nil")
	}

	err := r.db.WithContext(ctx).Create(user).Error
	if err != nil {
		if isDuplicateKeyError(err) {
			return ErrDuplicateEmail
		}
		return err
	}
	return nil
}

// FindByEmail searches for a user by email address (case-insensitive).
func (r *GormRepository) FindByEmail(ctx context.Context, email string) (*User, error) {
	if r.db == nil {
		return nil, errors.New("database connection is nil")
	}

	var user User
	err := r.db.WithContext(ctx).Where("LOWER(email) = LOWER(?)", strings.TrimSpace(email)).First(&user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &user, nil
}

// FindByID retrieves a user by their UUID primary key.
func (r *GormRepository) FindByID(ctx context.Context, id string) (*User, error) {
	if r.db == nil {
		return nil, errors.New("database connection is nil")
	}

	var user User
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &user, nil
}

// UpdatePassword sets a new password hash for the user.
func (r *GormRepository) UpdatePassword(ctx context.Context, id, newHash string) error {
	if r.db == nil {
		return errors.New("database connection is nil")
	}

	res := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Update("password_hash", newHash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// UpdatePasswordWithTimestamp sets a new password hash and records password_changed_at.
func (r *GormRepository) UpdatePasswordWithTimestamp(ctx context.Context, userID, newHash string, changedAt time.Time) error {
	if r.db == nil {
		return errors.New("database connection is nil")
	}

	utcChangedAt := changedAt.UTC()
	res := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).Updates(map[string]interface{}{
		"password_hash":       newHash,
		"password_changed_at": &utcChangedAt,
		"updated_at":          utcChangedAt,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// CreatePasswordReset persists a password recovery token.
func (r *GormRepository) CreatePasswordReset(ctx context.Context, reset *PasswordReset) error {
	if r.db == nil {
		return errors.New("database connection is nil")
	}
	return r.db.WithContext(ctx).Create(reset).Error
}

// FindValidPasswordReset finds an unexpired, unused password reset token by hash.
func (r *GormRepository) FindValidPasswordReset(ctx context.Context, tokenHash string) (*PasswordReset, error) {
	if r.db == nil {
		return nil, errors.New("database connection is nil")
	}
	var reset PasswordReset
	err := r.db.WithContext(ctx).
		Where("token_hash = ? AND used_at IS NULL AND expires_at > ?", tokenHash, time.Now().UTC()).
		First(&reset).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}
	return &reset, nil
}

// MarkPasswordResetUsed marks the reset token as used.
func (r *GormRepository) MarkPasswordResetUsed(ctx context.Context, id string) error {
	if r.db == nil {
		return errors.New("database connection is nil")
	}
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&PasswordReset{}).Where("id = ? AND used_at IS NULL", id).Update("used_at", &now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("token already used or not found")
	}
	return nil
}

// AtomicResetPassword atomically consumes an unexpired, unused reset token and updates the user's password and password_changed_at in a single transaction.
func (r *GormRepository) AtomicResetPassword(ctx context.Context, tokenHash, newHash string, now time.Time) error {
	if r.db == nil {
		return errors.New("database connection is nil")
	}

	utcNow := now.UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&PasswordReset{}).
			Where("token_hash = ? AND used_at IS NULL AND expires_at > ?", tokenHash, utcNow).
			Update("used_at", &utcNow)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrInvalidToken
		}

		var reset PasswordReset
		if err := tx.Where("token_hash = ?", tokenHash).First(&reset).Error; err != nil {
			return err
		}

		userRes := tx.Model(&User{}).
			Where("id = ?", reset.UserID).
			Updates(map[string]interface{}{
				"password_hash":       newHash,
				"password_changed_at": &utcNow,
				"updated_at":          utcNow,
			})
		if userRes.Error != nil {
			return userRes.Error
		}
		if userRes.RowsAffected == 0 {
			return ErrUserNotFound
		}
		return nil
	})
}

func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "23505")
}
