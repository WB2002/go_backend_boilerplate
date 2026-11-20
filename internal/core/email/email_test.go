package email

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockMailer(t *testing.T) {
	mailer := NewMockMailer()
	require.NotNil(t, mailer)

	msg := Message{
		To:      "user@example.com",
		Subject: "Welcome",
		Data:    map[string]any{"Token": "123456"},
	}

	err := mailer.Send(context.Background(), msg)
	assert.NoError(t, err)
	assert.Len(t, mailer.SentMessages, 1)
	assert.Equal(t, "user@example.com", mailer.SentMessages[0].To)
	assert.Equal(t, "123456", mailer.SentMessages[0].Data["Token"])

	mailer.Reset()
	assert.Empty(t, mailer.SentMessages)
}

func TestSMTPMailerConstructor(t *testing.T) {
	cfg := SMTPConfig{
		Host: "localhost",
		Port: 1025,
		From: "test@example.com",
	}
	mailer := NewSMTPMailer(cfg)
	assert.NotNil(t, mailer)
	assert.Equal(t, "localhost", mailer.cfg.Host)
}
