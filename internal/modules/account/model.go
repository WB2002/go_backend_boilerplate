package account

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Roles represents user role assignments compatible with both Postgres arrays and SQLite text.
type Roles []string

// Value implements the database/sql/driver.Valuer interface.
func (r Roles) Value() (driver.Value, error) {
	if len(r) == 0 {
		return "{}", nil
	}
	return "{" + strings.Join(r, ",") + "}", nil
}

// Scan implements the database/sql.Scanner interface.
func (r *Roles) Scan(src interface{}) error {
	if src == nil {
		*r = []string{}
		return nil
	}
	switch v := src.(type) {
	case string:
		*r = parseRoles(v)
	case []byte:
		*r = parseRoles(string(v))
	default:
		return fmt.Errorf("unsupported type for Roles: %T", src)
	}
	return nil
}

func parseRoles(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "[]" {
		return []string{}
	}

	// Try JSON array parsing
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		var slice []string
		if err := json.Unmarshal([]byte(raw), &slice); err == nil {
			return slice
		}
	}

	// Postgres array format {user,admin}
	trimmed := strings.TrimPrefix(raw, "{")
	trimmed = strings.TrimSuffix(trimmed, "}")

	parts := strings.Split(trimmed, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		clean := strings.Trim(strings.TrimSpace(p), "\"")
		if clean != "" {
			res = append(res, clean)
		}
	}
	return res
}

// User represents the persistent user account domain entity.
type User struct {
	ID           string    `gorm:"type:uuid;primaryKey" json:"id"`
	Email        string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"email"`
	PasswordHash string    `gorm:"not null" json:"-"`
	Roles        Roles     `gorm:"type:text;not null" json:"roles"`
	Status       string    `gorm:"type:varchar(50);not null;default:'active'" json:"status"`
	CreatedAt    time.Time `gorm:"not null;default:CURRENT_TIMESTAMP" json:"createdAt"`
	UpdatedAt    time.Time `gorm:"not null;default:CURRENT_TIMESTAMP" json:"updatedAt"`
}

// TableName overrides the default table name for User.
func (User) TableName() string {
	return "users"
}

// BeforeCreate GORM hook sets defaults for ID, Roles, and Status.
func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	if len(u.Roles) == 0 {
		u.Roles = Roles{"user"}
	}
	if u.Status == "" {
		u.Status = "active"
	}
	return nil
}

// RegisterRequest represents the registration request body.
type RegisterRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8,max=128"`
}

// LoginRequest represents user login credentials.
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// TokenResponse represents authenticated token response payload.
type TokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int64  `json:"expiresIn"`
}

// ChangePasswordRequest represents a password rotation payload.
type ChangePasswordRequest struct {
	OldPassword string `json:"oldPassword" binding:"required"`
	NewPassword string `json:"newPassword" binding:"required,min=8,max=128"`
}

// ForgotPasswordRequest represents a request to initiate password recovery.
type ForgotPasswordRequest struct {
	Email string `json:"email" binding:"required,email"`
}

// ResetPasswordRequest represents a request to finalize password recovery with a token.
type ResetPasswordRequest struct {
	Token       string `json:"token" binding:"required"`
	NewPassword string `json:"newPassword" binding:"required,min=8,max=128"`
}

// PasswordReset records an active or consumed password recovery token.
type PasswordReset struct {
	ID        string     `gorm:"type:uuid;primaryKey" json:"id"`
	UserID    string     `gorm:"type:uuid;not null;index" json:"userId"`
	TokenHash string     `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
	ExpiresAt time.Time  `gorm:"not null" json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	CreatedAt time.Time  `gorm:"autoCreateTime" json:"createdAt"`
}

// UserResponse represents the public user account profile.
type UserResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Roles     []string  `json:"roles"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

// ToUserResponse converts a User entity to a safe public UserResponse.
func ToUserResponse(u *User) *UserResponse {
	if u == nil {
		return nil
	}
	roles := []string(u.Roles)
	if roles == nil {
		roles = []string{}
	}
	return &UserResponse{
		ID:        u.ID,
		Email:     u.Email,
		Roles:     roles,
		Status:    u.Status,
		CreatedAt: u.CreatedAt,
	}
}
