package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName = "github.com/GizzZmo/autofix-engine/healer"
	meterName  = "github.com/GizzZmo/autofix-engine/healer"
)

// Telemetry holds tracers, meters, and instruments (polyglot OBSERVABILITY.md).
type Telemetry struct {
	Tracer trace.Tracer
	Meter  metric.Meter

	HealDuration    metric.Float64Histogram
	CheckDuration   metric.Float64Histogram
	WaybackDuration metric.Float64Histogram
	CircuitState    metric.Int64ObservableGauge
	CircuitTrips    metric.Int64Counter
	DiscoverReqs    metric.Int64Counter
	LinksTotal      metric.Int64Counter
	QueueDepth      metric.Int64ObservableGauge

	promHandler http.Handler
	tp          *sdktrace.TracerProvider
	mp          *sdkmetric.MeterProvider
}

func serviceResource() (*resource.Resource, error) {
	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("autofix-healer"),
			semconv.ServiceVersion("1.0.0"),
		),
	)
}

// InitTelemetry sets up OTLP (optional) + Prometheus /metrics.
// OTEL_EXPORTER_OTLP_ENDPOINT enables remote export (e.g. http://localhost:4318).
func InitTelemetry(ctx context.Context, queueDepthFn func() int64, circuitStateFn func() int64) (*Telemetry, error) {
	res, err := serviceResource()
	if err != nil {
		return nil, err
	}

	t := &Telemetry{}

	var spanExporters []sdktrace.SpanExporter
	if ep := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); ep != "" {
		opts := []otlptracehttp.Option{}
		if strings.HasPrefix(ep, "http://") || strings.HasPrefix(ep, "https://") {
			opts = append(opts, otlptracehttp.WithEndpointURL(ep))
		} else {
			opts = append(opts, otlptracehttp.WithEndpoint(ep), otlptracehttp.WithInsecure())
		}
		exp, err := otlptracehttp.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otlp trace exporter: %w", err)
		}
		spanExporters = append(spanExporters, exp)
	}

	var tpOpts []sdktrace.TracerProviderOption
	tpOpts = append(tpOpts, sdktrace.WithResource(res))
	for _, exp := range spanExporters {
		tpOpts = append(tpOpts, sdktrace.WithBatcher(exp))
	}
	t.tp = sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(t.tp)
	t.Tracer = t.tp.Tracer(tracerName)

	reg := prometheus.NewRegistry()
	promExp, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, fmt.Errorf("prometheus exporter: %w", err)
	}
	readers := []sdkmetric.Reader{promExp}

	if ep := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); ep != "" {
		opts := []otlpmetrichttp.Option{}
		if strings.HasPrefix(ep, "http://") || strings.HasPrefix(ep, "https://") {
			opts = append(opts, otlpmetrichttp.WithEndpointURL(ep))
		} else {
			opts = append(opts, otlpmetrichttp.WithEndpoint(ep), otlpmetrichttp.WithInsecure())
		}
		mexp, err := otlpmetrichttp.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otlp metric exporter: %w", err)
		}
		readers = append(readers, sdkmetric.NewPeriodicReader(mexp, sdkmetric.WithInterval(15*time.Second)))
	}

	var mpOpts []sdkmetric.Option
	mpOpts = append(mpOpts, sdkmetric.WithResource(res))
	for _, r := range readers {
		mpOpts = append(mpOpts, sdkmetric.WithReader(r))
	}
	t.mp = sdkmetric.NewMeterProvider(mpOpts...)
	otel.SetMeterProvider(t.mp)
	t.Meter = t.mp.Meter(meterName)
	t.promHandler = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})

	buckets := []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

	t.HealDuration, err = t.Meter.Float64Histogram(
		"autofix_heal_duration_seconds",
		metric.WithDescription("End-to-end heal path latency"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(buckets...),
	)
	if err != nil {
		return nil, err
	}
	t.CheckDuration, err = t.Meter.Float64Histogram(
		"autofix_check_duration_seconds",
		metric.WithDescription("Target URL check latency"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(buckets...),
	)
	if err != nil {
		return nil, err
	}
	t.WaybackDuration, err = t.Meter.Float64Histogram(
		"autofix_wayback_duration_seconds",
		metric.WithDescription("Wayback Availability API latency"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(buckets...),
	)
	if err != nil {
		return nil, err
	}
	t.CircuitTrips, err = t.Meter.Int64Counter(
		"autofix_circuit_trips_total",
		metric.WithDescription("Times breaker transitioned to open"),
	)
	if err != nil {
		return nil, err
	}
	t.DiscoverReqs, err = t.Meter.Int64Counter(
		"autofix_discover_requests_total",
		metric.WithDescription("Inbound /v1/discover requests"),
	)
	if err != nil {
		return nil, err
	}
	t.LinksTotal, err = t.Meter.Int64Counter(
		"autofix_links_total",
		metric.WithDescription("KV writes by status"),
	)
	if err != nil {
		return nil, err
	}

	t.CircuitState, err = t.Meter.Int64ObservableGauge(
		"autofix_circuit_state",
		metric.WithDescription("0=closed 1=half_open 2=open"),
		metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
			o.Observe(circuitStateFn(), metric.WithAttributes(attribute.String("name", "healer_wayback")))
			return nil
		}),
	)
	if err != nil {
		return nil, err
	}
	t.QueueDepth, err = t.Meter.Int64ObservableGauge(
		"autofix_queue_depth",
		metric.WithDescription("Approximate pending discovery URLs"),
		metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
			o.Observe(queueDepthFn())
			return nil
		}),
	)
	if err != nil {
		return nil, err
	}

	return t, nil
}

func (t *Telemetry) Shutdown(ctx context.Context) {
	if t == nil {
		return
	}
	if t.tp != nil {
		_ = t.tp.Shutdown(ctx)
	}
	if t.mp != nil {
		_ = t.mp.Shutdown(ctx)
	}
}

func (t *Telemetry) PromHandler() http.Handler {
	if t == nil || t.promHandler == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("telemetry not ready\n"))
		})
	}
	return t.promHandler
}

func setupSlog() {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(h))
}

func logEvent(ctx context.Context, level slog.Level, event string, attrs ...any) {
	base := []any{
		"event", event,
		"component", "healer",
		"layer", "L3",
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		base = append(base, "trace_id", sc.TraceID().String())
	}
	base = append(base, attrs...)
	slog.Log(ctx, level, event, base...)
}

func recordSpanError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

func circuitStateValue(state string) int64 {
	switch state {
	case "open":
		return 2
	case "half_open":
		return 1
	default:
		return 0
	}
}
