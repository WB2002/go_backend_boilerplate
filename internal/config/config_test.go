package config_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/config"
)

func setEnv(key, val string) {
	_ = os.Setenv(key, val)
}

func unsetEnv(key string) {
	_ = os.Unsetenv(key)
}

func setMinimalValidEnv() {
	os.Clearenv()
	setEnv("PORT", "8080")
	setEnv("NODE_ENV", "test")
	setEnv("JWT_PRIVATE_KEY", "dummy-private-key")
	setEnv("JWT_PUBLIC_KEY", "dummy-public-key")
	setEnv("COOKIE_SECRET", "super-secret-key-32-bytes-minimum")
	setEnv("POSTGRES_HOST", "localhost")
	setEnv("POSTGRES_PORT", "5432")
	setEnv("POSTGRES_USER", "postgres")
	setEnv("POSTGRES_PASSWORD", "postgres")
	setEnv("POSTGRES_DB", "boilerplate_test")
	setEnv("REDIS_HOST", "localhost")
	setEnv("REDIS_PORT", "6379")
	setEnv("MONGO_URI", "mongodb://localhost:27017/test")
}

func TestConfigLoadSuccess(t *testing.T) {
	setMinimalValidEnv()

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.HTTP.Port)
	assert.Equal(t, "test", cfg.App.Env)
	assert.False(t, cfg.App.EnableSwagger)
	assert.False(t, cfg.App.EnablePlayground)
	assert.Equal(t, 65000, cfg.HTTP.KeepAliveTimeoutMs)
	assert.Equal(t, 30000, cfg.HTTP.RequestTimeoutMs)
	assert.Equal(t, 60000, cfg.HTTP.RequestTimeoutMaxMs)
	assert.Equal(t, 500, cfg.HTTP.ShutdownDrainMs)
	assert.Equal(t, 1000, cfg.HTTP.ShutdownForceMs)
	assert.Equal(t, 1024, cfg.HTTP.GzipMinSizeBytes)
	assert.Empty(t, cfg.HTTP.AllowedOrigins)

	// Auth defaults
	assert.Equal(t, "dummy-private-key", cfg.Auth.JWTPrivateKey)
	assert.Equal(t, 900, cfg.Auth.AccessTokenTTLSec)
	assert.Equal(t, uint32(65536), cfg.Auth.Argon2Memory)
	assert.Equal(t, 2, cfg.Auth.Argon2MaxConcurrent)

	// Database
	assert.Equal(t, "localhost", cfg.Database.Host)
	assert.Equal(t, 5432, cfg.Database.Port)
	assert.Equal(t, "require", cfg.Database.SSLMode)
	assert.Equal(t, 25, cfg.Database.MaxOpenConns)

	// Redis
	assert.Equal(t, "localhost", cfg.Redis.Host)
	assert.Equal(t, 6379, cfg.Redis.Port)
	assert.Equal(t, 50, cfg.Redis.PoolSize)
	assert.False(t, cfg.Redis.UseTLS)

	// Mongo
	assert.Equal(t, "mongodb://localhost:27017/test", cfg.Mongo.URI)
	assert.Equal(t, 5000, cfg.Mongo.TimeoutMs)

	// RateLimit & Overload & Cache
	assert.Equal(t, 100, cfg.RateLimit.DefaultLimit)
	assert.Equal(t, 1000, cfg.Overload.MaxInFlight)
	assert.True(t, cfg.Overload.PrioritySheddingEnabled)
	assert.Equal(t, 0.15, cfg.Cache.TTLJitterRatio)

	// Jobs & WebSocket & GraphQL
	assert.Equal(t, 10, cfg.Jobs.WorkerConcurrency)
	assert.Equal(t, 5, cfg.WebSocket.MaxConnectionsPerUser)
	assert.Equal(t, 10, cfg.GraphQL.MaxDepth)
	assert.Equal(t, 50, cfg.GraphQL.MaxComplexity)

	// Security & Telemetry & FeatureFlag & Notification
	assert.Equal(t, "GeoLite2-Country.mmdb", cfg.Security.GeoIPDBPath)
	assert.Equal(t, int64(5242880), cfg.Security.UploadMaxFileSize)
	assert.Equal(t, 2000, cfg.Telemetry.HealthCheckCacheTTLMs)
	assert.Equal(t, 45000, cfg.FeatureFlag.ReconcileMs)
	assert.Equal(t, "", cfg.Notification.SlackWebhookURL)
}

func TestConfigMissingRequiredFails(t *testing.T) {
	os.Clearenv()
	_, err := config.Load()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing required environment variable")
}

func TestConfigIndividualMissingRequiredFails(t *testing.T) {
	requiredKeys := []string{
		"JWT_PRIVATE_KEY",
		"JWT_PUBLIC_KEY",
		"POSTGRES_HOST",
		"POSTGRES_USER",
		"POSTGRES_PASSWORD",
		"POSTGRES_DB",
		"MONGO_URI",
	}

	for _, key := range requiredKeys {
		t.Run("missing_"+key, func(t *testing.T) {
			setMinimalValidEnv()
			unsetEnv(key)
			_, err := config.Load()
			assert.Error(t, err)
			assert.Contains(t, err.Error(), key)
		})
	}
}

func TestConfigInvalidTypes(t *testing.T) {
	t.Run("invalid_port", func(t *testing.T) {
		setMinimalValidEnv()
		setEnv("PORT", "not-a-number")
		_, err := config.Load()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid integer")
	})

	t.Run("invalid_bool", func(t *testing.T) {
		setMinimalValidEnv()
		setEnv("OVERLOAD_PRIORITY_SHEDDING_ENABLED", "not-a-bool")
		_, err := config.Load()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid boolean")
	})

	t.Run("invalid_float", func(t *testing.T) {
		setMinimalValidEnv()
		setEnv("CACHE_TTL_JITTER_RATIO", "invalid-float")
		_, err := config.Load()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid float")
	})

	t.Run("uint8_overflow", func(t *testing.T) {
		setMinimalValidEnv()
		setEnv("ARGON2_PARALLELISM", "256") // exceeds uint8 (0-255)
		_, err := config.Load()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid unsigned integer")
	})
}

func TestConfigDefaultsWhenOptionalUnset(t *testing.T) {
	setMinimalValidEnv()
	unsetEnv("POSTGRES_PORT")
	unsetEnv("REDIS_HOST")
	unsetEnv("REDIS_PORT")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 5432, cfg.Database.Port)
	assert.Equal(t, "localhost", cfg.Redis.Host)
	assert.Equal(t, 6379, cfg.Redis.Port)
}

func TestConfigSliceParsing(t *testing.T) {
	setMinimalValidEnv()
	setEnv("HTTP_ALLOWED_ORIGINS", "https://example.com, https://app.example.com")
	setEnv("HTTP_TRUSTED_PROXIES", "10.0.0.0/8, 172.16.0.1")
	setEnv("GEOIP_ALLOWED_COUNTRIES", "US, CA, GB")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"https://example.com", "https://app.example.com"}, cfg.HTTP.AllowedOrigins)
	assert.Equal(t, []string{"10.0.0.0/8", "172.16.0.1"}, cfg.HTTP.TrustedProxies)
	assert.Equal(t, []string{"US", "CA", "GB"}, cfg.Security.GeoIPAllowedCountries)
}

func TestConfigAppDebugRouteFlags(t *testing.T) {
	setMinimalValidEnv()
	setEnv("ENABLE_SWAGGER", "true")
	setEnv("ENABLE_GRAPHQL_PLAYGROUND", "true")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.True(t, cfg.App.EnableSwagger)
	assert.True(t, cfg.App.EnablePlayground)
}

func TestConfigSecurityTransportAndOriginDefaults(t *testing.T) {
	t.Run("development defaults postgres sslmode to disable", func(t *testing.T) {
		setMinimalValidEnv()
		unsetEnv("NODE_ENV")
		setEnv("APP_ENV", "development")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, "disable", cfg.Database.SSLMode)
		assert.Empty(t, cfg.HTTP.AllowedOrigins)
		assert.False(t, cfg.Redis.UseTLS)
		assert.False(t, cfg.Telemetry.OTelExporterInsecure)
	})

	t.Run("unset env defaults to development and sslmode disable", func(t *testing.T) {
		setMinimalValidEnv()
		unsetEnv("NODE_ENV")
		unsetEnv("APP_ENV")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, "development", cfg.App.Env)
		assert.Equal(t, "disable", cfg.Database.SSLMode)
		assert.Empty(t, cfg.HTTP.AllowedOrigins)
	})

	t.Run("production defaults postgres sslmode to require and origins empty", func(t *testing.T) {
		setMinimalValidEnv()
		unsetEnv("NODE_ENV")
		setEnv("APP_ENV", "production")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, "require", cfg.Database.SSLMode)
		assert.Empty(t, cfg.HTTP.AllowedOrigins)
	})

	t.Run("explicit postgres sslmode overrides env default", func(t *testing.T) {
		setMinimalValidEnv()
		setEnv("APP_ENV", "production")
		setEnv("POSTGRES_SSLMODE", "disable")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, "disable", cfg.Database.SSLMode)
	})

	t.Run("redis tls and otel insecure opt-in flags parse", func(t *testing.T) {
		setMinimalValidEnv()
		setEnv("REDIS_USE_TLS", "true")
		setEnv("OTEL_EXPORTER_OTLP_INSECURE", "true")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.True(t, cfg.Redis.UseTLS)
		assert.True(t, cfg.Telemetry.OTelExporterInsecure)
	})
}

func TestConfigCookieSecretOptional(t *testing.T) {
	setMinimalValidEnv()
	unsetEnv("COOKIE_SECRET")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.Auth.CookieSecret)
}
