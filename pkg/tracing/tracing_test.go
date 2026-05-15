package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestInit_DisabledViaEnv verifies the OTEL_TRACES_DISABLED escape
// hatch — used by local dev where Tempo is unreachable. Must return a
// non-nil no-op Shutdown without touching the global tracer provider
// or attempting an exporter connection.
func TestInit_DisabledViaEnv(t *testing.T) {
	t.Setenv("OTEL_TRACES_DISABLED", "1")

	shutdown, err := Init(context.Background(), "test-svc", "v0.0.0", "test-cluster")
	if err != nil {
		t.Fatalf("Init returned error in disabled mode: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned nil Shutdown in disabled mode")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("no-op Shutdown returned error: %v", err)
	}
}

// TestInit_PreWarmsExporter verifies the headline feature: Init emits
// + ForceFlushes a warmup span BEFORE returning, so the first real
// request after server-start doesn't race the OTLP exporter's lazy
// connection setup.
func TestInit_PreWarmsExporter(t *testing.T) {
	t.Setenv("OTEL_TRACES_DISABLED", "")

	var receivedCount int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&receivedCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// otlptracehttp.WithEndpoint takes host:port without scheme.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL[len("http://"):])

	shutdown, err := Init(context.Background(), "test-svc", "v0.0.0", "test-cluster")
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if got := atomic.LoadInt64(&receivedCount); got < 1 {
		t.Errorf("expected at least 1 export request from pre-warm, got %d", got)
	}
}

// TestInit_WithoutWarmup verifies the opt-out: when WithoutWarmup is
// supplied, Init does NOT emit a warmup span.
func TestInit_WithoutWarmup(t *testing.T) {
	t.Setenv("OTEL_TRACES_DISABLED", "")

	var receivedCount int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&receivedCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL[len("http://"):])

	shutdown, err := Init(
		context.Background(), "test-svc", "v0.0.0", "test-cluster",
		WithoutWarmup(),
	)
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	// Brief sleep to assert "no spans sent during Init" rather than
	// "no spans sent ever".
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt64(&receivedCount); got != 0 {
		t.Errorf("expected 0 export requests when warmup is disabled, got %d", got)
	}
}

// TestInit_WarmupGracefulOnUnreachable verifies that warmup failure
// doesn't break Init() — service should still start, traces are just
// lost on the first request.
func TestInit_WarmupGracefulOnUnreachable(t *testing.T) {
	t.Setenv("OTEL_TRACES_DISABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "127.0.0.1:1")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	shutdown, err := Init(ctx, "test-svc", "v0.0.0", "test-cluster",
		WithWarmupTimeout(500*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Init returned error despite warmup being non-fatal: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned nil Shutdown")
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer shutdownCancel()
	_ = shutdown(shutdownCtx)
}

// TestInit_WithEndpointOption verifies the WithEndpoint option
// overrides OTEL_EXPORTER_OTLP_ENDPOINT env. Important for services
// that build endpoint from non-env sources.
func TestInit_WithEndpointOption(t *testing.T) {
	t.Setenv("OTEL_TRACES_DISABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "wrong-host:1234")

	var receivedCount int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&receivedCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	shutdown, err := Init(
		context.Background(), "test-svc", "v0.0.0", "test-cluster",
		WithEndpoint(srv.URL[len("http://"):]),
	)
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if got := atomic.LoadInt64(&receivedCount); got < 1 {
		t.Errorf("expected WithEndpoint to override env, got %d requests", got)
	}
}
