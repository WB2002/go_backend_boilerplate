package server_test

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/server"
)

func TestServerKeepAliveAndGracefulShutdown(t *testing.T) {
	cfg := config.HTTPConfig{
		Port:               18080,
		KeepAliveTimeoutMs: 65000,
		RequestTimeoutMs:   30000,
		ShutdownDrainMs:    500,
		ShutdownForceMs:    1000,
	}

	srv := server.New(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	errChan := make(chan error, 1)
	go func() {
		errChan <- srv.Start()
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:18080/health/live")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = srv.Shutdown(ctx)
	assert.NoError(t, err)

	select {
	case err := <-errChan:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("server.Start() did not exit after Shutdown()")
	}
}

func TestServerConfigurationAndALBKeepAlive(t *testing.T) {
	cfg := config.HTTPConfig{
		Port:                8081,
		KeepAliveTimeoutMs:  65000,
		RequestTimeoutMs:    30000,
		RequestTimeoutMaxMs: 45000,
		ShutdownDrainMs:     500,
		ShutdownForceMs:     1000,
	}

	srv := server.New(cfg, http.NotFoundHandler())
	require.NotNil(t, srv)

	httpSrv := srv.HTTPServer()
	require.NotNil(t, httpSrv)

	// ALB keep-alive: Backend idle timeout must be 65s (> 60s AWS ALB default)
	assert.Equal(t, 65*time.Second, httpSrv.IdleTimeout)
	assert.Equal(t, 5*time.Second, httpSrv.ReadHeaderTimeout)
	assert.Equal(t, 30*time.Second, httpSrv.ReadTimeout)
	assert.Equal(t, 45*time.Second, httpSrv.WriteTimeout)
	assert.Equal(t, ":8081", httpSrv.Addr)
}

func TestServerGracefulDrainDelay(t *testing.T) {
	cfg := config.HTTPConfig{
		Port:               18082,
		KeepAliveTimeoutMs: 65000,
		ShutdownDrainMs:    200,
		ShutdownForceMs:    500,
	}

	srv := server.New(cfg, http.NotFoundHandler())

	errChan := make(chan error, 1)
	go func() {
		errChan <- srv.Start()
	}()

	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := srv.Shutdown(ctx)
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.GreaterOrEqual(t, elapsed, 180*time.Millisecond, "Shutdown should wait for ShutdownDrainMs")

	select {
	case err := <-errChan:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("server.Start() did not exit after Shutdown()")
	}
}

func TestServerSignalHandling(t *testing.T) {
	cfg := config.HTTPConfig{
		Port:               18083,
		KeepAliveTimeoutMs: 65000,
		ShutdownDrainMs:    100,
		ShutdownForceMs:    500,
	}

	srv := server.New(cfg, http.NotFoundHandler())

	sigChan := make(chan syscall.Signal, 1)
	errChan := make(chan error, 1)

	go func() {
		errChan <- srv.StartWithSignalChannel(sigChan)
	}()

	time.Sleep(50 * time.Millisecond)

	// Simulate SIGTERM signal
	sigChan <- syscall.SIGTERM

	select {
	case err := <-errChan:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not exit after receiving SIGTERM")
	}
}

func TestServerInFlightRequestDrain(t *testing.T) {
	cfg := config.HTTPConfig{
		Port:               18084,
		KeepAliveTimeoutMs: 65000,
		ShutdownDrainMs:    50,
		ShutdownForceMs:    1500,
	}

	var inFlightFinished atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		inFlightFinished.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("slow response completed"))
	})

	srv := server.New(cfg, mux)

	sigChan := make(chan syscall.Signal, 1)
	serverExitChan := make(chan error, 1)

	go func() {
		serverExitChan <- srv.StartWithSignalChannel(sigChan)
	}()

	time.Sleep(50 * time.Millisecond)

	type responseResult struct {
		body       string
		statusCode int
		err        error
	}

	clientDone := make(chan responseResult, 1)

	// Launch in-flight client request
	go func() {
		resp, err := http.Get("http://127.0.0.1:18084/slow")
		if err != nil {
			clientDone <- responseResult{err: err}
			return
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(resp.Body)
		clientDone <- responseResult{
			statusCode: resp.StatusCode,
			body:       string(bodyBytes),
			err:        err,
		}
	}()

	// Allow client request to reach server and enter 150ms sleep
	time.Sleep(40 * time.Millisecond)

	// Trigger shutdown via signal while request is in-flight
	sigChan <- syscall.SIGTERM

	// 1. In-flight request must complete successfully
	select {
	case res := <-clientDone:
		require.NoError(t, res.err, "in-flight request must not fail during shutdown")
		assert.Equal(t, http.StatusOK, res.statusCode)
		assert.Equal(t, "slow response completed", res.body)
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not complete within timeout")
	}

	assert.True(t, inFlightFinished.Load(), "handler must have finished before server shut down")

	// 2. Server must cleanly exit after draining active request
	select {
	case err := <-serverExitChan:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not exit after in-flight request finished")
	}
}

func TestServerDrainHook(t *testing.T) {
	cfg := config.HTTPConfig{
		Port:            18085,
		ShutdownDrainMs: 50,
		ShutdownForceMs: 500,
	}

	srv := server.New(cfg, http.NotFoundHandler())
	var hookCalled atomic.Bool
	srv.RegisterDrainHook(func() {
		hookCalled.Store(true)
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := srv.Shutdown(ctx)
	assert.NoError(t, err)
	assert.True(t, hookCalled.Load(), "drain hook must be executed on shutdown")
}
