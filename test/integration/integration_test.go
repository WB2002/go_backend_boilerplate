//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
	mongooptions "go.mongodb.org/mongo-driver/mongo/options"

	"go_backend_boilerplate/internal/core/graphql"
	"go_backend_boilerplate/internal/core/middleware"
	"go_backend_boilerplate/internal/core/overload"
	"go_backend_boilerplate/internal/core/telemetry"
)

func TestHTTPStackIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	overloadMgr := overload.NewManager(overload.Config{
		MaxInFlight:             64,
		PrioritySheddingEnabled: true,
		RetryAfterSec:           5,
	})
	defer overloadMgr.Stop()

	router := gin.New()
	router.Use(middleware.MaxBodySize(1 << 20))
	router.Use(overloadMgr.Middleware())

	checker := telemetry.NewHealthChecker(telemetry.Config{Env: "production"})
	checker.RegisterRoutes(router)

	gqlHandler := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    1000,
	}, nil)
	gqlHandler.RegisterRoutes(router)

	// Verify GraphQL works through the full middleware stack
	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"{ health }"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	var gqlResp map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &gqlResp))
	assert.Contains(t, gqlResp, "data")
}

func TestExternalServicesConnectivityIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		db, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		defer db.Close()
		require.NoError(t, db.PingContext(ctx), "postgres must be reachable")
	}

	if redisAddr := os.Getenv("REDIS_ADDR"); redisAddr != "" {
		rdb := goredis.NewClient(&goredis.Options{Addr: redisAddr})
		defer rdb.Close()
		require.NoError(t, rdb.Ping(ctx).Err(), "redis must be reachable")
	}

	if mongoURI := os.Getenv("MONGO_URI"); mongoURI != "" {
		client, err := mongo.Connect(ctx, mongooptions.Client().ApplyURI(mongoURI))
		require.NoError(t, err)
		defer func() { _ = client.Disconnect(context.Background()) }()
		require.NoError(t, client.Ping(ctx, nil), "mongodb must be reachable")
	}
}
