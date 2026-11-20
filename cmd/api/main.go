package main

// @title Go Backend Boilerplate API
// @version 1.0
// @description Production-grade Go backend boilerplate API.
// @termsOfService https://example.com/terms/

// @contact.name API Support
// @contact.email support@example.com

// @license.name MIT
// @license.url https://opensource.org/licenses/MIT

// @host localhost:8080
// @BasePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Type "Bearer" followed by a space and JWT token.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/audit"
	"go_backend_boilerplate/internal/core/auth"
	"go_backend_boilerplate/internal/core/cache"
	"go_backend_boilerplate/internal/core/email"
	"go_backend_boilerplate/internal/core/featureflag"
	"go_backend_boilerplate/internal/core/graphql"
	"go_backend_boilerplate/internal/core/httpclient"
	"go_backend_boilerplate/internal/core/middleware"
	"go_backend_boilerplate/internal/core/notification"
	"go_backend_boilerplate/internal/core/overload"
	"go_backend_boilerplate/internal/core/ratelimit"
	"go_backend_boilerplate/internal/core/server"
	"go_backend_boilerplate/internal/core/telemetry"
	"go_backend_boilerplate/internal/core/websocket"
	"go_backend_boilerplate/internal/modules/account"
	"go_backend_boilerplate/internal/platform/database"
	"go_backend_boilerplate/internal/platform/logger"
	"go_backend_boilerplate/internal/platform/mongodb"
	"go_backend_boilerplate/internal/platform/redis"
)

func main() {
	// 1. Configuration
	cfg, err := config.Load()
	if err != nil {
		panic("failed to load configuration: " + err.Error())
	}

	// 2. Structured Logger
	log := logger.New(cfg.App.Env)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 3. OpenTelemetry Tracer
	otelShutdown, err := telemetry.InitTracer(ctx, cfg.Telemetry)
	if err != nil {
		log.Warn().Err(err).Msg("failed to initialize OpenTelemetry tracer")
	}
	defer func() {
		if otelShutdown != nil {
			_ = otelShutdown(context.Background())
		}
	}()

	// 4. Redis Client
	rdb := redis.NewClient(cfg.Redis)
	defer func() {
		_ = redis.Close(rdb)
	}()

	// 5. PostgreSQL Database (graceful offline handling in local/dev environments)
	var db *gorm.DB
	db, err = database.Connect(cfg.Database)
	if err != nil {
		log.Warn().Err(err).Msg("PostgreSQL database unavailable; proceeding with degraded database operations")
	} else {
		defer func() {
			_ = database.Close(db)
		}()
	}

	// 6. MongoDB Connection
	mongoClient, err := mongodb.Connect(cfg.Mongo)
	if err != nil {
		log.Warn().Err(err).Msg("MongoDB client init failed; proceeding with degraded mongo operations")
	} else {
		defer func() {
			_ = mongodb.Close(context.Background(), mongoClient)
		}()
	}

	// 7. Security & Authentication
	argonParams := auth.Argon2Params{
		Memory:      cfg.Auth.Argon2Memory,
		Iterations:  cfg.Auth.Argon2Iterations,
		Parallelism: cfg.Auth.Argon2Parallelism,
		SaltLength:  cfg.Auth.Argon2SaltLength,
		KeyLength:   cfg.Auth.Argon2KeyLength,
	}
	hasher := auth.NewArgon2idHasherWithParams(
		argonParams,
		cfg.Auth.Argon2MaxConcurrent,
		cfg.Auth.Argon2QueueDepth,
		time.Duration(cfg.Auth.Argon2QueueTimeoutMs)*time.Millisecond,
	)

	tokenManager, err := auth.NewRS256TokenManager(
		cfg.Auth.JWTPrivateKey,
		cfg.Auth.JWTPublicKey,
		time.Duration(cfg.Auth.AccessTokenTTLSec)*time.Second,
		time.Duration(cfg.Auth.RefreshTokenTTLSec)*time.Second,
		rdb,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize RS256 token manager")
	}

	// 8. Core Subsystems
	overloadMgr := overload.NewManager(overload.Config{
		MaxInFlight:             cfg.Overload.MaxInFlight,
		PrioritySheddingEnabled: cfg.Overload.PrioritySheddingEnabled,
		RetryAfterSec:           cfg.Overload.RetryAfterSec,
		LoadLevelQueueSize:      cfg.Overload.InboundLoadLevelMaxQueue,
		LoadLevelMaxWait:        time.Duration(cfg.Overload.InboundLoadLevelMaxWaitMs) * time.Millisecond,
	})
	defer func() {
		_ = overloadMgr.Close()
	}()

	var rateLimitStore ratelimit.Store = ratelimit.NewMemoryStore()
	if rdb != nil {
		rateLimitStore = ratelimit.NewRedisStore(rdb)
	}
	rateLimiter := ratelimit.New(rateLimitStore, ratelimit.Config{
		DefaultLimit:     cfg.RateLimit.DefaultLimit,
		DefaultWindow:    time.Duration(cfg.RateLimit.DefaultWindowSec) * time.Second,
		FallbackCooldown: time.Duration(cfg.RateLimit.FallbackCooldownMs) * time.Millisecond,
	})
	middleware.SetDefaultLimiter(rateLimiter)

	_ = cache.NewFromAppConfig(cfg.Cache, rdb)
	_ = httpclient.New()

	featureEngine := featureflag.NewEngine(cfg.FeatureFlag, rdb)
	defer func() {
		_ = featureEngine.Close()
	}()

	slackService := notification.NewSlackService(notification.Config{
		WebhookURL: cfg.Notification.SlackWebhookURL,
		Logger:     log,
	})
	defer func() {
		_ = slackService.Close(context.Background())
	}()

	wsHub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: cfg.WebSocket.MaxConnectionsPerUser,
		MessagesPerMinute:     cfg.WebSocket.MessagesPerMinute,
		AllowedOrigins:        cfg.HTTP.AllowedOrigins,
	}, rdb)
	go wsHub.Run(ctx)
	wsHandler := websocket.NewHandler(wsHub, tokenManager, cfg.HTTP.AllowedOrigins...)

	gqlHandler := graphql.NewHandler(cfg.GraphQL, rdb)

	var healthOpts []telemetry.Option
	if db != nil {
		healthOpts = append(healthOpts, telemetry.WithDB(db))
	}
	if rdb != nil {
		healthOpts = append(healthOpts, telemetry.WithRedis(rdb))
	}
	if mongoClient != nil {
		healthOpts = append(healthOpts, telemetry.WithMongo(mongoClient))
	}
	healthChecker := telemetry.NewHealthChecker(telemetry.Config{
		CacheTTL: time.Duration(cfg.Telemetry.HealthCheckCacheTTLMs) * time.Millisecond,
	}, healthOpts...)

	mailer := email.NewSMTPMailer(email.SMTPConfig{
		Host:     cfg.Email.SMTPHost,
		Port:     cfg.Email.SMTPPort,
		Username: cfg.Email.SMTPUsername,
		Password: cfg.Email.SMTPPassword,
		From:     cfg.Email.SMTPFrom,
	})

	auditService := audit.NewService(db, audit.Config{BufferSize: 1000})
	defer func() {
		_ = auditService.Close()
	}()

	accountRepo := account.NewRepository(db)
	accountHandler := account.NewHandlerWithAudit(accountRepo, hasher, tokenManager, slackService, mailer, auditService)

	// 9. Gin Engine with complete middleware stack
	if cfg.App.Env != "development" && cfg.App.Env != "test" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	if err := router.SetTrustedProxies(cfg.HTTP.TrustedProxies); err != nil {
		log.Fatal().Err(err).Msg("failed to configure trusted proxies")
	}

	router.Use(
		middleware.RequestID(),
		middleware.AuditContext(),
		middleware.StructuredLogger(log),
		middleware.Recovery(log),
		middleware.SecurityHeaders(),
		middleware.GzipCompression(cfg.HTTP.GzipMinSizeBytes),
		middleware.RequestDeadline(
			time.Duration(cfg.HTTP.RequestTimeoutMs)*time.Millisecond,
			time.Duration(cfg.HTTP.RequestTimeoutMaxMs)*time.Millisecond,
		),
		middleware.CORS(cfg.HTTP.AllowedOrigins),
		middleware.InboundOverload(overloadMgr),
		middleware.RateLimitWithLimiter(
			rateLimiter,
			ratelimit.IdentifierIP,
			cfg.RateLimit.DefaultLimit,
			time.Duration(cfg.RateLimit.DefaultWindowSec)*time.Second,
		),
		middleware.IdempotencyWithConfig(rdb, 10*time.Minute, []string{
			"/account/login",
			"/account/refresh",
			"/account/reset-password",
		}),
	)

	// 10. Route Registration
	healthChecker.RegisterRoutes(router)
	registerSwagger(router)

	authMiddleware := middleware.AuthMiddleware(tokenManager)
	accountHandler.RegisterRoutes(router, authMiddleware)

	wsHandler.RegisterRoutes(router, "/realtime")
	gqlHandler.RegisterRoutes(router)

	// 11. Server & Graceful ALB Connection Drain
	srv := server.New(cfg.HTTP, router)
	srv.RegisterDrainHook(func() {
		log.Info().Msg("Graceful shutdown initiated: setting health checker to draining state")
		healthChecker.SetDraining(true)
	})

	log.Info().
		Str("app", cfg.App.Name).
		Str("env", cfg.App.Env).
		Int("port", cfg.HTTP.Port).
		Msg("Starting API server")

	if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal().Err(err).Msg("Server terminated with error")
	}

	// Explicitly signal background contexts to terminate before deferred service cleanup
	cancel()

	log.Info().Msg("Server stopped gracefully")
}
