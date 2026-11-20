package graphql

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/vektah/gqlparser/v2/ast"
)

const maxASTTraversalVisits = 5000

// Sentinel errors returned by GraphQL depth and complexity analysis.
var (
	ErrDepthLimitExceeded      = errors.New("query exceeds maximum depth")
	ErrComplexityLimitExceeded = errors.New("query exceeds maximum complexity")
)

func checkContextAndVisitCap(ctx context.Context, visitedNodes *int, limitErr error) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	*visitedNodes++
	if *visitedNodes > maxASTTraversalVisits {
		return fmt.Errorf("%w: AST traversal visit limit of %d exceeded", limitErr, maxASTTraversalVisits)
	}
	return nil
}

// DepthEngine computes and validates GraphQL query AST depth against configured limits.
type DepthEngine struct {
	maxDepth int
}

// NewDepthEngine initializes a DepthEngine with the given maxDepth limit.
func NewDepthEngine(maxDepth int) *DepthEngine {
	if maxDepth <= 0 {
		maxDepth = 10
	}
	return &DepthEngine{maxDepth: maxDepth}
}

// MaxDepth returns the configured maximum query depth.
func (d *DepthEngine) MaxDepth() int {
	return d.maxDepth
}

// CalculateDepth computes the maximum depth of an AST query document.
func (d *DepthEngine) CalculateDepth(ctx context.Context, doc *ast.QueryDocument, operationName string) (int, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	if doc == nil || len(doc.Operations) == 0 {
		return 0, nil
	}

	depthMemo := make(map[string]int)
	visitedNodes := 0
	maxDepth := 0
	for _, op := range doc.Operations {
		if operationName != "" && op.Name != operationName {
			continue
		}
		visiting := make(map[string]bool)
		opDepth, err := d.calculateSelectionSetDepth(ctx, op.SelectionSet, doc.Fragments, visiting, depthMemo, &visitedNodes)
		if err != nil {
			return 0, err
		}
		if opDepth > maxDepth {
			maxDepth = opDepth
		}
	}

	if maxDepth > d.maxDepth {
		return maxDepth, fmt.Errorf("%w of %d (calculated %d)", ErrDepthLimitExceeded, d.maxDepth, maxDepth)
	}
	return maxDepth, nil
}

func (d *DepthEngine) calculateSelectionSetDepth(
	ctx context.Context,
	selSet ast.SelectionSet,
	fragments ast.FragmentDefinitionList,
	visiting map[string]bool,
	depthMemo map[string]int,
	visitedNodes *int,
) (int, error) {
	if len(selSet) == 0 {
		return 0, nil
	}

	maxChildDepth := 0
	for _, sel := range selSet {
		if err := checkContextAndVisitCap(ctx, visitedNodes, ErrDepthLimitExceeded); err != nil {
			return 0, err
		}

		switch s := sel.(type) {
		case *ast.Field:
			childDepth := 1
			if len(s.SelectionSet) > 0 {
				subDepth, err := d.calculateSelectionSetDepth(ctx, s.SelectionSet, fragments, visiting, depthMemo, visitedNodes)
				if err != nil {
					return 0, err
				}
				childDepth = 1 + subDepth
			}
			if childDepth > maxChildDepth {
				maxChildDepth = childDepth
			}
		case *ast.InlineFragment:
			childDepth, err := d.calculateSelectionSetDepth(ctx, s.SelectionSet, fragments, visiting, depthMemo, visitedNodes)
			if err != nil {
				return 0, err
			}
			if childDepth > maxChildDepth {
				maxChildDepth = childDepth
			}
		case *ast.FragmentSpread:
			if cachedDepth, ok := depthMemo[s.Name]; ok {
				if cachedDepth > maxChildDepth {
					maxChildDepth = cachedDepth
				}
				continue
			}
			if !visiting[s.Name] {
				visiting[s.Name] = true
				frag := fragments.ForName(s.Name)
				childDepth := 0
				if frag != nil {
					var err error
					childDepth, err = d.calculateSelectionSetDepth(ctx, frag.SelectionSet, fragments, visiting, depthMemo, visitedNodes)
					if err != nil {
						visiting[s.Name] = false
						return 0, err
					}
				}
				visiting[s.Name] = false
				depthMemo[s.Name] = childDepth
				if childDepth > maxChildDepth {
					maxChildDepth = childDepth
				}
			}
		}
	}

	return maxChildDepth, nil
}

// -------------------------------------------------------------------------
// Complexity Engine
// -------------------------------------------------------------------------

// ComplexityConfig contains options for field-level weight calculations.
type ComplexityConfig struct {
	MaxComplexity       int
	DefaultScalarWeight int
	DefaultNestedWeight int
	FieldWeights        map[string]int
}

// ComplexityEngine calculates and enforces single-query complexity scores.
type ComplexityEngine struct {
	cfg ComplexityConfig
}

// NewComplexityEngine creates a ComplexityEngine.
func NewComplexityEngine(cfg ComplexityConfig) *ComplexityEngine {
	if cfg.MaxComplexity <= 0 {
		cfg.MaxComplexity = 50
	}
	if cfg.DefaultScalarWeight <= 0 {
		cfg.DefaultScalarWeight = 1
	}
	if cfg.DefaultNestedWeight <= 0 {
		cfg.DefaultNestedWeight = 1
	}
	if cfg.FieldWeights == nil {
		cfg.FieldWeights = make(map[string]int)
	}
	return &ComplexityEngine{cfg: cfg}
}

// MaxComplexity returns the configured maximum single-query complexity score.
func (c *ComplexityEngine) MaxComplexity() int {
	return c.cfg.MaxComplexity
}

// CalculateComplexity computes the total complexity score of the query AST.
func (c *ComplexityEngine) CalculateComplexity(ctx context.Context, doc *ast.QueryDocument, operationName string) (int, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	if doc == nil || len(doc.Operations) == 0 {
		return 0, nil
	}

	complexityMemo := make(map[string]int)
	visitedNodes := 0
	maxComplexity := 0
	for _, op := range doc.Operations {
		if operationName != "" && op.Name != operationName {
			continue
		}
		visiting := make(map[string]bool)
		opComplexity, err := c.calculateSelectionSetComplexity(ctx, op.SelectionSet, doc.Fragments, visiting, complexityMemo, &visitedNodes)
		if err != nil {
			return 0, err
		}
		if opComplexity > maxComplexity {
			maxComplexity = opComplexity
		}
	}

	return maxComplexity, nil
}

func (c *ComplexityEngine) calculateSelectionSetComplexity(
	ctx context.Context,
	selSet ast.SelectionSet,
	fragments ast.FragmentDefinitionList,
	visiting map[string]bool,
	complexityMemo map[string]int,
	visitedNodes *int,
) (int, error) {
	total := 0
	for _, sel := range selSet {
		if err := checkContextAndVisitCap(ctx, visitedNodes, ErrComplexityLimitExceeded); err != nil {
			return 0, err
		}

		switch s := sel.(type) {
		case *ast.Field:
			weight := c.cfg.DefaultScalarWeight
			if len(s.SelectionSet) > 0 && c.cfg.DefaultNestedWeight > 0 {
				weight = c.cfg.DefaultNestedWeight
			}
			if custom, exists := c.cfg.FieldWeights[s.Name]; exists && custom >= 0 {
				weight = custom
			}

			if weight > c.cfg.MaxComplexity-total {
				return 0, fmt.Errorf("%w of %d", ErrComplexityLimitExceeded, c.cfg.MaxComplexity)
			}
			total += weight

			if len(s.SelectionSet) > 0 {
				childComplexity, err := c.calculateSelectionSetComplexity(ctx, s.SelectionSet, fragments, visiting, complexityMemo, visitedNodes)
				if err != nil {
					return 0, err
				}
				if childComplexity > c.cfg.MaxComplexity-total {
					return 0, fmt.Errorf("%w of %d", ErrComplexityLimitExceeded, c.cfg.MaxComplexity)
				}
				total += childComplexity
			}

		case *ast.InlineFragment:
			childComplexity, err := c.calculateSelectionSetComplexity(ctx, s.SelectionSet, fragments, visiting, complexityMemo, visitedNodes)
			if err != nil {
				return 0, err
			}
			if childComplexity > c.cfg.MaxComplexity-total {
				return 0, fmt.Errorf("%w of %d", ErrComplexityLimitExceeded, c.cfg.MaxComplexity)
			}
			total += childComplexity

		case *ast.FragmentSpread:
			fragComplexity, ok := complexityMemo[s.Name]
			if !ok {
				if visiting[s.Name] {
					continue
				}
				visiting[s.Name] = true
				frag := fragments.ForName(s.Name)
				if frag != nil {
					var err error
					fragComplexity, err = c.calculateSelectionSetComplexity(ctx, frag.SelectionSet, fragments, visiting, complexityMemo, visitedNodes)
					if err != nil {
						visiting[s.Name] = false
						return 0, err
					}
				}
				visiting[s.Name] = false
				complexityMemo[s.Name] = fragComplexity
			}
			if fragComplexity > c.cfg.MaxComplexity-total {
				return 0, fmt.Errorf("%w of %d", ErrComplexityLimitExceeded, c.cfg.MaxComplexity)
			}
			total += fragComplexity
		}
	}
	return total, nil
}

// -------------------------------------------------------------------------
// Cumulative Budget Limiting
// -------------------------------------------------------------------------

// budgetBucket tracks cumulative complexity in memory with expiration.
type budgetBucket struct {
	total     int
	expiresAt time.Time
}

type memoryBudgetStore struct {
	mu        sync.Mutex
	buckets   map[string]*budgetBucket
	lastPrune time.Time
}

func newMemoryBudgetStore() *memoryBudgetStore {
	return &memoryBudgetStore{
		buckets:   make(map[string]*budgetBucket),
		lastPrune: time.Now(),
	}
}

func (m *memoryBudgetStore) CheckAndRecord(key string, cost int, budget int) (bool, int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	// Opportunistic pruning to minimize lock contention: clean up expired entries
	// only when map exceeds 1000 items or at least 60 seconds have elapsed since last prune.
	if (len(m.buckets) > 1000 || now.Sub(m.lastPrune) > 60*time.Second) && len(m.buckets) > 0 {
		m.lastPrune = now
		for k, b := range m.buckets {
			if now.After(b.expiresAt) {
				delete(m.buckets, k)
			}
		}
	}

	bucket, exists := m.buckets[key]
	if exists && now.After(bucket.expiresAt) {
		delete(m.buckets, key)
		exists = false
		bucket = nil
	}

	if !exists {
		bucket = &budgetBucket{
			total:     0,
			expiresAt: now.Add(120 * time.Second),
		}
		m.buckets[key] = bucket
	}

	if bucket.total+cost > budget {
		return false, bucket.total
	}

	bucket.total += cost
	bucket.expiresAt = now.Add(120 * time.Second)
	return true, bucket.total
}

// Redis Lua script for atomic budget limit check and record
var redisBudgetScript = goredis.NewScript(`
local val = tonumber(redis.call('GET', KEYS[1])) or 0
local cost = tonumber(ARGV[1])
local budget = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])

if val + cost > budget then
    return {0, val}
end

local newVal = redis.call('INCRBY', KEYS[1], cost)
if val == 0 then
    redis.call('EXPIRE', KEYS[1], ttl)
end

return {1, newVal}
`)

func toInt64(v interface{}) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case float64:
		return int64(n), nil
	case string:
		var i int64
		_, err := fmt.Sscanf(n, "%d", &i)
		return i, err
	default:
		return 0, fmt.Errorf("unsupported type %T for int64 conversion", v)
	}
}

// BudgetTracker enforces per-user cumulative complexity budgets per minute.
type BudgetTracker struct {
	userBudget  int
	redisClient goredis.Cmdable
	memoryStore *memoryBudgetStore
}

// NewBudgetTracker initializes a BudgetTracker.
func NewBudgetTracker(userBudget int, redisClient goredis.Cmdable) *BudgetTracker {
	if userBudget <= 0 {
		userBudget = 1000
	}
	return &BudgetTracker{
		userBudget:  userBudget,
		redisClient: redisClient,
		memoryStore: newMemoryBudgetStore(),
	}
}

// CheckAndRecord verifies if the identifier's cumulative budget permits the query complexity,
// updating usage if within budget.
func (b *BudgetTracker) CheckAndRecord(ctx context.Context, identifier string, complexity int) (bool, int, error) {
	if identifier == "" {
		identifier = "anonymous"
	}

	minuteTimestamp := time.Now().Unix() / 60
	key := fmt.Sprintf("gql:budget:%s:%d", identifier, minuteTimestamp)

	if b.redisClient != nil {
		allowed, current, err := b.checkAndRecordRedis(ctx, key, complexity)
		if err == nil {
			return allowed, current, nil
		}
		// On Redis failure, seamlessly fall back to memory store
	}

	allowed, current := b.memoryStore.CheckAndRecord(key, complexity, b.userBudget)
	return allowed, current, nil
}

func (b *BudgetTracker) checkAndRecordRedis(ctx context.Context, key string, complexity int) (bool, int, error) {
	res, err := redisBudgetScript.Run(ctx, b.redisClient, []string{key}, complexity, b.userBudget, 120).Slice()
	if err != nil {
		return false, 0, err
	}

	if len(res) < 2 {
		return false, 0, fmt.Errorf("unexpected redis script response: %v", res)
	}

	allowedInt, err := toInt64(res[0])
	if err != nil {
		return false, 0, fmt.Errorf("invalid allowed status from redis script: %w", err)
	}

	usageInt, err := toInt64(res[1])
	if err != nil {
		return false, 0, fmt.Errorf("invalid usage from redis script: %w", err)
	}

	return allowedInt == 1, int(usageInt), nil
}
