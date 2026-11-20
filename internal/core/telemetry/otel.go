package telemetry

import (
	"context"
	"strings"

	"go.opentelemetry.io/contrib/propagators/aws/xray"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"go_backend_boilerplate/internal/config"
)

// InitTracer initializes the OpenTelemetry TracerProvider and registers global
// propagators (W3C TraceContext, W3C Baggage, and AWS X-Ray).
//
// If cfg.OTelExporterEndpoint is non-empty, an OTLP gRPC span exporter is configured.
// If cfg.OTelExporterEndpoint is empty, an in-memory no-op tracer provider is configured
// without failing application startup.
//
// Returns a shutdown callback function that flushes pending spans on graceful termination.
func InitTracer(ctx context.Context, cfg config.TelemetryConfig) (func(context.Context) error, error) {
	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = "go_backend_boilerplate"
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
		resource.WithHost(),
	)
	if err != nil {
		res = resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(serviceName),
		)
	}

	var tpOptions []sdktrace.TracerProviderOption
	tpOptions = append(tpOptions,
		sdktrace.WithResource(res),
		sdktrace.WithIDGenerator(xray.NewIDGenerator()),
	)

	if cfg.OTelExporterEndpoint != "" {
		var grpcOpts []otlptracegrpc.Option
		endpoint := cfg.OTelExporterEndpoint

		if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithEndpointURL(endpoint))
			if strings.HasPrefix(endpoint, "http://") {
				grpcOpts = append(grpcOpts, otlptracegrpc.WithInsecure())
			}
		} else {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
		}

		exporter, expErr := otlptracegrpc.New(ctx, grpcOpts...)
		if expErr != nil {
			return nil, expErr
		}

		tpOptions = append(tpOptions, sdktrace.WithBatcher(exporter))
	}

	tp := sdktrace.NewTracerProvider(tpOptions...)

	// Configure global text map propagator composite: W3C TraceContext, Baggage, AWS X-Ray
	compositePropagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
		xray.Propagator{},
	)
	otel.SetTextMapPropagator(compositePropagator)
	otel.SetTracerProvider(tp)

	shutdown := func(shutdownCtx context.Context) error {
		return tp.Shutdown(shutdownCtx)
	}

	return shutdown, nil
}
