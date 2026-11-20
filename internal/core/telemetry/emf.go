package telemetry

import (
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Default EMF configuration constants.
const (
	DefaultNamespace   = "go_backend_boilerplate"
	DefaultEnvironment = "production"
	DefaultService     = "go_backend_boilerplate"
)

// MetricDefinition defines a single metric entry in CloudWatchMetrics.
type MetricDefinition struct {
	Name string `json:"Name"`
	Unit string `json:"Unit,omitempty"`
}

// MetricDirective encapsulates CloudWatchMetrics metadata under the _aws key.
type MetricDirective struct {
	Namespace  string             `json:"Namespace"`
	Dimensions [][]string         `json:"Dimensions"`
	Metrics    []MetricDefinition `json:"Metrics"`
}

// AWSMetadata represents the root _aws object adhering to AWS CloudWatch EMF specification.
type AWSMetadata struct {
	Timestamp         int64             `json:"Timestamp"`
	CloudWatchMetrics []MetricDirective `json:"CloudWatchMetrics"`
}

// EMFEmitter generates CloudWatch Embedded Metric Format (EMF) logs.
type EMFEmitter struct {
	writer      io.Writer
	namespace   string
	environment string
	service     string
	nowFunc     func() time.Time
	mu          sync.Mutex
}

// EMFOption configures EMFEmitter.
type EMFOption func(*EMFEmitter)

// WithEMFWriter sets the output writer for EMF JSON logs.
func WithEMFWriter(w io.Writer) EMFOption {
	return func(e *EMFEmitter) {
		if w != nil {
			e.writer = w
		}
	}
}

// WithEMFNamespace sets the default EMF CloudWatch namespace.
func WithEMFNamespace(ns string) EMFOption {
	return func(e *EMFEmitter) {
		if ns != "" {
			e.namespace = ns
		}
	}
}

// WithEMFEnvironment sets the default environment dimension.
func WithEMFEnvironment(env string) EMFOption {
	return func(e *EMFEmitter) {
		if env != "" {
			e.environment = env
		}
	}
}

// WithEMFService sets the default service dimension.
func WithEMFService(svc string) EMFOption {
	return func(e *EMFEmitter) {
		if svc != "" {
			e.service = svc
		}
	}
}

// WithEMFNow sets the time supplier for testing.
func WithEMFNow(nowFunc func() time.Time) EMFOption {
	return func(e *EMFEmitter) {
		if nowFunc != nil {
			e.nowFunc = nowFunc
		}
	}
}

// NewEMFEmitter creates and initializes a new EMFEmitter with safe defaults.
func NewEMFEmitter(opts ...EMFOption) *EMFEmitter {
	emitter := &EMFEmitter{
		writer:      os.Stdout,
		namespace:   DefaultNamespace,
		environment: DefaultEnvironment,
		service:     DefaultService,
		nowFunc:     time.Now,
	}

	for _, opt := range opts {
		opt(emitter)
	}

	return emitter
}

// Emit generates and writes a single CloudWatch EMF log entry.
func (e *EMFEmitter) Emit(namespace string, dimensions map[string]string, metrics map[string]float64, units map[string]string) error {
	if namespace == "" {
		namespace = e.namespace
	}

	if dimensions == nil {
		dimensions = map[string]string{
			"Environment": e.environment,
			"Service":     e.service,
		}
	}

	// Deterministically sort dimension names (excluding reserved _aws key)
	dimKeys := make([]string, 0, len(dimensions))
	for k := range dimensions {
		if k == "_aws" {
			continue
		}
		dimKeys = append(dimKeys, k)
	}
	sort.Strings(dimKeys)

	// Deterministically sort metric definitions (excluding reserved _aws key)
	metricNames := make([]string, 0, len(metrics))
	for k := range metrics {
		if k == "_aws" {
			continue
		}
		metricNames = append(metricNames, k)
	}
	sort.Strings(metricNames)

	metricDefs := make([]MetricDefinition, 0, len(metricNames))
	for _, name := range metricNames {
		unit := "None"
		if units != nil && units[name] != "" {
			unit = units[name]
		} else if strings.Contains(name, "Count") {
			unit = "Count"
		} else if strings.Contains(name, "Latency") || strings.Contains(name, "Duration") {
			unit = "Milliseconds"
		}

		metricDefs = append(metricDefs, MetricDefinition{
			Name: name,
			Unit: unit,
		})
	}

	directive := MetricDirective{
		Namespace:  namespace,
		Dimensions: [][]string{dimKeys},
		Metrics:    metricDefs,
	}

	timestamp := e.nowFunc().UnixMilli()
	awsMeta := AWSMetadata{
		Timestamp:         timestamp,
		CloudWatchMetrics: []MetricDirective{directive},
	}

	// Build top-level JSON document
	doc := make(map[string]interface{})
	doc["_aws"] = awsMeta

	for k, v := range dimensions {
		if k == "_aws" {
			continue
		}
		doc[k] = v
	}

	for k, v := range metrics {
		if k == "_aws" {
			continue
		}
		doc[k] = v
	}

	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}

	data = append(data, '\n')

	e.mu.Lock()
	defer e.mu.Unlock()

	_, err = e.writer.Write(data)
	return err
}

// RecordOverload503 logs an overload shedding event (Overload503Count=1) using EMF.
func (e *EMFEmitter) RecordOverload503() error {
	return e.Emit(
		e.namespace,
		map[string]string{
			"Environment": e.environment,
			"Service":     e.service,
		},
		map[string]float64{
			"Overload503Count": 1,
		},
		map[string]string{
			"Overload503Count": "Count",
		},
	)
}

// RecordLatency logs an operation latency in milliseconds using EMF.
func (e *EMFEmitter) RecordLatency(name string, d time.Duration) error {
	return e.Emit(
		e.namespace,
		map[string]string{
			"Environment": e.environment,
			"Service":     e.service,
		},
		map[string]float64{
			name: float64(d.Nanoseconds()) / 1e6,
		},
		map[string]string{
			name: "Milliseconds",
		},
	)
}
