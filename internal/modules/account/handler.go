package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"go_backend_boilerplate/internal/core/audit"
	"go_backend_boilerplate/internal/core/auth"
	"go_backend_boilerplate/internal/core/email"
	"go_backend_boilerplate/internal/core/middleware"
	"go_backend_boilerplate/internal/core/notification"
	"go_backend_boilerplate/internal/core/problem"
)

const dummyArgon2idHash = "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHRzdHJpbmc$dGVzdGhhc2hkdW1teXZhbHVlZm9ydGltaW5n"

// Handler handles user registration, authentication, token refresh, and profile management.
type Handler struct {
	repo         Repository
	hasher       auth.PasswordHasher
	tokenManager auth.TokenManager
	slack        *notification.SlackService
	mailer       email.Mailer
	audit        audit.Service
}

// NewHandler constructs an Account Handler.
func NewHandler(repo Repository, hasher auth.PasswordHasher, tokenManager auth.TokenManager, slack *notification.SlackService) *Handler {
	return NewHandlerWithAudit(repo, hasher, tokenManager, slack, nil, nil)
}

// NewHandlerWithMailer constructs an Account Handler with an optional transactional mailer.
func NewHandlerWithMailer(repo Repository, hasher auth.PasswordHasher, tokenManager auth.TokenManager, slack *notification.SlackService, mailer email.Mailer) *Handler {
	return NewHandlerWithAudit(repo, hasher, tokenManager, slack, mailer, nil)
}

// NewHandlerWithAudit constructs an Account Handler with transactional mailer and structured audit logging.
func NewHandlerWithAudit(repo Repository, hasher auth.PasswordHasher, tokenManager auth.TokenManager, slack *notification.SlackService, mailer email.Mailer, audit audit.Service) *Handler {
	return &Handler{
		repo:         repo,
		hasher:       hasher,
		tokenManager: tokenManager,
		slack:        slack,
		mailer:       mailer,
		audit:        audit,
	}
}

// RegisterRoutes mounts all account domain endpoints on the provided router.
func (h *Handler) RegisterRoutes(r gin.IRoutes, authMiddleware gin.HandlerFunc) {
	r.POST("/account/register", h.Register)
	r.POST("/account/login", h.Login)
	r.POST("/account/refresh", h.Refresh)
	r.POST("/account/forgot-password", h.ForgotPassword)
	r.POST("/account/reset-password", h.ResetPassword)
	r.GET("/account/me", authMiddleware, h.Me)
	r.POST("/account/change-password", authMiddleware, h.ChangePassword)
}

// RegisterGroup mounts account endpoints on an existing gin.RouterGroup.
func (h *Handler) RegisterGroup(rg *gin.RouterGroup, authMiddleware gin.HandlerFunc) {
	rg.POST("/register", h.Register)
	rg.POST("/login", h.Login)
	rg.POST("/refresh", h.Refresh)
	rg.POST("/forgot-password", h.ForgotPassword)
	rg.POST("/reset-password", h.ResetPassword)
	rg.GET("/me", authMiddleware, h.Me)
	rg.POST("/change-password", authMiddleware, h.ChangePassword)
}

// Register handles user account registration.
// @Summary Register a new user account
// @Description Creates a new user profile with hashed password credentials
// @Tags Account
// @Accept json
// @Produce json
// @Param request body RegisterRequest true "Registration payload"
// @Success 201 {object} UserResponse
// @Failure 400 {object} problem.Details
// @Failure 409 {object} problem.Details
// @Failure 500 {object} problem.Details
// @Router /account/register [post]
func (h *Handler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		problem.RespondValidation(c, err)
		return
	}

	if !isStrongPassword(req.Password) {
		problem.Respond(c, http.StatusBadRequest, "INVALID_PASSWORD", "Invalid Password", "Password does not meet complexity requirements.")
		return
	}

	// Pre-check for duplicate email
	existing, _ := h.repo.FindByEmail(c.Request.Context(), req.Email)
	if existing != nil {
		problem.Respond(c, http.StatusConflict, "EMAIL_ALREADY_REGISTERED", "Conflict", "Email is already registered.")
		return
	}

	hash, err := h.hasher.HashPasswordContext(c.Request.Context(), req.Password)
	if err != nil {
		problem.Respond(c, http.StatusServiceUnavailable, "HASHER_UNAVAILABLE", "Service Unavailable", "Password hashing service unavailable.")
		return
	}

	user := &User{
		ID:           uuid.NewString(),
		Email:        strings.ToLower(strings.TrimSpace(req.Email)),
		PasswordHash: hash,
		Roles:        Roles{"user"},
		Status:       "active",
	}

	if err := h.repo.Create(c.Request.Context(), user); err != nil {
		if errors.Is(err, ErrDuplicateEmail) || isDuplicateKeyError(err) {
			problem.Respond(c, http.StatusConflict, "EMAIL_ALREADY_REGISTERED", "Conflict", "Email is already registered.")
			return
		}
		problem.Respond(c, http.StatusInternalServerError, "CREATE_USER_FAILED", "Internal Server Error", "Failed to create user account.")
		return
	}

	if h.slack != nil {
		go func() {
			_ = h.slack.Send(context.Background(), notification.Payload{
				Title:   "New User Registration",
				Message: fmt.Sprintf("User %s registered successfully", user.Email),
				Level:   "info",
			})
		}()
	}

	c.JSON(http.StatusCreated, ToUserResponse(user))
}

// Login verifies credentials and issues RS256 access and refresh tokens.
// @Summary Authenticate user
// @Description Verifies user email and password, returning JWT access and refresh token pair
// @Tags Account
// @Accept json
// @Produce json
// @Param request body LoginRequest true "Login credentials"
// @Success 200 {object} TokenResponse
// @Failure 400 {object} problem.Details
// @Failure 401 {object} problem.Details
// @Failure 500 {object} problem.Details
// @Router /account/login [post]
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		problem.RespondValidation(c, err)
		return
	}

	user, err := h.repo.FindByEmail(c.Request.Context(), req.Email)
	userFound := (err == nil && user != nil)

	hashToVerify := dummyArgon2idHash
	if userFound {
		hashToVerify = user.PasswordHash
	}

	valid, _ := h.hasher.VerifyPasswordContext(c.Request.Context(), req.Password, hashToVerify)
	if !userFound || !valid || user.Status != "active" {
		if h.audit != nil {
			h.audit.LogAsync(audit.Entry{
				ActorID:      "anonymous",
				ActorEmail:   req.Email,
				Action:       "account.login",
				TargetType:   "account",
				TargetID:     req.Email,
				IPAddress:    middleware.GetClientIP(c),
				UserAgent:    middleware.GetUserAgent(c),
				Status:       "failure",
				ErrorMessage: "invalid credentials",
			})
		}
		problem.Respond(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Unauthorized", "Invalid credentials.")
		return
	}

	tokens, err := h.tokenManager.GenerateTokenPair(user.ID, []string(user.Roles))
	if err != nil {
		problem.Respond(c, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Internal Server Error", "Failed to issue authentication tokens.")
		return
	}

	if h.audit != nil {
		h.audit.LogAsync(audit.Entry{
			ActorID:    user.ID,
			ActorEmail: user.Email,
			Action:     "account.login",
			TargetType: "account",
			TargetID:   user.ID,
			IPAddress:  middleware.GetClientIP(c),
			UserAgent:  middleware.GetUserAgent(c),
			Status:     "success",
		})
	}

	expiresIn := int64(time.Until(tokens.ExpiresAt).Seconds())
	if expiresIn <= 0 {
		expiresIn = 900
	}

	c.JSON(http.StatusOK, TokenResponse{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    expiresIn,
	})
}

// Refresh handles token rotation and revocation of consumed refresh tokens.
// @Summary Rotate refresh token
// @Description Invalidates consumed refresh token and issues a new access and refresh token pair
// @Tags Account
// @Accept json
// @Produce json
// @Success 200 {object} TokenResponse
// @Failure 400 {object} problem.Details
// @Failure 401 {object} problem.Details
// @Failure 500 {object} problem.Details
// @Router /account/refresh [post]
func (h *Handler) Refresh(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	_ = c.ShouldBindJSON(&req)
	refreshToken := req.RefreshToken
	if refreshToken == "" {
		refreshToken = c.GetHeader("X-Refresh-Token")
	}
	if refreshToken == "" {
		authHeader := c.GetHeader("Authorization")
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			refreshToken = strings.TrimSpace(authHeader[7:])
		}
	}
	if refreshToken == "" {
		problem.Respond(c, http.StatusBadRequest, "MISSING_REFRESH_TOKEN", "Bad Request", "Missing refresh token.")
		return
	}

	claims, err := h.tokenManager.ValidateRefreshToken(c.Request.Context(), refreshToken)
	if err != nil {
		problem.Respond(c, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "Unauthorized", "Invalid or expired refresh token.")
		return
	}

	user, err := h.repo.FindByID(c.Request.Context(), claims.UserID)
	if err != nil || user == nil || user.Status != "active" {
		problem.Respond(c, http.StatusUnauthorized, "INACTIVE_USER", "Unauthorized", "User account is not active.")
		return
	}

	if user.PasswordChangedAt != nil {
		if claims.IssuedAt == nil || !claims.IssuedAt.Time.After(*user.PasswordChangedAt) {
			problem.Respond(c, http.StatusUnauthorized, "TOKEN_INVALIDATED", "Unauthorized", "Refresh token invalidated due to password change.")
			return
		}
	}

	if claims.ID == "" {
		problem.Respond(c, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "Unauthorized", "Invalid or expired refresh token.")
		return
	}

	var remainingTTL time.Duration
	if claims.ExpiresAt != nil {
		remainingTTL = time.Until(claims.ExpiresAt.Time)
	}
	if remainingTTL <= 0 {
		remainingTTL = 24 * time.Hour
	}
	revoked, err := h.tokenManager.RevokeTokenOnce(c.Request.Context(), claims.ID, remainingTTL)
	if err != nil {
		problem.Respond(c, http.StatusServiceUnavailable, "TOKEN_REVOCATION_FAILED", "Service Unavailable", "Failed to revoke refresh token.")
		return
	}
	if !revoked {
		problem.Respond(c, http.StatusUnauthorized, "REFRESH_TOKEN_REPLAY", "Unauthorized", "Refresh token has already been consumed.")
		return
	}

	tokens, err := h.tokenManager.GenerateTokenPair(user.ID, []string(user.Roles))
	if err != nil {
		problem.Respond(c, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Internal Server Error", "Failed to generate token pair.")
		return
	}

	expiresIn := int64(time.Until(tokens.ExpiresAt).Seconds())
	if expiresIn <= 0 {
		expiresIn = 900
	}

	c.JSON(http.StatusOK, TokenResponse{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    expiresIn,
	})
}

// Me returns the profile of the authenticated user.
// @Summary Get user profile
// @Description Returns the profile data of the currently authenticated user
// @Tags Account
// @Produce json
// @Security BearerAuth
// @Success 200 {object} UserResponse
// @Failure 401 {object} problem.Details
// @Failure 404 {object} problem.Details
// @Failure 500 {object} problem.Details
// @Router /account/me [get]
func (h *Handler) Me(c *gin.Context) {
	claims, ok := middleware.GetUser(c)
	if !ok || claims == nil {
		problem.Respond(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "Authentication required.")
		return
	}

	user, err := h.repo.FindByID(c.Request.Context(), claims.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			problem.Respond(c, http.StatusNotFound, "USER_NOT_FOUND", "Not Found", "User not found.")
			return
		}
		problem.Respond(c, http.StatusInternalServerError, "LOAD_USER_FAILED", "Internal Server Error", "Failed to load user profile.")
		return
	}

	c.JSON(http.StatusOK, ToUserResponse(user))
}

// ChangePassword updates the authenticated user's password.
// @Summary Change user password
// @Description Updates user password given verified old password and compliant new password
// @Tags Account
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body ChangePasswordRequest true "Change password payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} problem.Details
// @Failure 401 {object} problem.Details
// @Failure 500 {object} problem.Details
// @Router /account/change-password [post]
func (h *Handler) ChangePassword(c *gin.Context) {
	claims, ok := middleware.GetUser(c)
	if !ok || claims == nil {
		problem.Respond(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "Authentication required.")
		return
	}

	var req ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		problem.RespondValidation(c, err)
		return
	}

	if !isStrongPassword(req.NewPassword) {
		problem.Respond(c, http.StatusBadRequest, "INVALID_PASSWORD", "Invalid Password", "New password does not meet security requirements.")
		return
	}

	user, err := h.repo.FindByID(c.Request.Context(), claims.UserID)
	if err != nil {
		problem.Respond(c, http.StatusNotFound, "USER_NOT_FOUND", "Not Found", "User not found.")
		return
	}

	valid, err := h.hasher.VerifyPasswordContext(c.Request.Context(), req.OldPassword, user.PasswordHash)
	if err != nil || !valid {
		problem.Respond(c, http.StatusBadRequest, "INVALID_OLD_PASSWORD", "Bad Request", "Current password does not match.")
		return
	}

	newHash, err := h.hasher.HashPasswordContext(c.Request.Context(), req.NewPassword)
	if err != nil {
		problem.Respond(c, http.StatusInternalServerError, "HASH_PASSWORD_FAILED", "Internal Server Error", "Failed to hash new password.")
		return
	}

	if err := h.repo.UpdatePasswordWithTimestamp(c.Request.Context(), user.ID, newHash, time.Now().UTC()); err != nil {
		problem.Respond(c, http.StatusInternalServerError, "UPDATE_PASSWORD_FAILED", "Internal Server Error", "Failed to update password.")
		return
	}

	if h.audit != nil {
		h.audit.LogAsync(audit.Entry{
			ActorID:    user.ID,
			ActorEmail: user.Email,
			Action:     "account.change_password",
			TargetType: "account",
			TargetID:   user.ID,
			IPAddress:  middleware.GetClientIP(c),
			UserAgent:  middleware.GetUserAgent(c),
			Status:     "success",
		})
	}

	c.JSON(http.StatusOK, gin.H{"message": "password updated successfully"})
}

// ForgotPassword initiates password recovery by issuing a time-limited token.
// @Summary Request password reset
// @Description Sends a password reset link to the email address if it exists
// @Tags Account
// @Accept json
// @Produce json
// @Param request body ForgotPasswordRequest true "Forgot password payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} problem.Details
// @Failure 500 {object} problem.Details
// @Router /account/forgot-password [post]
func (h *Handler) ForgotPassword(c *gin.Context) {
	var req ForgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		problem.RespondValidation(c, err)
		return
	}

	user, err := h.repo.FindByEmail(c.Request.Context(), req.Email)
	if err != nil || user == nil || user.Status != "active" {
		// Run dummy verification to defeat timing attacks and return generic success
		_, _ = h.hasher.VerifyPasswordContext(c.Request.Context(), "dummy-timing-probe", dummyArgon2idHash)
		c.JSON(http.StatusOK, gin.H{"status": "if that email is registered, a password reset link has been sent"})
		return
	}

	rawToken := uuid.NewString() + "-" + uuid.NewString()
	hashBytes := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hashBytes[:])

	reset := &PasswordReset{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}

	if err := h.repo.CreatePasswordReset(c.Request.Context(), reset); err != nil {
		problem.Respond(c, http.StatusInternalServerError, "PASSWORD_RESET_FAILED", "Internal Server Error", "Failed to initiate password recovery.")
		return
	}

	if h.mailer != nil {
		bodyText := fmt.Sprintf(
			"You requested a password reset.\n\nYour password reset token is: %s\n\nUse this token to reset your password within 15 minutes. If you did not request a password reset, you can safely ignore this email.",
			rawToken,
		)
		bodyHTML := fmt.Sprintf(
			"<p>You requested a password reset.</p><p>Your password reset token is: <code>%s</code></p><p>Use this token to reset your password within 15 minutes. If you did not request a password reset, you can safely ignore this email.</p>",
			html.EscapeString(rawToken),
		)
		_ = h.mailer.Send(c.Request.Context(), email.Message{
			To:       user.Email,
			Subject:  "Password Reset Request",
			BodyText: bodyText,
			BodyHTML: bodyHTML,
			Data: map[string]any{
				"Token": rawToken,
			},
		})
	}

	c.JSON(http.StatusOK, gin.H{"status": "if that email is registered, a password reset link has been sent"})
}

// ResetPassword finalizes password recovery with a valid token.
// @Summary Finalize password reset
// @Description Updates user password given a valid 15-minute recovery token
// @Tags Account
// @Accept json
// @Produce json
// @Param request body ResetPasswordRequest true "Reset password payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} problem.Details
// @Failure 500 {object} problem.Details
// @Router /account/reset-password [post]
func (h *Handler) ResetPassword(c *gin.Context) {
	var req ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		problem.RespondValidation(c, err)
		return
	}

	if !isStrongPassword(req.NewPassword) {
		problem.Respond(c, http.StatusBadRequest, "INVALID_PASSWORD", "Invalid Password", "New password does not meet security requirements.")
		return
	}

	hashBytes := sha256.Sum256([]byte(req.Token))
	tokenHash := hex.EncodeToString(hashBytes[:])

	reset, err := h.repo.FindValidPasswordReset(c.Request.Context(), tokenHash)
	if err != nil || reset == nil {
		problem.Respond(c, http.StatusBadRequest, "INVALID_OR_EXPIRED_TOKEN", "Bad Request", "Password reset token is invalid or expired.")
		return
	}

	newHash, err := h.hasher.HashPasswordContext(c.Request.Context(), req.NewPassword)
	if err != nil {
		problem.Respond(c, http.StatusServiceUnavailable, "HASHER_UNAVAILABLE", "Service Unavailable", "Password hashing service unavailable.")
		return
	}

	if err := h.repo.AtomicResetPassword(c.Request.Context(), tokenHash, newHash, time.Now().UTC()); err != nil {
		if errors.Is(err, ErrInvalidToken) {
			problem.Respond(c, http.StatusBadRequest, "INVALID_OR_EXPIRED_TOKEN", "Bad Request", "Password reset token is invalid or expired.")
			return
		}
		problem.Respond(c, http.StatusInternalServerError, "UPDATE_PASSWORD_FAILED", "Internal Server Error", "Failed to update password.")
		return
	}

	if h.audit != nil {
		h.audit.LogAsync(audit.Entry{
			ActorID:    reset.UserID,
			Action:     "account.reset_password",
			TargetType: "account",
			TargetID:   reset.UserID,
			IPAddress:  middleware.GetClientIP(c),
			UserAgent:  middleware.GetUserAgent(c),
			Status:     "success",
		})
	}

	c.JSON(http.StatusOK, gin.H{"status": "password has been successfully reset"})
}

func isStrongPassword(p string) bool {
	if len(p) < 8 || len(p) > 128 {
		return false
	}
	var hasLetter, hasDigitOrSpecial bool
	for _, ch := range p {
		switch {
		case unicode.IsLetter(ch):
			hasLetter = true
		case unicode.IsDigit(ch) || unicode.IsPunct(ch) || unicode.IsSymbol(ch):
			hasDigitOrSpecial = true
		}
	}
	return hasLetter && hasDigitOrSpecial
}
