// Package telemetry sets up OpenTelemetry tracing and carries trace context through
// Redis Streams, so one trace follows a request across every service.
package telemetry

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Init starts a tracer that exports to the OTLP gRPC endpoint (Jaeger). If OTLP_ENDPOINT
// is empty, tracing stays off and every span becomes a no-op. The returned function
// flushes pending spans and should be deferred.
func Init(ctx context.Context, service string) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	endpoint := os.Getenv("OTLP_ENDPOINT")
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Tracer returns the shared tracer.
func Tracer() trace.Tracer { return otel.Tracer("ainori") }

// Inject writes the trace context from ctx into stream fields as "traceparent".
func Inject(ctx context.Context, fields map[string]string) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(fields))
}

// Extract continues the trace carried in stream fields, or starts from ctx if none.
func Extract(ctx context.Context, fields map[string]string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(fields))
}
