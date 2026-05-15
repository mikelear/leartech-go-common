// Package tracing initialises OpenTelemetry trace export to a Tempo
// (or any OTLP-HTTP) backend, with two cold-start race mitigations
// that are normally invisible until a regression hits at the wrong
// moment:
//
//  1. BatchSpanProcessor timeout is 1 second (not the SDK default 5s).
//     A short-lived smoke test that calls a low-frequency endpoint
//     ONCE during a tight test window must flush its single span
//     fast — at 5s the batch timer may not fire before the test
//     window closes and a post-deploy gate queries the trace store.
//
//  2. Pre-warm: Init emits + ForceFlushes a single dummy span BEFORE
//     returning. This forces DNS + TCP + HTTP/2 handshake + the first
//     OTLP receive to happen during startup, not racing the first
//     real request. Accepts up to ~3s startup slowdown for
//     deterministic post-startup tracing.
//
// Originally proven in leartech-qa-canary 2026-05-15 (see
// qa-architecture/tier-2-demo.md Finding #5). Extracted here so every
// leartech service inherits the fix instead of each maintaining its
// own copy.
//
// Usage (minimal):
//
//	shutdown, err := tracing.Init(ctx, "my-service", version, clusterTag)
//	if err != nil { ... }
//	defer shutdown(context.Background())
//
// Usage (with options):
//
//	shutdown, err := tracing.Init(ctx, "my-service", version, clusterTag,
//	    tracing.WithEndpoint("tempo.observability:4318"),
//	    tracing.WithBatchTimeout(500*time.Millisecond),
//	    tracing.WithoutWarmup(), // opt out for services that don't need it
//	)
//
// Defaults assume in-cluster deployment in jx-staging:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT  default: tempo.jx-observability:4318
//	OTEL_TRACES_DISABLED         set to "1" to no-op (local dev)
package tracing

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Default values — see package doc for rationale.
const (
	defaultEndpoint      = "tempo.jx-observability:4318"
	defaultBatchTimeout  = 1 * time.Second
	defaultWarmupTimeout = 3 * time.Second
)

// Shutdown is the type returned by [Init]. Call before the process
// exits to flush pending spans. Safe to call multiple times (the
// underlying TracerProvider.Shutdown is idempotent).
type Shutdown func(context.Context) error

// Option configures Init. Compose multiple options to override defaults.
type Option func(*config)

type config struct {
	endpoint      string
	batchTimeout  time.Duration
	warmupTimeout time.Duration
	skipWarmup    bool
	sampler       sdktrace.Sampler
}

// WithEndpoint overrides the OTLP-HTTP endpoint. Format: "host:port"
// without scheme (otlptracehttp.WithEndpoint adds http:// when
// otlptracehttp.WithInsecure is set, which Init does). When unset,
// OTEL_EXPORTER_OTLP_ENDPOINT env is consulted, then the package
// default (tempo.jx-observability:4318) is used.
func WithEndpoint(endpoint string) Option {
	return func(c *config) { c.endpoint = endpoint }
}

// WithBatchTimeout overrides how long BatchSpanProcessor will wait
// before flushing pending spans. Default 1s — see package doc.
func WithBatchTimeout(d time.Duration) Option {
	return func(c *config) { c.batchTimeout = d }
}

// WithWarmupTimeout overrides the deadline for the pre-warm
// ForceFlush. Default 3s.
func WithWarmupTimeout(d time.Duration) Option {
	return func(c *config) { c.warmupTimeout = d }
}

// WithoutWarmup disables the pre-warm. Use for services that don't
// need deterministic first-request tracing (long-lived workers,
// batch jobs, etc.). Most HTTP services should keep the warmup.
func WithoutWarmup() Option {
	return func(c *config) { c.skipWarmup = true }
}

// WithSampler overrides the default AlwaysSample sampler. Useful for
// high-traffic production services that need ratio-based sampling.
func WithSampler(s sdktrace.Sampler) Option {
	return func(c *config) { c.sampler = s }
}

// Init configures the global OpenTelemetry TracerProvider and returns
// a Shutdown function. Honours OTEL_TRACES_DISABLED=1 as a local-dev
// escape hatch (returns a no-op Shutdown and configures nothing).
//
// serviceName, version, clusterTag are required — they become the
// service.name, service.version, and cluster resource attributes on
// every emitted span, which downstream consumers (forensics-runner,
// tempo-to-har, etc.) filter on.
func Init(ctx context.Context, serviceName, version, clusterTag string, opts ...Option) (Shutdown, error) {
	if os.Getenv("OTEL_TRACES_DISABLED") == "1" {
		return func(context.Context) error { return nil }, nil
	}

	cfg := config{
		endpoint:      os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		batchTimeout:  defaultBatchTimeout,
		warmupTimeout: defaultWarmupTimeout,
		sampler:       sdktrace.AlwaysSample(),
	}
	if cfg.endpoint == "" {
		cfg.endpoint = defaultEndpoint
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(cfg.endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
			attribute.String("cluster", clusterTag),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(cfg.batchTimeout)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(cfg.sampler),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if !cfg.skipWarmup {
		warmCtx, warmCancel := context.WithTimeout(ctx, cfg.warmupTimeout)
		defer warmCancel()
		_, warmSpan := tp.Tracer("tracer-warmup").Start(warmCtx, "tracer.warmup")
		warmSpan.End()
		if err := tp.ForceFlush(warmCtx); err != nil {
			// Non-fatal: warmup failure shouldn't block server start.
			// Service still traces requests; first one just hits the
			// cold-start race. Log loud so regressions are visible if
			// Tempo is degraded.
			fmt.Fprintf(os.Stderr, "tracing: warmup ForceFlush failed (non-fatal): %v\n", err)
		}
	}

	return Shutdown(tp.Shutdown), nil
}
