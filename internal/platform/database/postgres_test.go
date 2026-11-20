package database_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/platform/database"
)

func TestPostgresConnectionConfig(t *testing.T) {
	cfg := config.DatabaseConfig{
		Host:            "invalid-host",
		Port:            5432,
		User:            "user",
		Password:        "pass",
		DBName:          "db",
		SSLMode:         "disable",
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 300,
	}

	_, err := database.NewPostgres(cfg)
	// Expect failure on invalid host, but verifies constructor and pooling logic
	assert.Error(t, err)
}

func TestPostgresSpecialCharacterPassword(t *testing.T) {
	specialPass := "p@ss#w0rd! /?"
	cfg := config.DatabaseConfig{
		Host:            "invalid-host",
		Port:            5432,
		User:            "admin_user",
		Password:        specialPass,
		DBName:          "prod_db",
		SSLMode:         "require",
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 300,
	}

	dsn := database.BuildDSN(cfg)

	// Must parse cleanly as a valid URL
	parsedURL, err := url.Parse(dsn)
	assert.NoError(t, err)
	assert.Equal(t, "postgres", parsedURL.Scheme)
	assert.Equal(t, "admin_user", parsedURL.User.Username())
	pass, ok := parsedURL.User.Password()
	assert.True(t, ok)
	assert.Equal(t, specialPass, pass)
	assert.Equal(t, "invalid-host:5432", parsedURL.Host)
	assert.Equal(t, "/prod_db", parsedURL.Path)
	assert.Equal(t, "require", parsedURL.Query().Get("sslmode"))
	assert.Equal(t, "2", parsedURL.Query().Get("connect_timeout"))

	// Verify that special characters produce a valid connection attempt without DSN parsing syntax errors
	_, err = database.NewPostgres(cfg)
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "cannot parse")
	assert.NotContains(t, err.Error(), "invalid DSN")
}

func TestPostgresValidation(t *testing.T) {
	t.Run("missing host", func(t *testing.T) {
		cfg := config.DatabaseConfig{
			User:   "user",
			DBName: "db",
		}
		_, err := database.NewPostgres(cfg)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "host is required")
	})

	t.Run("missing user", func(t *testing.T) {
		cfg := config.DatabaseConfig{
			Host:   "localhost",
			DBName: "db",
		}
		_, err := database.NewPostgres(cfg)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "user is required")
	})

	t.Run("missing db name", func(t *testing.T) {
		cfg := config.DatabaseConfig{
			Host: "localhost",
			User: "user",
		}
		_, err := database.NewPostgres(cfg)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "db name is required")
	})
}

func TestPostgresNilHelpers(t *testing.T) {
	ctx := context.Background()
	err := database.Ping(ctx, nil)
	assert.Error(t, err)

	err = database.Close(nil)
	assert.NoError(t, err)
}
