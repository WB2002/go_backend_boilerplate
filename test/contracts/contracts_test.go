package contracts_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"go_backend_boilerplate/internal/core/graphql"
	"go_backend_boilerplate/internal/core/telemetry"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return root
}

func TestOpenAPISwaggerContract(t *testing.T) {
	specPath := filepath.Join(repoRoot(t), "docs", "swagger", "swagger.json")
	rawBytes, err := os.ReadFile(specPath)
	require.NoError(t, err, "swagger.json must exist")

	var spec map[string]any
	require.NoError(t, json.Unmarshal(rawBytes, &spec), "swagger.json must be valid JSON")

	assert.NotEmpty(t, spec["swagger"], "swagger version field must be present")
	info, ok := spec["info"].(map[string]any)
	require.True(t, ok, "info section must be present")
	assert.NotEmpty(t, info["title"], "API title must be present")
	assert.NotEmpty(t, info["version"], "API version must be present")

	paths, ok := spec["paths"].(map[string]any)
	require.True(t, ok, "paths section must be present")
	assert.NotEmpty(t, paths, "swagger spec must define API paths")
}

func TestGraphQLSchemaContract(t *testing.T) {
	schemaPath := filepath.Join(repoRoot(t), "internal", "core", "graphql", "schema.graphqls")
	rawSchema, err := os.ReadFile(schemaPath)
	require.NoError(t, err, "schema.graphqls must exist")

	schema, gqlErr := gqlparser.LoadSchema(&ast.Source{
		Name:  "schema.graphqls",
		Input: string(rawSchema),
	})
	require.Nil(t, gqlErr, "GraphQL schema must parse cleanly")
	require.NotNil(t, schema.Query, "Query root type must exist")

	assert.NotNil(t, schema.Query.Fields.ForName("health"), "Query.health must exist")
	assert.NotNil(t, schema.Query.Fields.ForName("categories"), "Query.categories must exist")
	assert.NotNil(t, schema.Query.Fields.ForName("user"), "Query.user must exist")
}

func TestGraphQLCategoriesQueryContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 50,
		UserBudget:    1000,
	}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"{ health categories { id name } }"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)

	var payload struct {
		Data struct {
			Health     string `json:"health"`
			Categories []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"categories"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.Empty(t, payload.Errors)
	assert.Equal(t, "ok", payload.Data.Health)
	assert.NotEmpty(t, payload.Data.Categories)
}

func TestHealthEndpointContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checker := telemetry.NewHealthChecker(telemetry.Config{
		Env: "production",
	})

	router := gin.New()
	checker.RegisterRoutes(router)

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/health", nil)
	require.NoError(t, err)
	router.ServeHTTP(recorder, req)

	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.Contains(t, []string{"UP", "DOWN"}, body["status"])
	_, hasSystemStats := body["system_stats"]
	assert.False(t, hasSystemStats, "production /health contract must omit system_stats")
}
