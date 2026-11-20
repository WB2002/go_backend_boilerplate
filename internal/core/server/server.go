package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go_backend_boilerplate/internal/config"
)

// Server wraps the standard http.Server with AWS ALB keep-alive optimizations,
// Slowloris header timeout protections, signal trapping, and graceful connection draining.
type Server struct {
	cfg          config.HTTPConfig
	httpServer   *http.Server
	shutdownChan chan struct{}
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	doneOnce     sync.Once
	shutdownErr  error
	drainHooks   []func()
	mu           sync.Mutex
}

// New creates and configures a production-ready HTTP Server.
//
// Key Operational Protections:
//  1. Multi-VM ALB Keep-Alive: IdleTimeout defaults to KeepAliveTimeoutMs (65s),
//     strictly exceeding AWS Application Load Balancer's 60s idle timeout to prevent
//     spurious 502 Bad Gateway race conditions.
//  2. Slowloris Defense: ReadHeaderTimeout is set to 5s to mitigate slow HTTP header attacks.
//  3. Request Deadlines: ReadTimeout is set to 30s and WriteTimeout is set to RequestTimeoutMaxMs (60s).
//  4. Graceful Drain: ShutdownDrainMs drains in-flight requests before listeners are closed.
func New(cfg config.HTTPConfig, handler http.Handler) *Server {
	if handler == nil {
		handler = http.DefaultServeMux
	}

	port := cfg.Port
	if port <= 0 {
		port = 8080
	}

	// 1. ALB Keep-Alive Idle Timeout (default 65s > 60s ALB idle)
	idleTimeout := 65 * time.Second
	if cfg.KeepAliveTimeoutMs > 0 {
		idleTimeout = time.Duration(cfg.KeepAliveTimeoutMs) * time.Millisecond
	}

	// 2. Slowloris Protection
	readHeaderTimeout := 5 * time.Second

	// 3. Read Timeout (default 30s)
	readTimeout := 30 * time.Second
	if cfg.RequestTimeoutMs > 0 {
		readTimeout = time.Duration(cfg.RequestTimeoutMs) * time.Millisecond
	}

	// 4. Write Timeout (default RequestTimeoutMaxMs or 60s)
	writeTimeout := 60 * time.Second
	if cfg.RequestTimeoutMaxMs > 0 {
		writeTimeout = time.Duration(cfg.RequestTimeoutMaxMs) * time.Millisecond
	} else if cfg.RequestTimeoutMs > 0 {
		writeTimeout = time.Duration(cfg.RequestTimeoutMs) * time.Millisecond
	}

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           handler,
		IdleTimeout:       idleTimeout,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
	}

	return &Server{
		cfg:          cfg,
		httpServer:   httpSrv,
		shutdownChan: make(chan struct{}),
		shutdownDone: make(chan struct{}),
	}
}

// RegisterDrainHook registers a callback executed immediately when graceful shutdown begins,
// prior to the ALB drain delay and HTTP listener shutdown.
func (s *Server) RegisterDrainHook(hook func()) {
	if hook == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drainHooks = append(s.drainHooks, hook)
}

// HTTPServer exposes the underlying *http.Server instance.
func (s *Server) HTTPServer() *http.Server {
	return s.httpServer
}

// Config returns the HTTPConfig used to initialize the server.
func (s *Server) Config() config.HTTPConfig {
	return s.cfg
}

// Start begins listening and serving HTTP requests. It traps SIGTERM and SIGINT
// signals, executing graceful connection draining before termination.
// It returns nil on normal shutdown via Shutdown() or signal.
func (s *Server) Start() error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigChan)

	return s.startWithSignals(sigChan)
}

// StartWithSignalChannel starts the server with an explicit signal channel,
// enabling deterministic signal testing without signaling the process.
func (s *Server) StartWithSignalChannel(sigChan <-chan syscall.Signal) error {
	osSigChan := make(chan os.Signal, 1)
	go func() {
		for sig := range sigChan {
			osSigChan <- sig
		}
		close(osSigChan)
	}()

	return s.startWithSignals(osSigChan)
}

func (s *Server) finishShutdown(err error) {
	s.doneOnce.Do(func() {
		s.shutdownErr = err
		close(s.shutdownDone)
	})
}

func (s *Server) startWithSignals(sigChan <-chan os.Signal) error {
	go func() {
		select {
		case sig, ok := <-sigChan:
			if !ok {
				return
			}
			_ = sig

			forceTimeout := time.Duration(s.cfg.ShutdownForceMs) * time.Millisecond
			if forceTimeout <= 0 {
				forceTimeout = 5 * time.Second
			}
			drainTimeout := time.Duration(s.cfg.ShutdownDrainMs) * time.Millisecond

			ctx, cancel := context.WithTimeout(context.Background(), drainTimeout+forceTimeout)
			defer cancel()

			_ = s.Shutdown(ctx)
		case <-s.shutdownChan:
			return
		}
	}()

	err := s.httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		// Wait until s.Shutdown(ctx) has completely finished draining active connections
		<-s.shutdownDone
		return s.shutdownErr
	}

	// In case ListenAndServe failed to bind or failed with a non-closed error,
	// ensure the signal goroutine unblocks and exits.
	s.shutdownOnce.Do(func() {
		close(s.shutdownChan)
	})
	return err
}

// Shutdown initiates graceful shutdown:
// 1. Invokes registered drain hooks (e.g. marking health probes as draining).
// 2. Waits for ShutdownDrainMs (if configured) so in-flight requests and load balancers can drain.
// 3. Closes listeners and waits for active connections to finish or ctx deadline to expire.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		close(s.shutdownChan)
		s.mu.Lock()
		hooks := make([]func(), len(s.drainHooks))
		copy(hooks, s.drainHooks)
		s.mu.Unlock()
		for _, hook := range hooks {
			hook()
		}
	})

	if s.cfg.ShutdownDrainMs > 0 {
		select {
		case <-time.After(time.Duration(s.cfg.ShutdownDrainMs) * time.Millisecond):
		case <-ctx.Done():
			s.finishShutdown(ctx.Err())
			return ctx.Err()
		}
	}

	err := s.httpServer.Shutdown(ctx)
	s.finishShutdown(err)
	return err
}
