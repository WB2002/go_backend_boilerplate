package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/graphql"
)

func TestDebugRouteExposureControl(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name               string
		appCfg             config.AppConfig
		expectSwagger      int
		expectPlayground   int
	}{
		{
			name:             "production defaults block swagger and playground",
			appCfg:           config.AppConfig{Env: "production"},
			expectSwagger:    http.StatusNotFound,
			expectPlayground: http.StatusNotFound,
		},
		{
			name:             "production case-insensitive blocks swagger and playground",
			appCfg:           config.AppConfig{Env: "Production"},
			expectSwagger:    http.StatusNotFound,
			expectPlayground: http.StatusNotFound,
		},
		{
			name:             "production with EnableSwagger only mounts swagger",
			appCfg:           config.AppConfig{Env: "production", EnableSwagger: true},
			expectSwagger:    http.StatusOK,
			expectPlayground: http.StatusNotFound,
		},
		{
			name:             "production with EnablePlayground only mounts playground",
			appCfg:           config.AppConfig{Env: "production", EnablePlayground: true},
			expectSwagger:    http.StatusNotFound,
			expectPlayground: http.StatusOK,
		},
		{
			name:             "production with both explicitly enabled mounts both",
			appCfg:           config.AppConfig{Env: "production", EnableSwagger: true, EnablePlayground: true},
			expectSwagger:    http.StatusOK,
			expectPlayground: http.StatusOK,
		},
		{
			name:             "development mounts both by default",
			appCfg:           config.AppConfig{Env: "development"},
			expectSwagger:    http.StatusOK,
			expectPlayground: http.StatusOK,
		},
		{
			name:             "staging mounts both by default",
			appCfg:           config.AppConfig{Env: "staging"},
			expectSwagger:    http.StatusOK,
			expectPlayground: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			registerSwagger(r, tc.appCfg)

			gqlHandler := graphql.NewHandler(
				config.GraphQLConfig{MaxDepth: 10, MaxComplexity: 50, UserBudget: 1000},
				nil,
				graphql.WithPlayground(shouldEnablePlayground(tc.appCfg)),
			)
			gqlHandler.RegisterRoutes(r)

			for _, path := range []string{"/swagger/index.html", "/swagger/doc.json"} {
				w := httptest.NewRecorder()
				req, _ := http.NewRequest(http.MethodGet, path, nil)
				req.RequestURI = path
				r.ServeHTTP(w, req)
				assert.Equal(t, tc.expectSwagger, w.Code, "path=%s", path)
			}

			for _, path := range []string{"/playground", "/graphiql"} {
				w := httptest.NewRecorder()
				req, _ := http.NewRequest(http.MethodGet, path, nil)
				r.ServeHTTP(w, req)
				assert.Equal(t, tc.expectPlayground, w.Code, "path=%s", path)
			}

			// GraphQL API endpoint itself remains mounted regardless of playground flag.
			wGQL := httptest.NewRecorder()
			reqGQL, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query": "{ health }"}`))
			reqGQL.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(wGQL, reqGQL)
			assert.Equal(t, http.StatusOK, wGQL.Code)
			assert.Contains(t, wGQL.Body.String(), `"health":"ok"`)
		})
	}
}
