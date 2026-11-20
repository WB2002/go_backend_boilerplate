package notification_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/core/notification"
)

func TestNotificationFailOpenOnBrokenWebhook(t *testing.T) {
	// Points to invalid endpoint that refuses connections
	svc := notification.NewSlackService(notification.Config{
		WebhookURL: "http://127.0.0.1:9999/broken-webhook",
		QueueSize:  10,
		Workers:    1,
		Timeout:    1 * time.Second,
	}, nil)
	defer svc.Close(context.Background())

	// Must return nil error, preserving transaction integrity (fail-open)
	err := svc.Send(context.Background(), notification.Payload{
		Title:   "Security Alert: Suspicious Login",
		Message: "Failed login attempt",
		Level:   "warning",
	})
	assert.NoError(t, err, "Notification failure must not break calling transaction (fail-open)")
}

func TestNotificationSuccessFormat(t *testing.T) {
	var receivedBody notification.SlackWebhookBody
	var receivedContentType string
	var reqCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		receivedContentType = r.Header.Get("Content-Type")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.Unmarshal(body, &receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	svc := notification.NewSlackService(notification.Config{
		WebhookURL: server.URL,
		QueueSize:  10,
		Workers:    1,
		Timeout:    2 * time.Second,
	}, server.Client())
	defer svc.Close(context.Background())

	fixedTime := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	err := svc.SendSync(context.Background(), notification.Payload{
		Title:   "Critical Vulnerability Detected",
		Message: "Malware payload discovered in upload quarantine",
		Level:   "danger",
		Fields: map[string]string{
			"Host": "prod-api-01",
			"User": "admin@example.com",
		},
		Timestamp: fixedTime,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), reqCount.Load())
	assert.Equal(t, "application/json", receivedContentType)

	// Verify top-level summary
	assert.Equal(t, "Critical Vulnerability Detected", receivedBody.Text)

	// Verify attachment structure & color accent
	require.Len(t, receivedBody.Attachments, 1)
	att := receivedBody.Attachments[0]
	assert.Equal(t, notification.ColorDanger, att.Color)
	assert.Equal(t, "Critical Vulnerability Detected", att.Title)
	assert.Equal(t, "Malware payload discovered in upload quarantine", att.Text)
	assert.Equal(t, fixedTime.Unix(), att.Timestamp)

	// Verify fields
	require.Len(t, att.Fields, 2)
	assert.Equal(t, "Host", att.Fields[0].Title)
	assert.Equal(t, "prod-api-01", att.Fields[0].Value)
	assert.True(t, att.Fields[0].Short)
	assert.Equal(t, "User", att.Fields[1].Title)
	assert.Equal(t, "admin@example.com", att.Fields[1].Value)
	assert.True(t, att.Fields[1].Short)
}

func TestNotificationQueueOverflowFailOpen(t *testing.T) {
	blockHandler := make(chan struct{})
	var handlerStarted atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerStarted.Add(1)
		<-blockHandler
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Single worker, queue size of 1
	svc := notification.NewSlackService(notification.Config{
		WebhookURL: server.URL,
		QueueSize:  1,
		Workers:    1,
		Timeout:    5 * time.Second,
	}, server.Client())
	defer svc.Close(context.Background())

	// 1st request: worker picks it up and blocks in server
	err1 := svc.Send(context.Background(), notification.Payload{Title: "Item 1"})
	assert.NoError(t, err1)

	// Wait until worker is actively blocking in mock server
	for i := 0; i < 50; i++ {
		if handlerStarted.Load() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 2nd request: fills the single queue slot
	err2 := svc.Send(context.Background(), notification.Payload{Title: "Item 2"})
	assert.NoError(t, err2)

	// 3rd & 4th requests: queue is full, must drop non-blocking without error (fail-open)
	start := time.Now()
	err3 := svc.Send(context.Background(), notification.Payload{Title: "Item 3 - Overflow"})
	err4 := svc.Send(context.Background(), notification.Payload{Title: "Item 4 - Overflow"})
	elapsed := time.Since(start)

	assert.NoError(t, err3)
	assert.NoError(t, err4)
	assert.Less(t, elapsed, 100*time.Millisecond, "Send on full queue must be non-blocking")

	// Unblock mock server so worker drains rapidly
	close(blockHandler)
}

func TestNotificationSynchronousDispatch(t *testing.T) {
	t.Run("success response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		svc := notification.NewSlackService(notification.Config{
			WebhookURL: server.URL,
			Timeout:    2 * time.Second,
		}, server.Client())
		defer svc.Close(context.Background())

		err := svc.SendSync(context.Background(), notification.Payload{
			Title:   "Sync Test",
			Message: "Sync payload",
			Level:   "info",
		})
		assert.NoError(t, err)
	})

	t.Run("server error returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		svc := notification.NewSlackService(notification.Config{
			WebhookURL: server.URL,
			Timeout:    2 * time.Second,
		}, server.Client())
		defer svc.Close(context.Background())

		err := svc.SendSync(context.Background(), notification.Payload{
			Title:   "Sync Failure Test",
			Message: "Sync error expected",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})
}

func TestNotificationGracefulDrain(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	svc := notification.NewSlackService(notification.Config{
		WebhookURL: server.URL,
		QueueSize:  20,
		Workers:    2,
		Timeout:    2 * time.Second,
	}, server.Client())

	totalMessages := 10
	for i := 0; i < totalMessages; i++ {
		err := svc.Send(context.Background(), notification.Payload{
			Title:   "Queued Message",
			Message: "Draining test",
			Level:   "info",
		})
		require.NoError(t, err)
	}

	// Close waits for all queued messages to drain
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := svc.Drain(ctx)
	assert.NoError(t, err)
	assert.Equal(t, int32(totalMessages), count.Load(), "All queued notifications must be delivered before Close completes")
}

func TestNotificationEmptyWebhookURL(t *testing.T) {
	svc := notification.NewSlackService(notification.Config{
		WebhookURL: "",
		QueueSize:  10,
		Workers:    1,
	}, nil)
	defer svc.Close(context.Background())

	// Both Send and SendSync must be safe no-ops
	errSend := svc.Send(context.Background(), notification.Payload{
		Title: "Empty Webhook",
	})
	assert.NoError(t, errSend)

	errSync := svc.SendSync(context.Background(), notification.Payload{
		Title: "Empty Webhook Sync",
	})
	assert.NoError(t, errSync)
}

func TestNotificationColorForLevel(t *testing.T) {
	tests := []struct {
		level    string
		expected string
	}{
		{"info", notification.ColorInfo},
		{"INFO", notification.ColorInfo},
		{"good", notification.ColorInfo},
		{"warning", notification.ColorWarning},
		{"warn", notification.ColorWarning},
		{"danger", notification.ColorDanger},
		{"error", notification.ColorDanger},
		{"critical", notification.ColorDanger},
		{"unknown", notification.ColorInfo},
		{"", notification.ColorInfo},
	}

	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			assert.Equal(t, tc.expected, notification.ColorForLevel(tc.level))
		})
	}
}

type panicThenSuccessDoer struct {
	attempts atomic.Int32
	success  atomic.Int32
}

func (p *panicThenSuccessDoer) Do(req *http.Request) (*http.Response, error) {
	attempt := p.attempts.Add(1)
	if attempt == 1 {
		panic("simulated panic on 1st request")
	}
	p.success.Add(1)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     make(http.Header),
	}, nil
}

func TestNotificationWorkerPanicShield(t *testing.T) {
	doer := &panicThenSuccessDoer{}
	svc := notification.NewSlackService(notification.Config{
		WebhookURL: "http://example.com/panic",
		QueueSize:  5,
		Workers:    1,
		Timeout:    1 * time.Second,
	}, doer)

	// Send 1st message that triggers panic in worker
	err := svc.Send(context.Background(), notification.Payload{
		Title: "Panic trigger",
	})
	require.NoError(t, err)

	// Send 2nd message that should succeed because worker recovered
	err = svc.Send(context.Background(), notification.Payload{
		Title: "Post-panic success",
	})
	require.NoError(t, err)

	// Close waits for all queued messages to drain
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = svc.Close(ctx)
	assert.NoError(t, err)

	// Verify worker survived the panic and processed both requests
	assert.Equal(t, int32(2), doer.attempts.Load(), "both requests should have been attempted")
	assert.Equal(t, int32(1), doer.success.Load(), "2nd request should succeed after worker recovered")
}

type blockingDoer struct {
	started chan struct{}
}

func (b *blockingDoer) Do(req *http.Request) (*http.Response, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestNotificationDrainContextCanceled(t *testing.T) {
	t.Run("deadline exceeded aborts blocked workers", func(t *testing.T) {
		started := make(chan struct{}, 1)
		doer := &blockingDoer{started: started}

		svc := notification.NewSlackService(notification.Config{
			WebhookURL: "http://example.com/blocked",
			QueueSize:  5,
			Workers:    1,
			Timeout:    5 * time.Second,
		}, doer)

		err := svc.Send(context.Background(), notification.Payload{
			Title: "Blocking Payload",
		})
		require.NoError(t, err)

		// Wait until worker is actively blocked in Do
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not start request")
		}

		// Drain with short deadline
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		err = svc.Drain(ctx)
		elapsed := time.Since(start)

		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Less(t, elapsed, 500*time.Millisecond, "Drain must abort promptly on deadline")

		// Confirm workers cleanly terminated and done channel closed
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cleanCancel()
		assert.NoError(t, svc.Close(cleanCtx), "workers must have exited after context cancellation")
	})

	t.Run("canceled context aborts blocked workers", func(t *testing.T) {
		started := make(chan struct{}, 1)
		doer := &blockingDoer{started: started}

		svc := notification.NewSlackService(notification.Config{
			WebhookURL: "http://example.com/blocked",
			QueueSize:  5,
			Workers:    1,
			Timeout:    5 * time.Second,
		}, doer)

		err := svc.Send(context.Background(), notification.Payload{
			Title: "Blocking Payload 2",
		})
		require.NoError(t, err)

		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not start request")
		}

		ctx, cancel := context.WithCancel(context.Background())
		// Cancel immediately
		cancel()

		start := time.Now()
		err = svc.Close(ctx)
		elapsed := time.Since(start)

		assert.ErrorIs(t, err, context.Canceled)
		assert.Less(t, elapsed, 500*time.Millisecond, "Close must abort promptly on cancellation")

		// Confirm workers cleanly terminated and done channel closed
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cleanCancel()
		assert.NoError(t, svc.Close(cleanCtx), "workers must have exited after context cancellation")
	})
}

func TestNotificationErrorResponseBodyLogging(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid_payload"))
	}))
	defer server.Close()

	svc := notification.NewSlackService(notification.Config{
		WebhookURL: server.URL,
		Timeout:    2 * time.Second,
	}, server.Client())
	defer svc.Close(context.Background())

	err := svc.SendSync(context.Background(), notification.Payload{
		Title: "Bad Request Test",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
	assert.Contains(t, err.Error(), "invalid_payload")
}

func TestNotificationSendOnClosedService(t *testing.T) {
	svc := notification.NewSlackService(notification.Config{
		WebhookURL: "http://example.com/closed",
		QueueSize:  5,
		Workers:    1,
	}, nil)

	require.NoError(t, svc.Close(context.Background()))

	// Send drops silently and returns nil (fail-open)
	err := svc.Send(context.Background(), notification.Payload{Title: "After Close"})
	assert.NoError(t, err)

	// SendSync returns ErrServiceClosed
	errSync := svc.SendSync(context.Background(), notification.Payload{Title: "Sync After Close"})
	assert.ErrorIs(t, errSync, notification.ErrServiceClosed)
}

func TestNotificationConcurrentDispatch(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	svc := notification.NewSlackService(notification.Config{
		WebhookURL: server.URL,
		QueueSize:  100,
		Workers:    4,
		Timeout:    2 * time.Second,
	}, server.Client())

	var wg sync.WaitGroup
	concurrentSenders := 20
	for i := 0; i < concurrentSenders; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = svc.Send(context.Background(), notification.Payload{
				Title: "Concurrent message",
				Level: "info",
			})
		}(i)
	}
	wg.Wait()

	require.NoError(t, svc.Close(context.Background()))
	assert.Equal(t, int32(concurrentSenders), received.Load())
}
