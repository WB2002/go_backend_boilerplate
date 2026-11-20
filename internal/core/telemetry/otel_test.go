package telemetry_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/telemetry"
)

func captureFirstBytes(t *testing.T, buildCfg func(addr string) config.TelemetryConfig) []byte {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	firstBytesCh := make(chan []byte, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 24)
		n, _ := conn.Read(buf)
		firstBytesCh <- buf[:n]
	}()

	cfg := buildCfg(ln.Addr().String())
	shutdown, err := telemetry.InitTracer(context.Background(), cfg)
	require.NoError(t, err)

	_, span := otel.Tracer("otel-tls-test").Start(context.Background(), "probe-span")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = shutdown(ctx)

	select {
	case b := <-firstBytesCh:
		return b
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OTLP exporter TCP connection")
		return nil
	}
}

func TestInitTracer_RequiresTLSByDefaultForHostPort(t *testing.T) {
	firstBytes := captureFirstBytes(t, func(addr string) config.TelemetryConfig {
		return config.TelemetryConfig{
			ServiceName:          "tls-default-test",
			OTelExporterEndpoint: addr,
		}
	})

	require.NotEmpty(t, firstBytes)
	// TLS ClientHello record starts with content type 0x16 (handshake)
	assert.Equal(t, byte(0x16), firstBytes[0], "expected TLS ClientHello handshake by default, got %q", string(firstBytes))
}

func TestInitTracer_AllowsPlaintextWhenInsecureOptedIn(t *testing.T) {
	firstBytes := captureFirstBytes(t, func(addr string) config.TelemetryConfig {
		return config.TelemetryConfig{
			ServiceName:          "insecure-opt-in-test",
			OTelExporterEndpoint: addr,
			OTelExporterInsecure: true,
		}
	})

	require.GreaterOrEqual(t, len(firstBytes), 14)
	assert.Equal(t, "PRI * HTTP/2.0", string(firstBytes[:14]))
}

func TestInitTracer_AllowsPlaintextForHTTPURL(t *testing.T) {
	firstBytes := captureFirstBytes(t, func(addr string) config.TelemetryConfig {
		return config.TelemetryConfig{
			ServiceName:          "http-scheme-test",
			OTelExporterEndpoint: "http://" + addr,
		}
	})

	require.GreaterOrEqual(t, len(firstBytes), 14)
	assert.Equal(t, "PRI * HTTP/2.0", string(firstBytes[:14]))
}
