// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package telemetry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// exporter posts spans to an OTLP receiver as protobuf over HTTP.
//
// # Why this is written here rather than imported
//
// The obvious implementation is go.opentelemetry.io/otel/exporters/otlp/
// otlptrace/otlptracehttp, and it was the first one tried. It transitively
// imports go.opentelemetry.io/proto/otlp/collector/trace/v1 for one message
// type, and that package carries the generated gRPC service and grpc-gateway
// stubs alongside it — so an HTTP-only exporter links grpc-go, grpc-gateway and
// their dependency trees into an executable that ships inside a scratch image,
// which is the cost avroc chose OTLP over HTTP to avoid in the first place.
//
// What is left once that package is out of the way is small enough to own: the
// OTLP data messages (trace/v1, common/v1, resource/v1) carry no service
// definition and no gRPC, the transform below is mechanical, and the request
// envelope is one repeated field, appended with protowire rather than pulled in
// as a dependency. See marshalRequest.
//
// It is deliberately thin. There is no retry, no compression and no queue: the
// SDK's batch span processor already owns batching and the timeout, and a build
// tool that spent thirty seconds retrying against a collector that is not there
// would be the failure mode this whole package is arranged to avoid.
type exporter struct {
	client   *http.Client
	endpoint string
	headers  map[string]string

	// stopped makes a post-shutdown export a no-op rather than a request. The
	// batch processor does not issue one, but Shutdown is reachable from
	// anywhere and an exporter that answers "no" is cheaper to reason about than
	// one that must not be asked.
	stopped atomic.Bool
}

// newExporter builds the exporter cfg describes. It opens no connection: the
// first one is made by the first export, which is what makes a configured but
// unused endpoint cost nothing.
func newExporter(cfg config) *exporter {
	// A zero timeout is no timeout at all in net/http, which is the one thing it
	// must not be allowed to mean: an unconfigured exporter is the one most
	// likely to be pointed at a collector that will never answer.
	timeout := cfg.exportTimeout
	if timeout <= 0 {
		timeout = ExportTimeout
	}

	return &exporter{
		client:   &http.Client{Timeout: timeout},
		endpoint: cfg.endpoint,
		headers:  cfg.headers,
	}
}

// ExportSpans implements [sdktrace.SpanExporter].
func (e *exporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if e.stopped.Load() || len(spans) == 0 {
		return nil
	}

	body, err := marshalRequest(resourceSpans(spans))
	if err != nil {
		return fmt.Errorf("failed to encode %d spans: %w", len(spans), err)
	}
	// A batch that held nothing the transform could carry — every element nil —
	// encodes to no bytes, and a request with an empty body is one a collector is
	// entitled to reject. There is nothing to export, so nothing is posted: the
	// emptiness is decided after the transform rather than before it, because the
	// transform is the thing that knows what survived.
	if len(body) == 0 {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	for key, value := range e.headers {
		req.Header.Set(key, value)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to export spans to %s: %w", e.endpoint, err)
	}
	defer func() {
		// Drained before closing so that the connection can be reused: a run
		// exports more than once, and a fresh TCP connection per batch is a cost
		// paid on the machine that is trying to build something.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("collector at %s responded %s", e.endpoint, resp.Status)
	}
	return nil
}

// Shutdown implements [sdktrace.SpanExporter].
func (e *exporter) Shutdown(context.Context) error {
	e.stopped.Store(true)
	e.client.CloseIdleConnections()
	return nil
}

// resourceSpansField is ExportTraceServiceRequest's only field:
//
//	message ExportTraceServiceRequest {
//	  repeated ResourceSpans resource_spans = 1;
//	}
const resourceSpansField = protowire.Number(1)

// marshalRequest encodes the OTLP trace export request.
//
// The message it produces is ExportTraceServiceRequest, which lives in the one
// package this file exists to avoid importing. A repeated message field is a
// tag, a length and the encoded message, once per element, so the envelope is
// written directly with protowire — the encoding is protobuf's and is fixed by
// the wire format rather than by anything either package chose.
//
// Marshalling is deterministic, so two exports of the same spans produce the
// same bytes. Nothing downstream requires it; it costs nothing and it makes a
// recorded request comparable.
func marshalRequest(spans []*tracepb.ResourceSpans) ([]byte, error) {
	opts := proto.MarshalOptions{Deterministic: true}

	var buf []byte
	for _, rs := range spans {
		encoded, err := opts.Marshal(rs)
		if err != nil {
			return nil, err
		}
		buf = protowire.AppendTag(buf, resourceSpansField, protowire.BytesType)
		buf = protowire.AppendBytes(buf, encoded)
	}
	return buf, nil
}
