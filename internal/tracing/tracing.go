// Package tracing wires OpenTelemetry spans through the TUI's hot paths
// with a zero-cost default: when KARGO_TUI_TRACE_FILE is unset the global
// tracer stays the OTel no-op tracer, so every Tracer().Start call
// dispatches into a noop that allocates nothing and returns instantly.
// Call sites are written without any "if enabled" guards; the guard lives
// here in Init.
//
// When the env var is set, spans are written one-per-line as compact JSON
// into the file at that path. Designed for an AI assistant (or the user
// with grep) to consume directly: each line is self-describing, the
// resource block is dropped because it would dominate every line, and
// timestamps are RFC3339Nano so duration math is straightforward.
//
// Not a general-purpose tracing setup. Bubble Tea's Update/View pipeline
// does not carry a context.Context through the message loop, so spans
// don't propagate across the Update/View boundary. Each top-level span
// starts from context.Background and lives for the duration of one
// Update or one View call. Child spans within that call chain through
// the returned context like normal.
package tracing

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is what call sites pass to otel.Tracer. Cached by the OTel
// global so calling Tracer() repeatedly is free.
const tracerName = "kargo-tui"

// envTraceFile is the only knob. Set to a file path to enable export;
// leave unset for zero-cost noop.
const envTraceFile = "KARGO_TUI_TRACE_FILE"

// Init configures the global tracer provider. Returns a shutdown
// function that flushes pending spans and closes the export file. When
// envTraceFile is unset, returns a no-op shutdown and leaves the global
// tracer as OTel's noop.
func Init(serviceName, version string) (shutdown func(context.Context) error, err error) {
	path := os.Getenv(envTraceFile)
	if path == "" {
		return func(context.Context) error { return nil }, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, errors.Wrap(err, "open trace file")
	}
	exp := &jsonlExporter{w: f}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(version),
	))
	if err != nil {
		_ = f.Close()
		return nil, errors.Wrap(err, "build resource")
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp,
			sdktrace.WithBatchTimeout(time.Second),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	return func(ctx context.Context) error {
		sdErr := tp.Shutdown(ctx)
		fErr := f.Close()
		if sdErr != nil {
			return errors.Wrap(sdErr, "shutdown tracer provider")
		}
		return errors.Wrap(fErr, "close trace file")
	}, nil
}

// Tracer returns the package's tracer. When tracing is disabled this is
// the OTel global noop tracer; every Start call on it is zero-cost.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// Start opens a span on the package tracer. Equivalent to
// Tracer().Start(ctx, name, trace.WithAttributes(attrs...)) but avoids
// constructing the attrs slice on the noop path.
//
// Attributes that are expensive to compute (e.g. fmt.Sprintf("%T", msg))
// should be guarded with span.IsRecording() at the call site instead of
// passed here, because Go evaluates the attribute values before the call
// regardless of whether tracing is on.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if len(attrs) == 0 {
		return Tracer().Start(ctx, name)
	}
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// ambientCtx holds a parent context for code paths that cannot thread
// ctx through their function signatures (e.g. paintFrame, called from
// 14 render-path methods). The render loop (View) calls SetAmbient at
// entry and ClearAmbient on exit, so helper spans nested inside View
// can pick up the parent without us refactoring every render method to
// take a ctx parameter.
//
// Safe because bubbletea calls Update/View serially on a single
// goroutine. The variable is only mutated from that goroutine.
//
// Callers MUST pair SetAmbient with ClearAmbient (typically via defer)
// so a leftover ambient ctx doesn't parent unrelated spans from a later
// loop iteration.
var ambientCtx context.Context

// SetAmbient installs ctx as the parent for AmbientStart calls. Returns
// a reset function the caller defers to restore the previous value.
func SetAmbient(ctx context.Context) func() {
	prev := ambientCtx
	ambientCtx = ctx
	return func() { ambientCtx = prev }
}

// AmbientStart opens a span under the currently-installed ambient ctx,
// or under context.Background when none is installed. Use only from
// code paths that can't take a ctx parameter; prefer Start(ctx, ...)
// everywhere else.
func AmbientStart(name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	parent := ambientCtx
	if parent == nil {
		parent = context.Background()
	}
	return Start(parent, name, attrs...)
}

// jsonlExporter writes one JSON line per span to an io.Writer. Schema is
// stable and intentionally flat:
//
//	{"trace_id":"...","span_id":"...","parent_span_id":"...",
//	 "name":"Update","start":"2026-06-10T17:02:11.123Z",
//	 "duration_ms":15.234,"attrs":{...},"status":"OK"}
//
// duration_ms is a float, not a string, so it sorts and filters cleanly.
// parent_span_id is "" for top-level spans (OTel uses all-zeroes which
// would be noisy to read). Attribute values are coerced to their natural
// JSON type (string/int/float/bool) rather than wrapping in OTel's
// (type, value) struct.
type jsonlExporter struct {
	mu sync.Mutex
	w  io.Writer
}

type jsonlSpan struct {
	TraceID      string         `json:"trace_id"`
	SpanID       string         `json:"span_id"`
	ParentSpanID string         `json:"parent_span_id"`
	Name         string         `json:"name"`
	Start        string         `json:"start"`
	DurationMS   float64        `json:"duration_ms"`
	Attrs        map[string]any `json:"attrs,omitempty"`
	Status       string         `json:"status,omitempty"`
}

func (e *jsonlExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	enc := json.NewEncoder(e.w)
	for _, s := range spans {
		parent := ""
		if p := s.Parent(); p.IsValid() {
			parent = p.SpanID().String()
		}
		attrs := map[string]any{}
		for _, kv := range s.Attributes() {
			attrs[string(kv.Key)] = kv.Value.AsInterface()
		}
		out := jsonlSpan{
			TraceID:      s.SpanContext().TraceID().String(),
			SpanID:       s.SpanContext().SpanID().String(),
			ParentSpanID: parent,
			Name:         s.Name(),
			Start:        s.StartTime().UTC().Format(time.RFC3339Nano),
			DurationMS:   float64(s.EndTime().Sub(s.StartTime()).Microseconds()) / 1000.0,
		}
		if len(attrs) > 0 {
			out.Attrs = attrs
		}
		if s.Status().Code != 0 {
			out.Status = s.Status().Code.String()
		}
		if err := enc.Encode(out); err != nil {
			return errors.Wrap(err, "encode span")
		}
	}
	return nil
}

func (e *jsonlExporter) Shutdown(context.Context) error {
	// File close is handled by Init's returned shutdown; the exporter
	// itself does not own the writer's lifecycle.
	return nil
}
