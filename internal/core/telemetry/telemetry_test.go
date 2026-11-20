package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
	mongooptions "go.mongodb.org/mongo-driver/mongo/options"
	"go.opentelemetry.io/otel"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/telemetry"
)

func TestHealthCheckCacheMitigatesThunderingHerd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checker := telemetry.NewHealthChecker(telemetry.Config{
		CacheTTL: 2 * time.Second,
		Env:      "development",
	})

	router := gin.New()
	checker.RegisterRoutes(router)

	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodGet, "/health", nil)
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	var res1 telemetry.HealthResponse
	err := json.Unmarshal(w1.Body.Bytes(), &res1)
	require.NoError(t, err)
	assert.Equal(t, "UP", res1.Status)
	require.NotNil(t, res1.SystemStats)
	assert.Greater(t, res1.SystemStats.Goroutines, 0)

	// Immediate second call should return cached response with exact same timestamp
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, "/health", nil)
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)

	var res2 telemetry.HealthResponse
	err = json.Unmarshal(w2.Body.Bytes(), &res2)
	require.NoError(t, err)
	assert.Equal(t, res1.Timestamp.UnixNano(), res2.Timestamp.UnixNano())

	// Simulate transition to draining shutdown
	checker.SetDraining(true)
	assert.True(t, checker.IsDraining())

	// Readiness probe must now return 503
	wReady := httptest.NewRecorder()
	reqReady, _ := http.NewRequest(http.MethodGet, "/health/ready", nil)
	router.ServeHTTP(wReady, reqReady)
	assert.Equal(t, http.StatusServiceUnavailable, wReady.Code)

	var readyRes map[string]string
	err = json.Unmarshal(wReady.Body.Bytes(), &readyRes)
	require.NoError(t, err)
	assert.Equal(t, "DOWN", readyRes["status"])

	// Liveness probe continues returning 200
	wLive := httptest.NewRecorder()
	reqLive, _ := http.NewRequest(http.MethodGet, "/health/live", nil)
	router.ServeHTTP(wLive, reqLive)
	assert.Equal(t, http.StatusOK, wLive.Code)
}

func TestHealthCheckFunctionalOptions(t *testing.T) {
	checker := telemetry.NewHealthChecker(
		telemetry.Config{},
		telemetry.WithDB(nil),
		telemetry.WithRedis(nil),
		telemetry.WithMongo(nil),
	)
	assert.NotNil(t, checker)
}

func TestEMFEmitterDefaultOptions(t *testing.T) {
	emitter := telemetry.NewEMFEmitter(
		telemetry.WithEMFWriter(nil),
		telemetry.WithEMFNamespace(""),
		telemetry.WithEMFEnvironment(""),
		telemetry.WithEMFService(""),
		telemetry.WithEMFNow(nil),
	)
	assert.NotNil(t, emitter)
}

func TestHealthCheckFailedDependencies(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gormDB, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gormDB.DB()
	require.NoError(t, err)
	_ = sqlDB.Close()

	rdb := goredis.NewClient(&goredis.Options{
		Addr:        "127.0.0.1:54321",
		DialTimeout: 20 * time.Millisecond,
	})

	mongoClient, err := mongo.Connect(context.Background(), mongooptions.Client().ApplyURI("mongodb://127.0.0.1:54322/?connectTimeoutMS=20&serverSelectionTimeoutMS=20"))
	require.NoError(t, err)

	checker := telemetry.NewHealthChecker(telemetry.Config{
		CacheTTL: 10 * time.Millisecond,
		DB:       gormDB,
		Redis:    rdb,
		Mongo:    mongoClient,
	})

	router := gin.New()
	checker.RegisterRoutes(router)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/health", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var res telemetry.HealthResponse
	err = json.Unmarshal(w.Body.Bytes(), &res)
	require.NoError(t, err)
	assert.Equal(t, "DOWN", res.Status)
	assert.Equal(t, "DOWN", res.Services["postgres"].Status)
	assert.Equal(t, "DOWN", res.Services["redis"].Status)
	assert.Equal(t, "DOWN", res.Services["mongodb"].Status)
}

func TestInitTracerNoopAndEndpoint(t *testing.T) {
	// Test without endpoint (noop)
	shutdown, err := telemetry.InitTracer(context.Background(), config.TelemetryConfig{
		ServiceName:          "test-service",
		OTelExporterEndpoint: "",
	})
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	err = shutdown(context.Background())
	assert.NoError(t, err)

	// Test with endpoint set
	shutdownWithEndpoint, err := telemetry.InitTracer(context.Background(), config.TelemetryConfig{
		ServiceName:          "test-service",
		OTelExporterEndpoint: "localhost:4317",
	})
	require.NoError(t, err)
	require.NotNil(t, shutdownWithEndpoint)

	err = shutdownWithEndpoint(context.Background())
	assert.NoError(t, err)
}

func TestEMFEmitterFormat(t *testing.T) {
	var buf bytes.Buffer
	fixedTime := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	emitter := telemetry.NewEMFEmitter(
		telemetry.WithEMFWriter(&buf),
		telemetry.WithEMFNamespace("TestApp"),
		telemetry.WithEMFEnvironment("staging"),
		telemetry.WithEMFService("api-service"),
		telemetry.WithEMFNow(func() time.Time { return fixedTime }),
	)

	err := emitter.Emit(
		"CustomNamespace",
		map[string]string{
			"Environment": "staging",
			"Service":     "api-service",
		},
		map[string]float64{
			"Overload503Count": 1,
			"RequestDuration":  123.45,
		},
		map[string]string{
			"Overload503Count": "Count",
			"RequestDuration":  "Milliseconds",
		},
	)
	require.NoError(t, err)

	var doc map[string]interface{}
	err = json.Unmarshal(buf.Bytes(), &doc)
	require.NoError(t, err)

	// Verify top level dimensions and metrics
	assert.Equal(t, "staging", doc["Environment"])
	assert.Equal(t, "api-service", doc["Service"])
	assert.Equal(t, float64(1), doc["Overload503Count"])
	assert.Equal(t, 123.45, doc["RequestDuration"])

	// Verify _aws root
	awsVal, ok := doc["_aws"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(fixedTime.UnixMilli()), awsVal["Timestamp"])

	cwMetrics, ok := awsVal["CloudWatchMetrics"].([]interface{})
	require.True(t, ok)
	require.Len(t, cwMetrics, 1)

	metricDirective, ok := cwMetrics[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "CustomNamespace", metricDirective["Namespace"])

	// Check dimensions
	dims, ok := metricDirective["Dimensions"].([]interface{})
	require.True(t, ok)
	require.Len(t, dims, 1)

	// Check metrics
	metrics, ok := metricDirective["Metrics"].([]interface{})
	require.True(t, ok)
	assert.Len(t, metrics, 2)
}

func TestEMFEmitterHelperMethods(t *testing.T) {
	var buf bytes.Buffer
	emitter := telemetry.NewEMFEmitter(
		telemetry.WithEMFWriter(&buf),
		telemetry.WithEMFNamespace("AppNamespace"),
		telemetry.WithEMFEnvironment("production"),
		telemetry.WithEMFService("go_backend_boilerplate"),
	)

	err := emitter.RecordOverload503()
	require.NoError(t, err)

	var doc1 map[string]interface{}
	err = json.Unmarshal(buf.Bytes(), &doc1)
	require.NoError(t, err)
	assert.Equal(t, float64(1), doc1["Overload503Count"])
	assert.Equal(t, "production", doc1["Environment"])
	assert.Equal(t, "go_backend_boilerplate", doc1["Service"])

	buf.Reset()
	err = emitter.RecordLatency("DatabaseQueryLatency", 45*time.Millisecond)
	require.NoError(t, err)

	var doc2 map[string]interface{}
	err = json.Unmarshal(buf.Bytes(), &doc2)
	require.NoError(t, err)
	assert.Equal(t, float64(45), doc2["DatabaseQueryLatency"])

	buf.Reset()
	err = emitter.RecordLatency("SubMsQueryLatency", 500*time.Microsecond)
	require.NoError(t, err)

	var doc3 map[string]interface{}
	err = json.Unmarshal(buf.Bytes(), &doc3)
	require.NoError(t, err)
	assert.Equal(t, 0.5, doc3["SubMsQueryLatency"])
}

func TestHealthCheckConcurrentThunderingHerd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checker := telemetry.NewHealthChecker(telemetry.Config{
		CacheTTL: 50 * time.Millisecond,
	})

	router := gin.New()
	checker.RegisterRoutes(router)

	const concurrency = 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	errCh := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req, err := http.NewRequest(http.MethodGet, "/health", nil)
			if err != nil {
				errCh <- err
				return
			}
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				errCh <- fmt.Errorf("unexpected status code: %d, body: %s", w.Code, w.Body.String())
				return
			}

			var res telemetry.HealthResponse
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				errCh <- err
				return
			}

			if res.Status != "UP" {
				errCh <- fmt.Errorf("expected UP status, got %s", res.Status)
				return
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		assert.NoError(t, err)
	}
}

func TestEMFEmitterConcurrentEmissions(t *testing.T) {
	var buf bytes.Buffer
	emitter := telemetry.NewEMFEmitter(
		telemetry.WithEMFWriter(&buf),
		telemetry.WithEMFNamespace("TestApp"),
		telemetry.WithEMFEnvironment("production"),
		telemetry.WithEMFService("test-service"),
	)

	const concurrency = 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			_ = emitter.RecordLatency("Latency", time.Duration(idx)*time.Millisecond)
			_ = emitter.RecordOverload503()
			_ = emitter.Emit(
				"TestApp",
				map[string]string{
					"Worker": fmt.Sprintf("worker-%d", idx),
				},
				map[string]float64{
					"Metric": float64(idx),
				},
				nil,
			)
		}(i)
	}

	wg.Wait()
	assert.NotEmpty(t, buf.Bytes())
}

func TestEMFEmitterGuardAWSKey(t *testing.T) {
	var buf bytes.Buffer
	emitter := telemetry.NewEMFEmitter(
		telemetry.WithEMFWriter(&buf),
		telemetry.WithEMFNamespace("TestApp"),
	)

	err := emitter.Emit(
		"TestApp",
		map[string]string{
			"_aws": "overwritten",
			"Env":  "prod",
		},
		map[string]float64{
			"_aws":  999.0,
			"Count": 1.0,
		},
		nil,
	)
	require.NoError(t, err)

	var doc map[string]interface{}
	err = json.Unmarshal(buf.Bytes(), &doc)
	require.NoError(t, err)

	awsVal, ok := doc["_aws"].(map[string]interface{})
	require.True(t, ok, "_aws must remain the directive map")
	assert.NotNil(t, awsVal["CloudWatchMetrics"])
	assert.NotNil(t, awsVal["Timestamp"])
	assert.Equal(t, "prod", doc["Env"])
	assert.Equal(t, 1.0, doc["Count"])
}

func TestInitTracerXRayIDGenerator(t *testing.T) {
	shutdown, err := telemetry.InitTracer(context.Background(), config.TelemetryConfig{
		ServiceName: "xray-test",
	})
	require.NoError(t, err)
	defer func() {
		_ = shutdown(context.Background())
	}()

	tr := otel.Tracer("test")
	_, span := tr.Start(context.Background(), "test-span")
	defer span.End()

	sc := span.SpanContext()
	require.True(t, sc.HasTraceID())
	traceIDHex := sc.TraceID().String()
	require.Len(t, traceIDHex, 32)
}
