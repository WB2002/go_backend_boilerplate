package graphql_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/core/auth"
	"go_backend_boilerplate/internal/core/graphql"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestGraphQLQueryDepthLimiting(t *testing.T) {
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      3,
		MaxComplexity: 50,
		UserBudget:    1000,
	}, nil)

	r := gin.New()
	h.RegisterRoutes(r)

	// Query with depth 5 (or 6) exceeds max depth 3
	deepQuery := `{"query": "{ categories { subcategories { categories { subcategories { categories { id } } } } } }"}`

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(deepQuery))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code) // GraphQL specs return 200 with errors array
	assert.Contains(t, w.Body.String(), "query exceeds maximum depth")

	// Shallow query within depth 3 should succeed
	shallowQuery := `{"query": "{ categories { id name } }"}`
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(shallowQuery))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	assert.NotContains(t, w2.Body.String(), "query exceeds maximum depth")
	assert.Contains(t, w2.Body.String(), "data")
}

func TestGraphQLQueryComplexityLimiting(t *testing.T) {
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 5,
		UserBudget:    1000,
	}, nil)

	r := gin.New()
	h.RegisterRoutes(r)

	// Complex query requesting multiple fields and nested fields exceeding MaxComplexity 5
	complexQuery := `{"query": "{ categories { id name subcategories { id name subcategories { id } } } }"}`

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(complexQuery))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "query exceeds maximum complexity")

	// Simple query with complexity <= 5 should succeed
	simpleQuery := `{"query": "{ health }"}`
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(simpleQuery))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	assert.NotContains(t, w2.Body.String(), "query exceeds maximum complexity")
	assert.Contains(t, w2.Body.String(), "ok")
}

func TestGraphQLUserBudgetLimit(t *testing.T) {
	// Budget is 5. Each health query has complexity 1.
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    5,
	}, nil)

	r := gin.New()
	h.RegisterRoutes(r)

	query := `{"query": "{ health }"}`

	// Execute 5 queries successfully (total complexity = 5)
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.168.1.100:1234"
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "ok")
	}

	// 6th query exceeds the cumulative budget of 5
	wBlocked := httptest.NewRecorder()
	reqBlocked, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
	reqBlocked.Header.Set("Content-Type", "application/json")
	reqBlocked.RemoteAddr = "192.168.1.100:1234"
	r.ServeHTTP(wBlocked, reqBlocked)

	assert.Equal(t, http.StatusOK, wBlocked.Code)
	assert.Contains(t, wBlocked.Body.String(), "budget")

	// Different client IP has its own budget and should still succeed
	wOther := httptest.NewRecorder()
	reqOther, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
	reqOther.Header.Set("Content-Type", "application/json")
	reqOther.RemoteAddr = "192.168.1.200:1234"
	r.ServeHTTP(wOther, reqOther)

	assert.Equal(t, http.StatusOK, wOther.Code)
	assert.Contains(t, wOther.Body.String(), "ok")
}

func TestGraphQLUserIdentityExtraction(t *testing.T) {
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    2,
	}, nil)

	r := gin.New()
	// Middleware setting auth.Claims in context
	r.Use(func(c *gin.Context) {
		if token := c.GetHeader("X-Test-User"); token != "" {
			c.Set("user", &auth.Claims{UserID: token})
		}
		c.Next()
	})
	h.RegisterRoutes(r)

	query := `{"query": "{ health }"}`

	// User "user-1" makes 2 requests
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-User", "user-1")
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "ok")
	}

	// 3rd request for user-1 should be rejected by budget
	wBlocked := httptest.NewRecorder()
	reqBlocked, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
	reqBlocked.Header.Set("Content-Type", "application/json")
	reqBlocked.Header.Set("X-Test-User", "user-1")
	r.ServeHTTP(wBlocked, reqBlocked)
	assert.Equal(t, http.StatusOK, wBlocked.Code)
	assert.Contains(t, wBlocked.Body.String(), "budget")

	// User "user-2" should succeed
	wUser2 := httptest.NewRecorder()
	reqUser2, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
	reqUser2.Header.Set("Content-Type", "application/json")
	reqUser2.Header.Set("X-Test-User", "user-2")
	r.ServeHTTP(wUser2, reqUser2)
	assert.Equal(t, http.StatusOK, wUser2.Code)
	assert.Contains(t, wUser2.Body.String(), "ok")
}

// -------------------------------------------------------------------------
// Redis-backed Budget Mock & Tests
// -------------------------------------------------------------------------

type mockRedisClient struct {
	goredis.Cmdable
	t     *testing.T
	mu    sync.Mutex
	store map[string]int64
	exp   map[string]time.Duration
}

func newMockRedisClient(t *testing.T) *mockRedisClient {
	return &mockRedisClient{
		t:     t,
		store: make(map[string]int64),
		exp:   make(map[string]time.Duration),
	}
}

func (m *mockRedisClient) EvalSha(ctx context.Context, sha string, keys []string, args ...any) *goredis.Cmd {
	return m.Eval(ctx, "", keys, args...)
}

func (m *mockRedisClient) Eval(ctx context.Context, script string, keys []string, args ...any) *goredis.Cmd {
	m.mu.Lock()
	defer m.mu.Unlock()

	cmd := goredis.NewCmd(ctx)
	if len(keys) == 0 || len(args) < 3 {
		cmd.SetErr(errors.New("invalid arguments to Eval"))
		return cmd
	}

	key := keys[0]
	cost := toInt64Test(args[0])
	budget := toInt64Test(args[1])
	ttl := toInt64Test(args[2])

	val := m.store[key]
	if val+cost > budget {
		cmd.SetVal([]interface{}{int64(0), val})
		return cmd
	}

	newVal := val + cost
	m.store[key] = newVal
	if val == 0 {
		m.exp[key] = time.Duration(ttl) * time.Second
	}

	cmd.SetVal([]interface{}{int64(1), newVal})
	return cmd
}

func toInt64Test(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		var i int64
		_, _ = fmt.Sscanf(n, "%d", &i)
		return i
	default:
		return 0
	}
}

func TestGraphQLRedisBudgetTracking(t *testing.T) {
	mockRedis := newMockRedisClient(t)

	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    2,
	}, mockRedis)

	r := gin.New()
	h.RegisterRoutes(r)

	query := `{"query": "{ health }"}`

	// 1st request -> within budget of 2
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
	req1.Header.Set("Content-Type", "application/json")
	req1.RemoteAddr = "10.0.0.1:5000"
	r.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)
	assert.Contains(t, w1.Body.String(), "ok")

	// Verify key usage was recorded atomically and TTL set
	var recordedKey string
	mockRedis.mu.Lock()
	for k := range mockRedis.store {
		if strings.HasPrefix(k, "gql:budget:10.0.0.1:") {
			recordedKey = k
			break
		}
	}
	mockRedis.mu.Unlock()
	require.NotEmpty(t, recordedKey)
	assert.Equal(t, int64(1), mockRedis.store[recordedKey])
	assert.Equal(t, 120*time.Second, mockRedis.exp[recordedKey])

	// 2nd request -> within budget of 2
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
	req2.Header.Set("Content-Type", "application/json")
	req2.RemoteAddr = "10.0.0.1:5000"
	r.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, int64(2), mockRedis.store[recordedKey])

	// 3rd request -> exceeds budget of 2
	w3 := httptest.NewRecorder()
	req3, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
	req3.Header.Set("Content-Type", "application/json")
	req3.RemoteAddr = "10.0.0.1:5000"
	r.ServeHTTP(w3, req3)
	assert.Equal(t, http.StatusOK, w3.Code)
	assert.Contains(t, w3.Body.String(), "budget")
	assert.Equal(t, int64(2), mockRedis.store[recordedKey])
}

func TestGraphQLRedisBudgetTrackingConcurrent(t *testing.T) {
	mockRedis := newMockRedisClient(t)

	// UserBudget is 10. Cost of health is 1.
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    10,
	}, mockRedis)

	r := gin.New()
	h.RegisterRoutes(r)

	query := `{"query": "{ health }"}`
	concurrency := 20
	var wg sync.WaitGroup
	var successCount int64
	var rejectedCount int64

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "10.0.0.2:1234"
			r.ServeHTTP(w, req)

			if strings.Contains(w.Body.String(), `"data":{"health":"ok"}`) {
				atomic.AddInt64(&successCount, 1)
			} else if strings.Contains(w.Body.String(), "exceeds user complexity budget") {
				atomic.AddInt64(&rejectedCount, 1)
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, int64(10), successCount, "atomic script must allow exactly 10 requests")
	assert.Equal(t, int64(10), rejectedCount, "atomic script must reject exactly 10 requests")

	// Final stored value must be exactly 10
	var recordedKey string
	mockRedis.mu.Lock()
	for k := range mockRedis.store {
		if strings.HasPrefix(k, "gql:budget:10.0.0.2:") {
			recordedKey = k
			break
		}
	}
	mockRedis.mu.Unlock()
	require.NotEmpty(t, recordedKey)
	assert.Equal(t, int64(10), mockRedis.store[recordedKey])
}

func TestGraphQLRedisFallbackOnFailure(t *testing.T) {
	// Point to unreachable client to trigger Redis error fallback
	unreachableClient := goredis.NewClient(&goredis.Options{
		Addr:        "127.0.0.1:9999",
		DialTimeout: 50 * time.Millisecond,
	})
	defer unreachableClient.Close()

	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    1000,
	}, unreachableClient)

	r := gin.New()
	h.RegisterRoutes(r)

	// Should still succeed via in-memory fallback
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query": "{ health }"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}

func TestGraphQLPlaygroundEndpoint(t *testing.T) {
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    1000,
	}, nil)

	r := gin.New()
	h.RegisterRoutes(r)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/playground", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, w.Body.String(), "GraphQL")
}

func TestGraphQLSuccessfulQueryExecution(t *testing.T) {
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    1000,
	}, nil)

	r := gin.New()
	h.RegisterRoutes(r)

	// Health query
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query": "{ health }"}`))
	req1.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w1, req1)

	assert.Equal(t, http.StatusOK, w1.Code)
	assert.JSONEq(t, `{"data":{"health":"ok"}}`, w1.Body.String())

	// Categories query
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query": "{ categories { id name } }"}`))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Contains(t, w2.Body.String(), `"data"`)
	assert.Contains(t, w2.Body.String(), `"categories"`)

	// User query with argument
	w3 := httptest.NewRecorder()
	req3, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query": "{ user(id: \"usr-42\") { id name email } }"}`))
	req3.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w3, req3)

	assert.Equal(t, http.StatusOK, w3.Code)
	assert.Contains(t, w3.Body.String(), `"usr-42"`)
}

func TestGraphQLCustomFieldWeights(t *testing.T) {
	customWeights := map[string]int{
		"health": 15,
	}

	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 10,
		UserBudget:    1000,
	}, nil, graphql.WithFieldWeights(customWeights))

	r := gin.New()
	h.RegisterRoutes(r)

	// Health has custom weight 15, which exceeds MaxComplexity 10
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query": "{ health }"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "query exceeds maximum complexity")
}

func TestGraphQLInvalidQueries(t *testing.T) {
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    1000,
	}, nil)

	r := gin.New()
	h.RegisterRoutes(r)

	// Empty query body
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(``))
	req1.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusBadRequest, w1.Code)

	// Syntax error in query
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query": "{ invalidSyntax {{{"}`))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Contains(t, w2.Body.String(), "errors")

	// Missing query string
	w3 := httptest.NewRecorder()
	req3, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"operationName": "Test"}`))
	req3.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w3, req3)
	assert.Equal(t, http.StatusBadRequest, w3.Code)

	// GET query execution
	w4 := httptest.NewRecorder()
	req4, _ := http.NewRequest(http.MethodGet, "/graphql?query={health}", nil)
	r.ServeHTTP(w4, req4)
	assert.Equal(t, http.StatusOK, w4.Code)
	require.JSONEq(t, `{"data":{"health":"ok"}}`, w4.Body.String())
}

func TestGraphQLRequestBodySizeLimit(t *testing.T) {
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    1000,
	}, nil)

	r := gin.New()
	h.RegisterRoutes(r)

	// Create payload larger than 2MB (2.5MB payload)
	largePadding := strings.Repeat("a", 2500*1024)
	oversizedBody := fmt.Sprintf(`{"query": "{ health }", "variables": {"large": "%s"}}`, largePadding)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(oversizedBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Contains(t, w.Body.String(), "request body exceeds maximum allowed size")
}

func TestGraphQLMultipleOperationsWithoutOperationName(t *testing.T) {
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    1000,
	}, nil)

	r := gin.New()
	h.RegisterRoutes(r)

	// Multiple operations without operationName -> rejected per GraphQL spec
	query := `{"query": "query Op1 { health } query Op2 { health }"}`
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
	req1.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w1, req1)

	assert.Equal(t, http.StatusOK, w1.Code)
	assert.Contains(t, w1.Body.String(), "Must provide operation name if query contains multiple operations.")

	// Multiple operations with valid operationName -> allowed
	queryWithOp := `{"query": "query Op1 { health } query Op2 { health }", "operationName": "Op1"}`
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(queryWithOp))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	assert.NotContains(t, w2.Body.String(), "Must provide operation name")
	assert.Contains(t, w2.Body.String(), `"data":{"health":"ok"}`)
}

func TestGraphQLCustomFieldWeightZero(t *testing.T) {
	// Setting custom weight 0 for "id"
	customWeights := map[string]int{
		"id": 0,
	}

	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 1, // Max complexity 1: categories is 1, id is 0 -> total 1 <= 1 (allowed)
		UserBudget:    1000,
	}, nil, graphql.WithFieldWeights(customWeights))

	r := gin.New()
	h.RegisterRoutes(r)

	// categories has defaultNestedWeight=1. id has customWeight=0.
	// Total complexity = 1 + 0 = 1 <= 1 -> should succeed!
	// If id weight 0 was ignored, complexity would be 1 + 1 = 2 > 1 -> would fail.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query": "{ categories { id } }"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "query exceeds maximum complexity")
	assert.Contains(t, w.Body.String(), `"data"`)
}

func TestGraphQLAuthorizationHeaderNotUsedAsIdentity(t *testing.T) {
	// Budget is 2 requests for client IP
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    2,
	}, nil)

	r := gin.New()
	h.RegisterRoutes(r)

	query := `{"query": "{ health }"}`

	// 1st request with Authorization header from 10.0.0.5
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer fake-token-1")
	req1.RemoteAddr = "10.0.0.5:1234"
	r.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)
	assert.Contains(t, w1.Body.String(), "ok")

	// 2nd request with Authorization header from 10.0.0.5
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer fake-token-1")
	req2.RemoteAddr = "10.0.0.5:1234"
	r.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Contains(t, w2.Body.String(), "ok")

	// 3rd request from same client IP, attempting to bypass budget with a spoofed Authorization header
	w3 := httptest.NewRecorder()
	req3, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(query))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("Authorization", "Bearer fake-token-2-spoofed")
	req3.RemoteAddr = "10.0.0.5:1234"
	r.ServeHTTP(w3, req3)

	// Must be rejected because budget is tied to client IP, not unverified Authorization header
	assert.Equal(t, http.StatusOK, w3.Code)
	assert.Contains(t, w3.Body.String(), "budget")
}
