package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// The point of the whole feature: a span started after reading from a stream must be a
// child of the span that wrote to it, in the same trace.
func TestTraceContinuesAcrossStreamFields(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})

	pubCtx, pubSpan := Tracer().Start(context.Background(), "publish")
	fields := map[string]string{"request_id": "r1"}
	Inject(pubCtx, fields)
	pubSpan.End()

	if fields["traceparent"] == "" {
		t.Fatal("traceparent was not written into the stream fields")
	}

	consCtx := Extract(context.Background(), fields)
	_, consSpan := Tracer().Start(consCtx, "consume")
	consSpan.End()

	ended := rec.Ended()
	if len(ended) != 2 {
		t.Fatalf("recorded %d spans, want 2", len(ended))
	}
	pub, cons := ended[0], ended[1]
	if cons.SpanContext().TraceID() != pub.SpanContext().TraceID() {
		t.Fatal("consumer span is in a different trace")
	}
	if cons.Parent().SpanID() != pub.SpanContext().SpanID() {
		t.Fatal("consumer span is not a child of the publisher span")
	}
	_ = trace.SpanKindConsumer
}

func TestExtractWithoutHeaderStartsFresh(t *testing.T) {
	ctx := Extract(context.Background(), map[string]string{"request_id": "r1"})
	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Fatal("no traceparent should mean no parent span")
	}
}
