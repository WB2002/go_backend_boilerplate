package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
)

// Config aggregates all 15 configuration blocks plus base application settings.
type Config struct {
	App          AppConfig          `json:"app"`
	HTTP         HTTPConfig         `json:"http"`
	Auth         AuthConfig         `json:"auth"`
	Database     DatabaseConfig     `json:"database"`
	Redis        RedisConfig        `json:"redis"`
	Mongo        MongoConfig        `json:"mongo"`
	RateLimit    RateLimitConfig    `json:"rate_limit"`
	Overload     OverloadConfig     `json:"overload"`
	Cache        CacheConfig        `json:"cache"`
	Jobs         JobsConfig         `json:"jobs"`
	WebSocket    WebSocketConfig    `json:"websocket"`
	GraphQL      GraphQLConfig      `json:"graphql"`
	Security     SecurityConfig     `json:"security"`
	Telemetry    TelemetryConfig    `json:"telemetry"`
	FeatureFlag  FeatureFlagConfig  `json:"feature_flag"`
	Notification NotificationConfig `json:"notification"`
	Email        EmailConfig        `json:"email"`
}

// AppConfig contains base application environment metadata.
type AppConfig struct {
	Env              string `env:"NODE_ENV,APP_ENV" default:"development"`
	Name             string `env:"APP_NAME" default:"go_backend_boilerplate"`
	EnableSwagger    bool   `env:"ENABLE_SWAGGER" default:"false"`
	EnablePlayground bool   `env:"ENABLE_GRAPHQL_PLAYGROUND" default:"false"`
}

// HTTPConfig contains HTTP server, ALB keep-alive, and Gin runtime settings.
type HTTPConfig struct {
	Port                int      `env:"PORT" default:"8080"`
	KeepAliveTimeoutMs  int      `env:"HTTP_KEEP_ALIVE_TIMEOUT_MS" default:"65000"`
	RequestTimeoutMs    int      `env:"HTTP_REQUEST_TIMEOUT_MS" default:"30000"`
	RequestTimeoutMaxMs int      `env:"HTTP_REQUEST_TIMEOUT_MAX_MS" default:"60000"`
	ShutdownDrainMs     int      `env:"HTTP_SHUTDOWN_DRAIN_MS" default:"500"`
	ShutdownForceMs     int      `env:"HTTP_SHUTDOWN_FORCE_MS" default:"1000"`
	GzipMinSizeBytes    int      `env:"HTTP_GZIP_MIN_SIZE_BYTES" default:"1024"`
	AllowedOrigins      []string `env:"HTTP_ALLOWED_ORIGINS" default:""`
	TrustedProxies      []string `env:"HTTP_TRUSTED_PROXIES" default:""`
}

// AuthConfig contains JWT RS256 keys, cookie secrets, and Argon2id bulkhead parameters.
type AuthConfig struct {
	JWTPrivateKey        string `env:"JWT_PRIVATE_KEY" required:"true"`
	JWTPublicKey         string `env:"JWT_PUBLIC_KEY" required:"true"`
	CookieSecret         string `env:"COOKIE_SECRET"`
	AccessTokenTTLSec    int    `env:"JWT_ACCESS_TOKEN_TTL_SEC" default:"900"`
	RefreshTokenTTLSec   int    `env:"JWT_REFRESH_TOKEN_TTL_SEC" default:"604800"`
	Argon2Memory         uint32 `env:"ARGON2_MEMORY" default:"65536"`
	Argon2Iterations     uint32 `env:"ARGON2_ITERATIONS" default:"3"`
	Argon2Parallelism    uint8  `env:"ARGON2_PARALLELISM" default:"2"`
	Argon2SaltLength     uint32 `env:"ARGON2_SALT_LENGTH" default:"16"`
	Argon2KeyLength      uint32 `env:"ARGON2_KEY_LENGTH" default:"32"`
	Argon2MaxConcurrent  int    `env:"ARGON2_MAX_CONCURRENT" default:"2"`
	Argon2QueueDepth     int    `env:"ARGON2_QUEUE_DEPTH" default:"4"`
	Argon2QueueTimeoutMs int    `env:"ARGON2_QUEUE_TIMEOUT_MS" default:"2000"`
}

// DatabaseConfig contains PostgreSQL connection and GORM pool settings.
type DatabaseConfig struct {
	Host            string `env:"POSTGRES_HOST" required:"true"`
	Port            int    `env:"POSTGRES_PORT" default:"5432"`
	User            string `env:"POSTGRES_USER" required:"true"`
	Password        string `env:"POSTGRES_PASSWORD" required:"true"`
	DBName          string `env:"POSTGRES_DB" required:"true"`
	SSLMode         string `env:"POSTGRES_SSLMODE" default:"disable"`
	MaxOpenConns    int    `env:"POSTGRES_MAX_OPEN_CONNS" default:"25"`
	MaxIdleConns    int    `env:"POSTGRES_MAX_IDLE_CONNS" default:"5"`
	ConnMaxLifetime int    `env:"POSTGRES_CONN_MAX_LIFETIME_SEC" default:"300"`
}

// RedisConfig contains Redis connection and connection pool settings.
type RedisConfig struct {
	Host         string `env:"REDIS_HOST" default:"localhost"`
	Port         int    `env:"REDIS_PORT" default:"6379"`
	Password     string `env:"REDIS_PASSWORD" default:""`
	DB           int    `env:"REDIS_DB" default:"0"`
	PoolSize     int    `env:"REDIS_POOL_SIZE" default:"50"`
	MinIdleConns int    `env:"REDIS_MIN_IDLE_CONNS" default:"10"`
	UseTLS       bool   `env:"REDIS_USE_TLS" default:"false"`
}

// MongoConfig contains MongoDB connection and pool options.
type MongoConfig struct {
	URI         string `env:"MONGO_URI" required:"true"`
	TimeoutMs   int    `env:"MONGO_TIMEOUT_MS" default:"5000"`
	MaxPoolSize uint64 `env:"MONGO_MAX_POOL_SIZE" default:"50"`
	MinPoolSize uint64 `env:"MONGO_MIN_POOL_SIZE" default:"5"`
}

// RateLimitConfig contains rate limiting thresholds and fallback options.
type RateLimitConfig struct {
	DefaultLimit       int `env:"RATE_LIMIT_DEFAULT_LIMIT" default:"100"`
	DefaultWindowSec   int `env:"RATE_LIMIT_DEFAULT_WINDOW_SEC" default:"60"`
	FallbackCooldownMs int `env:"RATE_LIMIT_FALLBACK_COOLDOWN_MS" default:"5000"`
}

// OverloadConfig contains inbound concurrency bulkhead and load shedding thresholds.
type OverloadConfig struct {
	MaxInFlight               int  `env:"OVERLOAD_MAX_IN_FLIGHT" default:"1000"`
	PrioritySheddingEnabled   bool `env:"OVERLOAD_PRIORITY_SHEDDING_ENABLED" default:"true"`
	RetryAfterSec             int  `env:"OVERLOAD_RETRY_AFTER_SEC" default:"5"`
	InboundLoadLevelMaxQueue  int  `env:"INBOUND_LOAD_LEVEL_MAX_QUEUE" default:"0"`
	InboundLoadLevelMaxWaitMs int  `env:"INBOUND_LOAD_LEVEL_MAX_WAIT_MS" default:"0"`
}

// CacheConfig contains multi-tier cache TTLs and jitter settings.
type CacheConfig struct {
	TTLJitterRatio     float64 `env:"CACHE_TTL_JITTER_RATIO" default:"0.15"`
	DefaultSoftTTLSec  int     `env:"CACHE_DEFAULT_SOFT_TTL_SEC" default:"60"`
	DefaultHardTTLSec  int     `env:"CACHE_DEFAULT_HARD_TTL_SEC" default:"300"`
	NegativeSoftTTLSec int     `env:"CACHE_NEGATIVE_SOFT_TTL_SEC" default:"10"`
}

// JobsConfig contains background worker concurrency and lease settings.
type JobsConfig struct {
	WorkerConcurrency      int `env:"BACKGROUND_JOBS_WORKER_CONCURRENCY" default:"10"`
	LeaseDurationSec       int `env:"BACKGROUND_JOBS_LEASE_DURATION_SEC" default:"30"`
	LeaseReclaimIntervalMs int `env:"BACKGROUND_JOBS_LEASE_RECLAIM_INTERVAL_MS" default:"10000"`
}

// WebSocketConfig contains real-time WebSocket connection limits.
type WebSocketConfig struct {
	MaxConnectionsPerUser int `env:"WEBSOCKET_MAX_CONNECTIONS_PER_USER" default:"5"`
	MessagesPerMinute     int `env:"WEBSOCKET_MESSAGES_PER_MINUTE" default:"120"`
}

// GraphQLConfig contains GraphQL depth and complexity budget settings.
type GraphQLConfig struct {
	MaxDepth      int `env:"GQL_MAX_DEPTH" default:"10"`
	MaxComplexity int `env:"GQL_MAX_COMPLEXITY" default:"50"`
	UserBudget    int `env:"GQL_USER_BUDGET" default:"1000"`
}

// SecurityConfig contains GeoLite2, file upload limits, ClamAV, and captcha options.
type SecurityConfig struct {
	GeoIPDBPath           string   `env:"GEOIP_DB_PATH" default:"GeoLite2-Country.mmdb"`
	GeoIPAllowedCountries []string `env:"GEOIP_ALLOWED_COUNTRIES" default:""`
	UploadMaxFileSize     int64    `env:"UPLOAD_MAX_FILE_SIZE" default:"5242880"`
	UploadMaxTotalSize    int64    `env:"UPLOAD_MAX_TOTAL_SIZE" default:"20971520"`
	ClamAVAddr            string   `env:"CLAMAV_ADDR" default:"localhost:3310"`
	HCaptchaSecret        string   `env:"HCAPTCHA_SECRET" default:""`
}

// TelemetryConfig contains health check caching and OpenTelemetry exporter options.
type TelemetryConfig struct {
	HealthCheckCacheTTLMs int    `env:"HEALTH_CHECK_CACHE_TTL_MS" default:"2000"`
	OTelExporterEndpoint  string `env:"OTEL_EXPORTER_OTLP_ENDPOINT" default:""`
	OTelExporterInsecure  bool   `env:"OTEL_EXPORTER_OTLP_INSECURE" default:"false"`
	ServiceName           string `env:"OTEL_SERVICE_NAME" default:"go_backend_boilerplate"`
}

// FeatureFlagConfig contains dynamic feature flag synchronization settings.
type FeatureFlagConfig struct {
	ReconcileMs int `env:"FEATURE_FLAG_RECONCILE_MS" default:"45000"`
}

// NotificationConfig contains asynchronous webhook notification endpoints.
type NotificationConfig struct {
	SlackWebhookURL string `env:"SLACK_WEBHOOK_URL" default:""`
}

// EmailConfig contains SMTP connection parameters for transactional mail dispatch.
type EmailConfig struct {
	SMTPHost     string `env:"SMTP_HOST" default:"localhost"`
	SMTPPort     int    `env:"SMTP_PORT" default:"1025"`
	SMTPUsername string `env:"SMTP_USERNAME" default:""`
	SMTPPassword string `env:"SMTP_PASSWORD" default:""`
	SMTPFrom     string `env:"SMTP_FROM" default:"no-reply@example.com"`
}

// Load populates and validates Config from the system environment.
func Load() (*Config, error) {
	cfg := &Config{}
	val := reflect.ValueOf(cfg).Elem()
	if err := populateStruct(val); err != nil {
		return nil, err
	}
	applyEnvironmentDefaults(cfg)
	return cfg, nil
}

func applyEnvironmentDefaults(cfg *Config) {
	if rawSSL, ok := os.LookupEnv("POSTGRES_SSLMODE"); !ok || strings.TrimSpace(rawSSL) == "" {
		if strings.EqualFold(strings.TrimSpace(cfg.App.Env), "development") {
			cfg.Database.SSLMode = "disable"
		} else {
			cfg.Database.SSLMode = "require"
		}
	}
}

func populateStruct(val reflect.Value) error {
	t := val.Type()
	for i := 0; i < val.NumField(); i++ {
		fieldVal := val.Field(i)
		fieldType := t.Field(i)

		if fieldVal.Kind() == reflect.Struct {
			if err := populateStruct(fieldVal); err != nil {
				return err
			}
			continue
		}

		envKey := fieldType.Tag.Get("env")
		defaultVal := fieldType.Tag.Get("default")
		required := fieldType.Tag.Get("required") == "true"

		if envKey == "" {
			continue
		}

		rawVal := ""
		keys := strings.Split(envKey, ",")
		for _, key := range keys {
			key = strings.TrimSpace(key)
			if v, ok := os.LookupEnv(key); ok && v != "" {
				rawVal = v
				break
			}
		}

		if rawVal == "" {
			if defaultVal != "" {
				rawVal = defaultVal
			} else if required {
				return fmt.Errorf("missing required environment variable: %s", envKey)
			}
		}

		if rawVal == "" {
			continue
		}

		if err := setFieldValue(fieldVal, rawVal); err != nil {
			return fmt.Errorf("field %s (%s): %w", fieldType.Name, envKey, err)
		}
	}
	return nil
}

func setFieldValue(field reflect.Value, rawVal string) error {
	switch field.Kind() {
	case reflect.String:
		field.SetString(rawVal)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		intVal, err := strconv.ParseInt(rawVal, 10, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid integer value %q", rawVal)
		}
		field.SetInt(intVal)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		uintVal, err := strconv.ParseUint(rawVal, 10, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid unsigned integer value %q", rawVal)
		}
		field.SetUint(uintVal)
	case reflect.Float32, reflect.Float64:
		floatVal, err := strconv.ParseFloat(rawVal, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid float value %q", rawVal)
		}
		field.SetFloat(floatVal)
	case reflect.Bool:
		boolVal, err := strconv.ParseBool(rawVal)
		if err != nil {
			return fmt.Errorf("invalid boolean value %q", rawVal)
		}
		field.SetBool(boolVal)
	case reflect.Slice:
		if field.Type().Elem().Kind() == reflect.String {
			parts := strings.Split(rawVal, ",")
			slice := make([]string, 0, len(parts))
			for _, part := range parts {
				p := strings.TrimSpace(part)
				if p != "" {
					slice = append(slice, p)
				}
			}
			field.Set(reflect.ValueOf(slice))
		} else {
			return fmt.Errorf("unsupported slice type: %s", field.Type())
		}
	default:
		return fmt.Errorf("unsupported field kind: %s", field.Kind())
	}
	return nil
}
