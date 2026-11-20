package telemetry_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
	mongooptions "go.mongodb.org/mongo-driver/mongo/options"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"go_backend_boilerplate/internal/core/auth"
	"go_backend_boilerplate/internal/core/telemetry"
)

func newFailingHealthChecker(t *testing.T, env string) *telemetry.HealthChecker {
	t.Helper()

	gormDB, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gormDB.DB()
	require.NoError(t, err)
	_ = sqlDB.Close()

	rdb := goredis.NewClient(&goredis.Options{
		Addr:        "127.0.0.1:54321",
		DialTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() {
		_ = rdb.Close()
	})

	mongoClient, err := mongo.Connect(context.Background(), mongooptions.Client().ApplyURI("mongodb://127.0.0.1:54322/?connectTimeoutMS=20&serverSelectionTimeoutMS=20"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = mongoClient.Disconnect(context.Background())
	})

	return telemetry.NewHealthChecker(telemetry.Config{
		CacheTTL: 2 * time.Second,
		Env:      env,
		DB:       gormDB,
		Redis:    rdb,
		Mongo:    mongoClient,
	})
}

func TestHealth_RedactsInternalErrorsInProduction(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, env := range []string{"", "production", "staging"} {
		t.Run("env_"+env, func(t *testing.T) {
			checker := newFailingHealthChecker(t, env)
			router := gin.New()
			checker.RegisterRoutes(router)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/health", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusServiceUnavailable, w.Code)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
			assert.Equal(t, "DOWN", raw["status"])

			_, hasSystemStats := raw["system_stats"]
			assert.False(t, hasSystemStats, "public /health response must omit system_stats outside development")

			services, ok := raw["services"].(map[string]any)
			require.True(t, ok)
			for _, svcName := range []string{"postgres", "redis", "mongodb"} {
				svc, svcOK := services[svcName].(map[string]any)
				require.True(t, svcOK, "expected service %s", svcName)
				assert.Equal(t, "DOWN", svc["status"])
				_, hasErr := svc["error"]
				assert.False(t, hasErr, "service %s must omit raw error string", svcName)
			}
		})
	}
}

func TestHealth_IncludesDiagnosticsInDevelopment(t *testing.T) {
	gin.SetMode(gin.TestMode)

	checker := newFailingHealthChecker(t, "development")
	router := gin.New()
	checker.RegisterRoutes(router)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/health", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	assert.Equal(t, "DOWN", raw["status"])
	assert.Contains(t, raw, "system_stats")

	services, ok := raw["services"].(map[string]any)
	require.True(t, ok)
	for _, svcName := range []string{"postgres", "redis", "mongodb"} {
		svc, svcOK := services[svcName].(map[string]any)
		require.True(t, svcOK, "expected service %s", svcName)
		assert.Equal(t, "DOWN", svc["status"])
		assert.NotEmpty(t, svc["error"])
	}
}

func TestHealth_CacheDoesNotLeakDiagnosticsAcrossAuthBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)

	checker := newFailingHealthChecker(t, "production")
	router := gin.New()
	router.GET("/admin/health", func(c *gin.Context) {
		c.Set("user", &auth.Claims{UserID: "admin-1"})
		checker.HandleHealth(c)
	})
	checker.RegisterRoutes(router)

	// 1. Authenticated call populates cache
	wAuth := httptest.NewRecorder()
	reqAuth, _ := http.NewRequest(http.MethodGet, "/admin/health", nil)
	router.ServeHTTP(wAuth, reqAuth)
	assert.Equal(t, http.StatusServiceUnavailable, wAuth.Code)

	var authBody map[string]any
	require.NoError(t, json.Unmarshal(wAuth.Body.Bytes(), &authBody))
	assert.Contains(t, authBody, "system_stats")

	// 2. Subsequent unauthenticated call hits cache and must still be redacted
	wPublic := httptest.NewRecorder()
	reqPublic, _ := http.NewRequest(http.MethodGet, "/health", nil)
	router.ServeHTTP(wPublic, reqPublic)
	assert.Equal(t, http.StatusServiceUnavailable, wPublic.Code)

	var publicBody map[string]any
	require.NoError(t, json.Unmarshal(wPublic.Body.Bytes(), &publicBody))
	assert.NotContains(t, publicBody, "system_stats")
	services := publicBody["services"].(map[string]any)
	for _, svcName := range []string{"postgres", "redis", "mongodb"} {
		svc := services[svcName].(map[string]any)
		assert.NotContains(t, svc, "error")
	}
}
