package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go_backend_boilerplate/internal/platform/logger"
)

var (
	// ErrServiceClosed is returned when an operation cannot be performed because the service is closed.
	ErrServiceClosed = errors.New("notification service is closed")
)

// Constants for Slack attachment accent colors.
const (
	ColorInfo    = "#36a64f"
	ColorWarning = "#ecb22e"
	ColorDanger  = "#e01e5a"
)

// Config defines the configuration parameters for the Slack notification service.
type Config struct {
	WebhookURL string        `json:"webhook_url"`
	QueueSize  int           `json:"queue_size"`
	Workers    int           `json:"workers"`
	Timeout    time.Duration `json:"timeout"`
	Logger     *zerolog.Logger
}

// Payload represents an event notification dispatched to Slack.
type Payload struct {
	Title     string            `json:"title"`
	Message   string            `json:"message"`
	Level     string            `json:"level"`
	Fields    map[string]string `json:"fields,omitempty"`
	Timestamp time.Time         `json:"timestamp,omitempty"`
}

// SlackField represents a key-value attribute displayed in Slack Block Kit / Attachment.
type SlackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// SlackAttachment represents a formatted Slack legacy/Block Kit attachment.
type SlackAttachment struct {
	Color     string       `json:"color,omitempty"`
	Title     string       `json:"title,omitempty"`
	Text      string       `json:"text,omitempty"`
	Fields    []SlackField `json:"fields,omitempty"`
	Timestamp int64        `json:"ts,omitempty"`
}

// SlackWebhookBody represents the top-level JSON body sent to the Slack webhook endpoint.
type SlackWebhookBody struct {
	Text        string            `json:"text,omitempty"`
	Attachments []SlackAttachment `json:"attachments,omitempty"`
}

// HTTPDoer defines the minimal contract required to execute outbound HTTP requests.
// Both standard *http.Client and resilient *httpclient.Client implement this interface.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Service defines the contract for sending notifications.
type Service interface {
	Send(ctx context.Context, payload Payload) error
	SendSync(ctx context.Context, payload Payload) error
	Close(ctx context.Context) error
	Drain(ctx context.Context) error
}

// SlackService implements Service using Slack Incoming Webhooks with fail-open semantics.
type SlackService struct {
	cfg          Config
	client       HTTPDoer
	queue        chan Payload
	wg           sync.WaitGroup
	closed       atomic.Bool
	closeOnce    sync.Once
	mu           sync.RWMutex
	done         chan struct{}
	workerCtx    context.Context
	workerCancel context.CancelFunc
	logger       *zerolog.Logger
}

var _ Service = (*SlackService)(nil)

// NewSlackService constructs and initializes a new SlackService worker pool.
func NewSlackService(cfg Config, clients ...HTTPDoer) *SlackService {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 100
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}

	log := cfg.Logger
	if log == nil {
		log = logger.Default()
	}

	var doer HTTPDoer
	if len(clients) > 0 && clients[0] != nil {
		val := reflect.ValueOf(clients[0])
		if val.Kind() == reflect.Ptr && val.IsNil() {
			doer = nil
		} else {
			doer = clients[0]
		}
	}
	if doer == nil {
		doer = &http.Client{
			Timeout: cfg.Timeout,
		}
	}

	workerCtx, workerCancel := context.WithCancel(context.Background())

	svc := &SlackService{
		cfg:          cfg,
		client:       doer,
		queue:        make(chan Payload, cfg.QueueSize),
		done:         make(chan struct{}),
		workerCtx:    workerCtx,
		workerCancel: workerCancel,
		logger:       log,
	}

	for i := 0; i < cfg.Workers; i++ {
		svc.wg.Add(1)
		go svc.worker(i)
	}

	return svc
}

// Send enqueues a payload asynchronously without blocking the caller.
// If the queue is saturated, it logs a warning and drops the payload (fail-open),
// ensuring the caller's transaction or request is never interrupted.
func (s *SlackService) Send(ctx context.Context, payload Payload) error {
	if s.cfg.WebhookURL == "" {
		return nil
	}

	if s.closed.Load() {
		s.logger.Warn().
			Str("title", payload.Title).
			Msg("slack notification dropped: service is closed")
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed.Load() {
		s.logger.Warn().
			Str("title", payload.Title).
			Msg("slack notification dropped: service is closed")
		return nil
	}

	select {
	case s.queue <- payload:
		return nil
	default:
		s.logger.Warn().
			Str("title", payload.Title).
			Str("level", payload.Level).
			Msg("slack notification queue full, dropping payload (fail-open)")
		return nil
	}
}

// SendSync performs a direct synchronous HTTP dispatch of the payload.
// Unlike Send, delivery errors are returned to the caller for direct status verification.
func (s *SlackService) SendSync(ctx context.Context, payload Payload) error {
	if s.cfg.WebhookURL == "" {
		return nil
	}
	if s.closed.Load() {
		return ErrServiceClosed
	}

	reqCtx := ctx
	if _, ok := reqCtx.Deadline(); !ok && s.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(reqCtx, s.cfg.Timeout)
		defer cancel()
	}

	return s.sendHTTP(reqCtx, payload)
}

// Close gracefully closes the queue, flushes buffered notifications, and terminates workers
// within the provided context deadline.
func (s *SlackService) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.mu.Lock()
		close(s.queue)
		s.mu.Unlock()

		go func() {
			s.wg.Wait()
			close(s.done)
		}()
	})

	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		s.workerCancel()
		return ctx.Err()
	}
}

// Drain is an alias for Close, flushing buffered payloads and waiting for workers to complete.
func (s *SlackService) Drain(ctx context.Context) error {
	return s.Close(ctx)
}

func (s *SlackService) worker(id int) {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error().
				Interface("panic", r).
				Int("worker_id", id).
				Msg("recovered panic in slack notification worker")
		}
	}()

	for {
		select {
		case <-s.workerCtx.Done():
			return
		case payload, ok := <-s.queue:
			if !ok {
				return
			}
			s.safeDeliver(payload)
		}
	}
}

func (s *SlackService) safeDeliver(payload Payload) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error().
				Interface("panic", r).
				Str("title", payload.Title).
				Msg("recovered panic during slack notification delivery")
		}
	}()

	ctx, cancel := context.WithTimeout(s.workerCtx, s.cfg.Timeout)
	defer cancel()

	if err := s.sendHTTP(ctx, payload); err != nil {
		s.logger.Warn().
			Err(err).
			Str("title", payload.Title).
			Str("level", payload.Level).
			Msg("failed to deliver slack notification (fail-open)")
	}
}

func (s *SlackService) sendHTTP(ctx context.Context, payload Payload) error {
	if s.cfg.WebhookURL == "" {
		return nil
	}

	bodyBytes, err := FormatPayload(payload)
	if err != nil {
		return fmt.Errorf("failed to format slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.WebhookURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack webhook request failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		respBody := strings.TrimSpace(string(respBodyBytes))
		if respBody != "" {
			return fmt.Errorf("slack webhook returned non-2xx status code %d: %s", resp.StatusCode, respBody)
		}
		return fmt.Errorf("slack webhook returned non-2xx status code: %d", resp.StatusCode)
	}

	return nil
}

// FormatPayload serializes Payload into Slack webhook JSON with Block Kit / Attachment styling.
func FormatPayload(p Payload) ([]byte, error) {
	body := BuildSlackPayload(p)
	return json.Marshal(body)
}

// BuildSlackPayload constructs the SlackWebhookBody with color accent and fields.
func BuildSlackPayload(p Payload) SlackWebhookBody {
	color := ColorForLevel(p.Level)

	var fields []SlackField
	if len(p.Fields) > 0 {
		keys := make([]string, 0, len(p.Fields))
		for k := range p.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fields = make([]SlackField, 0, len(keys))
		for _, k := range keys {
			fields = append(fields, SlackField{
				Title: k,
				Value: p.Fields[k],
				Short: true,
			})
		}
	}

	var ts int64
	if !p.Timestamp.IsZero() {
		ts = p.Timestamp.Unix()
	} else {
		ts = time.Now().Unix()
	}

	attachment := SlackAttachment{
		Color:     color,
		Title:     p.Title,
		Text:      p.Message,
		Fields:    fields,
		Timestamp: ts,
	}

	summaryText := p.Title
	if summaryText == "" {
		summaryText = p.Message
	}

	return SlackWebhookBody{
		Text:        summaryText,
		Attachments: []SlackAttachment{attachment},
	}
}

// ColorForLevel maps severity level strings to Slack hex color codes.
func ColorForLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "info", "good":
		return ColorInfo
	case "warning", "warn":
		return ColorWarning
	case "danger", "error", "critical":
		return ColorDanger
	default:
		return ColorInfo
	}
}
