package graphql

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/auth"
)

// Config is an alias for the centralized GraphQLConfig.
type Config = config.GraphQLConfig

//go:embed schema.graphqls
var defaultSchemaSource string

// RequestPayload represents an incoming GraphQL request.
type RequestPayload struct {
	Query         string                 `json:"query"`
	OperationName string                 `json:"operationName,omitempty"`
	Variables     map[string]interface{} `json:"variables,omitempty"`
}

// ResolverFunc defines custom query resolver logic.
type ResolverFunc func(ctx context.Context, args map[string]interface{}) (interface{}, error)

// Handler serves GraphQL execution and interactive playground IDE endpoints.
type Handler struct {
	cfg              Config
	enablePlayground bool
	schema           *ast.Schema
	depthEngine      *DepthEngine
	complexEngine    *ComplexityEngine
	budgetTracker    *BudgetTracker
	resolvers        map[string]ResolverFunc
}

// HandlerOption configures custom Handler behavior.
type HandlerOption func(*Handler)

// WithCustomSchema overrides the embedded default GraphQL schema.
func WithCustomSchema(schemaStr string) HandlerOption {
	return func(h *Handler) {
		s, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphqls", Input: schemaStr})
		if err == nil {
			h.schema = s
		}
	}
}

// WithFieldWeights sets custom field complexity weights.
func WithFieldWeights(weights map[string]int) HandlerOption {
	return func(h *Handler) {
		h.complexEngine = NewComplexityEngine(ComplexityConfig{
			MaxComplexity: h.cfg.MaxComplexity,
			FieldWeights:  weights,
		})
	}
}

// WithPlayground enables or disables mounting the GraphQL Playground and GraphiQL routes.
func WithPlayground(enabled bool) HandlerOption {
	return func(h *Handler) {
		h.enablePlayground = enabled
	}
}

// NewHandler initializes a new GraphQL engine Handler.
func NewHandler(cfg Config, redisClient goredis.Cmdable, opts ...HandlerOption) *Handler {
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = 10
	}
	if cfg.MaxComplexity <= 0 {
		cfg.MaxComplexity = 50
	}
	if cfg.UserBudget <= 0 {
		cfg.UserBudget = 1000
	}

	loadedSchema, err := gqlparser.LoadSchema(&ast.Source{
		Name:  "schema.graphqls",
		Input: defaultSchemaSource,
	})
	if err != nil {
		panic(fmt.Sprintf("failed to parse default graphql schema: %v", err))
	}

	h := &Handler{
		cfg:           cfg,
		schema:        loadedSchema,
		depthEngine:   NewDepthEngine(cfg.MaxDepth),
		complexEngine: NewComplexityEngine(ComplexityConfig{MaxComplexity: cfg.MaxComplexity}),
		budgetTracker: NewBudgetTracker(cfg.UserBudget, redisClient),
		resolvers:     make(map[string]ResolverFunc),
	}

	h.registerDefaultResolvers()

	for _, opt := range opts {
		opt(h)
	}

	return h
}

func (h *Handler) registerDefaultResolvers() {
	h.resolvers["health"] = func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
		return "ok", nil
	}

	h.resolvers["categories"] = func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
		return []map[string]interface{}{
			{
				"id":   "cat-1",
				"name": "Electronics",
				"subcategories": []map[string]interface{}{
					{
						"id":            "cat-1-1",
						"name":          "Laptops",
						"subcategories": []map[string]interface{}{},
						"categories":    []map[string]interface{}{},
					},
				},
				"categories": []map[string]interface{}{},
			},
			{
				"id":            "cat-2",
				"name":          "Books",
				"subcategories": []map[string]interface{}{},
				"categories":    []map[string]interface{}{},
			},
		}, nil
	}

	h.resolvers["user"] = func(_ context.Context, args map[string]interface{}) (interface{}, error) {
		userID := "1"
		if id, ok := args["id"].(string); ok && id != "" {
			userID = id
		}
		return map[string]interface{}{
			"id":    userID,
			"name":  "Test User",
			"email": "test@example.com",
			"category": map[string]interface{}{
				"id":            "cat-1",
				"name":          "General",
				"subcategories": []map[string]interface{}{},
				"categories":    []map[string]interface{}{},
			},
		}, nil
	}
}

// RegisterQueryResolver registers a custom field resolver for root queries.
func (h *Handler) RegisterQueryResolver(fieldName string, fn ResolverFunc) {
	h.resolvers[fieldName] = fn
}

// RegisterRoutes registers GraphQL and, when enabled, Playground routes with the provided Gin router.
func (h *Handler) RegisterRoutes(r gin.IRoutes) {
	r.POST("/graphql", h.GraphQLHandler())
	r.GET("/graphql", h.GraphQLHandler())
	if h.enablePlayground {
		r.Handle(http.MethodGet, "/playground", h.PlaygroundHandler())
		r.Handle(http.MethodGet, "/graphiql", h.PlaygroundHandler())
	}
}

// PlaygroundHandler returns a Gin handler serving the GraphQL Playground web IDE.
func (h *Handler) PlaygroundHandler() gin.HandlerFunc {
	ph := playground.Handler("GraphQL playground", "/graphql")
	return func(c *gin.Context) {
		ph.ServeHTTP(c.Writer, c.Request)
	}
}

// GraphQLHandler returns a Gin handler processing GraphQL query executions.
// @Summary Execute GraphQL query
// @Description Executes a GraphQL query or mutation with depth, complexity, and budget enforcement
// @Tags GraphQL
// @Accept json
// @Produce json,application/problem+json
// @Param request body RequestPayload false "GraphQL request payload"
// @Param query query string false "GraphQL query string"
// @Param operationName query string false "GraphQL operation name"
// @Param variables query string false "JSON-encoded GraphQL variables"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]interface{}
// @Failure 413 {object} map[string]interface{}
// @Failure 429 {object} map[string]interface{}
// @Router /graphql [post]
// @Router /graphql [get]
func (h *Handler) GraphQLHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var payload RequestPayload

		if c.Request.Method == http.MethodGet {
			payload.Query = c.Query("query")
			payload.OperationName = c.Query("operationName")
			if vars := c.Query("variables"); vars != "" {
				_ = json.Unmarshal([]byte(vars), &payload.Variables)
			}
		} else {
			if c.Request.Body != nil {
				c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*1024*1024)
			}
			if err := c.ShouldBindJSON(&payload); err != nil {
				var maxBytesErr *http.MaxBytesError
				if errors.As(err, &maxBytesErr) || strings.Contains(err.Error(), "request body too large") {
					c.JSON(http.StatusRequestEntityTooLarge, gin.H{
						"errors": []gin.H{{"message": "request body exceeds maximum allowed size of 2MB"}},
					})
					return
				}
				c.JSON(http.StatusBadRequest, gin.H{
					"errors": []gin.H{{"message": "invalid request body: " + err.Error()}},
				})
				return
			}
		}

		if payload.Query == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"errors": []gin.H{{"message": "query must not be empty"}},
			})
			return
		}

		// 1. Parse AST
		queryDoc, parseErr := parser.ParseQuery(&ast.Source{
			Name:  "query",
			Input: payload.Query,
		})
		if parseErr != nil {
			c.JSON(http.StatusOK, gin.H{
				"errors": []gin.H{{"message": parseErr.Error()}},
			})
			return
		}

		if len(queryDoc.Operations) > 1 && payload.OperationName == "" {
			c.JSON(http.StatusOK, gin.H{
				"errors": []gin.H{{"message": "Must provide operation name if query contains multiple operations."}},
			})
			return
		}

		// 2. Schema Validation
		if valErrs := validator.Validate(h.schema, queryDoc); len(valErrs) > 0 {
			errList := make([]gin.H, len(valErrs))
			for i, e := range valErrs {
				errList[i] = gin.H{"message": e.Message}
			}
			c.JSON(http.StatusOK, gin.H{"errors": errList})
			return
		}

		// 3. Query Depth Limiting
		depth, err := h.depthEngine.CalculateDepth(c.Request.Context(), queryDoc, payload.OperationName)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"errors": []gin.H{{"message": err.Error()}},
			})
			return
		}
		if depth > h.cfg.MaxDepth {
			c.JSON(http.StatusOK, gin.H{
				"errors": []gin.H{{
					"message": fmt.Sprintf("query exceeds maximum depth of %d (calculated %d)", h.cfg.MaxDepth, depth),
				}},
			})
			return
		}

		// 4. Query Complexity Limiting
		complexity, err := h.complexEngine.CalculateComplexity(c.Request.Context(), queryDoc, payload.OperationName)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"errors": []gin.H{{"message": err.Error()}},
			})
			return
		}
		if complexity > h.cfg.MaxComplexity {
			c.JSON(http.StatusOK, gin.H{
				"errors": []gin.H{{
					"message": fmt.Sprintf("query exceeds maximum complexity of %d (calculated %d)", h.cfg.MaxComplexity, complexity),
				}},
			})
			return
		}

		// 5. Per-User Cumulative Complexity Budget
		identifier := extractUserIdentifier(c)
		allowed, current, err := h.budgetTracker.CheckAndRecord(c.Request.Context(), identifier, complexity)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"errors": []gin.H{{"message": "budget tracker error: " + err.Error()}},
			})
			return
		}
		if !allowed {
			c.JSON(http.StatusOK, gin.H{
				"errors": []gin.H{{
					"message": fmt.Sprintf("query exceeds user complexity budget of %d (current usage: %d, cost: %d)", h.cfg.UserBudget, current, complexity),
				}},
			})
			return
		}

		// 6. Execute Query
		data, execErrs := h.executeQuery(c.Request.Context(), queryDoc, payload.OperationName, payload.Variables)
		resp := gin.H{}
		if data != nil {
			resp["data"] = data
		}
		if len(execErrs) > 0 {
			resp["errors"] = execErrs
		}
		c.JSON(http.StatusOK, resp)
	}
}

func (h *Handler) executeQuery(ctx context.Context, doc *ast.QueryDocument, operationName string, variables map[string]interface{}) (map[string]interface{}, []gin.H) {
	var targetOp *ast.OperationDefinition
	for _, op := range doc.Operations {
		if operationName == "" || op.Name == operationName {
			targetOp = op
			break
		}
	}

	if targetOp == nil {
		return nil, []gin.H{{"message": "operation not found"}}
	}

	result := make(map[string]interface{})
	var errs []gin.H

	for _, sel := range targetOp.SelectionSet {
		field, ok := sel.(*ast.Field)
		if !ok {
			continue
		}

		alias := field.Alias
		if alias == "" {
			alias = field.Name
		}

		if field.Name == "__typename" {
			result[alias] = "Query"
			continue
		}

		resolver, exists := h.resolvers[field.Name]
		if !exists {
			errs = append(errs, gin.H{"message": fmt.Sprintf("no resolver registered for field %q", field.Name)})
			continue
		}

		args := field.ArgumentMap(variables)
		val, err := resolver(ctx, args)
		if err != nil {
			errs = append(errs, gin.H{"message": err.Error()})
			continue
		}

		if len(field.SelectionSet) > 0 {
			result[alias] = filterSelection(val, field.SelectionSet, variables, doc.Fragments)
		} else {
			result[alias] = val
		}
	}

	return result, errs
}

func filterSelection(data interface{}, selSet ast.SelectionSet, variables map[string]interface{}, fragments ast.FragmentDefinitionList) interface{} {
	if data == nil {
		return nil
	}

	switch v := data.(type) {
	case []map[string]interface{}:
		res := make([]interface{}, len(v))
		for i, item := range v {
			res[i] = filterSelection(item, selSet, variables, fragments)
		}
		return res
	case []interface{}:
		res := make([]interface{}, len(v))
		for i, item := range v {
			res[i] = filterSelection(item, selSet, variables, fragments)
		}
		return res
	case map[string]interface{}:
		res := make(map[string]interface{})
		for _, sel := range selSet {
			switch s := sel.(type) {
			case *ast.Field:
				alias := s.Alias
				if alias == "" {
					alias = s.Name
				}
				if s.Name == "__typename" {
					res[alias] = "Object"
					continue
				}
				val, exists := v[s.Name]
				if !exists {
					continue
				}
				if len(s.SelectionSet) > 0 {
					res[alias] = filterSelection(val, s.SelectionSet, variables, fragments)
				} else {
					res[alias] = val
				}
			case *ast.InlineFragment:
				if subRes, ok := filterSelection(v, s.SelectionSet, variables, fragments).(map[string]interface{}); ok {
					for k, val := range subRes {
						res[k] = val
					}
				}
			case *ast.FragmentSpread:
				frag := fragments.ForName(s.Name)
				if frag != nil {
					if subRes, ok := filterSelection(v, frag.SelectionSet, variables, fragments).(map[string]interface{}); ok {
						for k, val := range subRes {
							res[k] = val
						}
					}
				}
			}
		}
		return res
	default:
		return data
	}
}

func extractUserIdentifier(c *gin.Context) string {
	if val, exists := c.Get("user"); exists {
		switch u := val.(type) {
		case *auth.Claims:
			if u.UserID != "" {
				return u.UserID
			}
			if u.Subject != "" {
				return u.Subject
			}
		case string:
			if u != "" {
				return u
			}
		case fmt.Stringer:
			if s := u.String(); s != "" {
				return s
			}
		}
	}
	if val, exists := c.Get("user_id"); exists {
		if s, ok := val.(string); ok && s != "" {
			return s
		}
	}
	if val, exists := c.Get("userID"); exists {
		if s, ok := val.(string); ok && s != "" {
			return s
		}
	}
	ip := c.ClientIP()
	if ip != "" {
		return ip
	}
	return "anonymous"
}
