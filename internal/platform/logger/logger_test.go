package logger_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/platform/logger"
)

func TestLoggerPIIMasking(t *testing.T) {
	var buf bytes.Buffer
	log := logger.NewWithWriter(&buf, "production")

	// Test masking email
	log.Info().Str("email", "john.doe@example.com").Msg("user created")
	output := buf.String()
	assert.NotContains(t, output, "john.doe@example.com")
	assert.Contains(t, output, "[REDACTED_EMAIL]")
	buf.Reset()

	// Test masking password
	log.Info().Str("password", "super-secret-password-123").Msg("login attempted")
	output = buf.String()
	assert.NotContains(t, output, "super-secret-password-123")
	assert.Contains(t, output, "[REDACTED]")
	buf.Reset()

	// Test masking Authorization bearer token
	log.Info().Str("auth", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.dummytoken").Msg("token verified")
	output = buf.String()
	assert.NotContains(t, output, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.dummytoken")
	assert.Contains(t, output, "Bearer [REDACTED_TOKEN]")
	buf.Reset()
}

func TestMaskStringDirect(t *testing.T) {
	raw := "Contact user at admin@example.com with password 'my-pass' and Bearer 1234567890abcdef"
	masked := logger.MaskPII(raw)

	assert.NotContains(t, masked, "admin@example.com")
	assert.NotContains(t, masked, "Bearer 1234567890abcdef")
	assert.Contains(t, masked, "[REDACTED_EMAIL]")
	assert.Contains(t, masked, "Bearer [REDACTED_TOKEN]")
}

func TestMaskQueryParameters(t *testing.T) {
	query := "param=value&token=xyz123&api_key=secret456&access_token=token789&refresh_token=refresh000&pass=p123"
	masked := logger.MaskPII(query)

	assert.Contains(t, masked, "param=value")
	assert.Contains(t, masked, "token=[REDACTED]")
	assert.Contains(t, masked, "api_key=[REDACTED]")
	assert.Contains(t, masked, "access_token=[REDACTED]")
	assert.Contains(t, masked, "refresh_token=[REDACTED]")
	assert.Contains(t, masked, "pass=[REDACTED]")

	assert.NotContains(t, masked, "xyz123")
	assert.NotContains(t, masked, "secret456")
	assert.NotContains(t, masked, "token789")
	assert.NotContains(t, masked, "refresh000")
	assert.NotContains(t, masked, "p123")
}

func TestLogger_MaskPII_ExtendedPatterns(t *testing.T) {
	assert.Equal(t, "Basic [REDACTED]", logger.MaskPII("Basic dXNlcjpwYXNz"))
	assert.Equal(t, "Authorization: Basic [REDACTED]", logger.MaskPII("Authorization: basic YWJjOjEyMw=="))
	assert.Equal(t, `"password_hash":"[REDACTED]"`, logger.MaskPII(`"password_hash":"$argon2id$v=19$..."`))
	assert.Equal(t, `"password_hash": "[REDACTED]"`, logger.MaskPII(`"password_hash": "$2a$10$hashvalue"`))
	assert.Equal(t, "postgres://user:[REDACTED]@postgres:5432/db", logger.MaskPII("postgres://user:supersecret@postgres:5432/db"))
	assert.Equal(t, "postgres://user:[REDACTED]@db.internal.local:5432/db", logger.MaskPII("postgres://user:supersecret@db.internal.local:5432/db"))
	assert.Equal(t, "primary=postgres://u1:[REDACTED]@db1:5432/a replica=redis://:[REDACTED]@redis:6379/0", logger.MaskPII("primary=postgres://u1:p1@db1:5432/a replica=redis://:p2@redis:6379/0"))
	assert.Equal(t, "https://example.com:8080/path and postgres://u:[REDACTED]@postgres:5432/db", logger.MaskPII("https://example.com:8080/path and postgres://u:p@postgres:5432/db"))
}
