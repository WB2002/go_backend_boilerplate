package graphql_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"go_backend_boilerplate/internal/core/graphql"
)

const testSchemaSource = `
type Query {
  health: String!
  categories: [Category!]!
  user(id: ID!): User
}

type Category {
  id: ID!
  name: String!
  subcategories: [Category!]!
  categories: [Category!]!
}

type User {
  id: ID!
  name: String!
  email: String!
  category: Category
}
`

func parseTestSchema(t *testing.T) *ast.Schema {
	t.Helper()
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphqls", Input: testSchemaSource})
	require.NoError(t, err)
	return schema
}

func parseTestQuery(t *testing.T, _ *ast.Schema, query string) *ast.QueryDocument {
	t.Helper()
	doc, err := parser.ParseQuery(&ast.Source{Name: "query.graphql", Input: query})
	require.NoError(t, err)
	return doc
}

func buildExponentialFragmentDAG(levels int, leafBody string) string {
	var b strings.Builder
	b.WriteString("query Bomb { ...F1 }\n")
	for i := 1; i < levels; i++ {
		fmt.Fprintf(&b, "fragment F%d on Query { ...F%d ...F%d }\n", i, i+1, i+1)
	}
	fmt.Fprintf(&b, "fragment F%d on Query { %s }\n", levels, leafBody)
	return b.String()
}

func TestComplexity_ExponentialFragmentDAG(t *testing.T) {
	query := buildExponentialFragmentDAG(25, `user(id: "1") { id }`)
	schema := parseTestSchema(t)
	doc := parseTestQuery(t, schema, query)

	engine := graphql.NewComplexityEngine(graphql.ComplexityConfig{MaxComplexity: 100})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := engine.CalculateComplexity(ctx, doc, "")
	duration := time.Since(start)

	assert.Less(t, duration, 50*time.Millisecond)
	require.Error(t, err)
	assert.ErrorIs(t, err, graphql.ErrComplexityLimitExceeded)
}

func TestComplexity_IntegerOverflowPrevented(t *testing.T) {
	// 70 levels would overflow 64-bit signed int (2^69) if memoized without saturation/limit checks.
	query := buildExponentialFragmentDAG(70, `health`)
	schema := parseTestSchema(t)
	doc := parseTestQuery(t, schema, query)

	engine := graphql.NewComplexityEngine(graphql.ComplexityConfig{MaxComplexity: 100})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := engine.CalculateComplexity(ctx, doc, "")
	duration := time.Since(start)

	assert.Less(t, duration, 50*time.Millisecond)
	require.Error(t, err)
	assert.ErrorIs(t, err, graphql.ErrComplexityLimitExceeded)
}

func TestDepth_ExponentialFragmentDAG(t *testing.T) {
	query := buildExponentialFragmentDAG(25, `user(id: "1") { id }`)
	schema := parseTestSchema(t)
	doc := parseTestQuery(t, schema, query)

	engine := graphql.NewDepthEngine(10)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	depth, err := engine.CalculateDepth(ctx, doc, "")
	duration := time.Since(start)

	assert.Less(t, duration, 50*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, 2, depth)
}

func TestComplexity_VisitCapExceeded(t *testing.T) {
	var b strings.Builder
	b.WriteString("query ManyNodes { ")
	for i := 0; i < 5005; i++ {
		fmt.Fprintf(&b, "f%d: health ", i)
	}
	b.WriteString("}")

	schema := parseTestSchema(t)
	doc := parseTestQuery(t, schema, b.String())

	// Weight 0 ensures complexity score itself doesn't trigger before AST visit cap.
	engine := graphql.NewComplexityEngine(graphql.ComplexityConfig{
		MaxComplexity: 100000,
		FieldWeights:  map[string]int{"health": 0},
	})

	_, err := engine.CalculateComplexity(context.Background(), doc, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, graphql.ErrComplexityLimitExceeded)
}

func TestDepth_VisitCapExceeded(t *testing.T) {
	var b strings.Builder
	b.WriteString("query ManyNodes { ")
	for i := 0; i < 5005; i++ {
		fmt.Fprintf(&b, "f%d: health ", i)
	}
	b.WriteString("}")

	schema := parseTestSchema(t)
	doc := parseTestQuery(t, schema, b.String())

	engine := graphql.NewDepthEngine(10)
	_, err := engine.CalculateDepth(context.Background(), doc, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, graphql.ErrDepthLimitExceeded)
}

func TestComplexity_ContextCancelled(t *testing.T) {
	query := `query Simple { user(id: "1") { id name } }`
	schema := parseTestSchema(t)
	doc := parseTestQuery(t, schema, query)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	compEngine := graphql.NewComplexityEngine(graphql.ComplexityConfig{MaxComplexity: 50})
	_, err := compEngine.CalculateComplexity(ctx, doc, "")
	assert.ErrorIs(t, err, context.Canceled)

	depthEngine := graphql.NewDepthEngine(10)
	_, err = depthEngine.CalculateDepth(ctx, doc, "")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestComplexity_SharedFragmentMemoizationAccuracy(t *testing.T) {
	query := `
	query Shared {
		...F1
		...F1
	}
	fragment F1 on Query {
		user(id: "1") {
			id
			name
		}
	}`
	schema := parseTestSchema(t)
	doc := parseTestQuery(t, schema, query)

	// Each F1 has complexity 3 (user=1, id=1, name=1). Spread twice -> 6.
	engine := graphql.NewComplexityEngine(graphql.ComplexityConfig{MaxComplexity: 10})
	complexity, err := engine.CalculateComplexity(context.Background(), doc, "")
	require.NoError(t, err)
	assert.Equal(t, 6, complexity)
}

func TestGraphQLHandler_ExponentialFragmentDAGRejectedFast(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := graphql.NewHandler(graphql.Config{
		MaxDepth:      10,
		MaxComplexity: 100,
		UserBudget:    1000,
	}, nil)

	r := gin.New()
	h.RegisterRoutes(r)

	query := buildExponentialFragmentDAG(25, `user(id: "1") { id }`)
	body, err := json.Marshal(map[string]string{"query": query})
	require.NoError(t, err)

	start := time.Now()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/graphql", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	duration := time.Since(start)

	assert.Less(t, duration, 50*time.Millisecond)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "query exceeds maximum complexity")
}
