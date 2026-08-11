// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package telemetry

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/z5labs/avroc/internal/cli"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// request is one export as the collector received it.
type request struct {
	path        string
	contentType string
	headers     http.Header
	body        []byte
}

// collector is an OTLP/HTTP receiver a test can point avroc at.
//
// It is a real HTTP server rather than a fake exporter because the thing under
// test is the transport: that the request goes to the signal's path, carries the
// protobuf content type and holds the spans that were produced. A fake would
// assert that the code calls itself.
type collector struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []request
	status   int
}

func newCollector(t *testing.T) *collector {
	t.Helper()

	c := &collector{status: http.StatusOK}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		c.mu.Lock()
		c.requests = append(c.requests, request{
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			headers:     r.Header.Clone(),
			body:        body,
		})
		status := c.status
		c.mu.Unlock()

		w.WriteHeader(status)
	}))
	t.Cleanup(c.server.Close)

	return c
}

// endpoint is what OTEL_EXPORTER_OTLP_ENDPOINT would be set to: the base URL,
// with no signal path on it.
func (c *collector) endpoint() string {
	return c.server.URL
}

// fail makes every subsequent export fail with status.
func (c *collector) fail(status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = status
}

func (c *collector) recorded() []request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]request(nil), c.requests...)
}

// resourceSpans decodes every export the collector received.
//
// It peels the ExportTraceServiceRequest envelope the same way marshalRequest
// writes it, which is what makes this the round trip of that function and not
// merely a reader for it.
func (c *collector) resourceSpans(t *testing.T) []*tracepb.ResourceSpans {
	t.Helper()

	var out []*tracepb.ResourceSpans
	for _, req := range c.recorded() {
		remaining := req.body
		for len(remaining) > 0 {
			number, typ, read := protowire.ConsumeTag(remaining)
			if read < 0 {
				t.Fatalf("export body is not protobuf: %v", protowire.ParseError(read))
			}
			remaining = remaining[read:]
			if number != resourceSpansField || typ != protowire.BytesType {
				t.Fatalf("export body carries field %d of type %d, want field %d of type %d",
					number, typ, resourceSpansField, protowire.BytesType)
			}

			encoded, read := protowire.ConsumeBytes(remaining)
			if read < 0 {
				t.Fatalf("export body is not protobuf: %v", protowire.ParseError(read))
			}
			remaining = remaining[read:]

			var rs tracepb.ResourceSpans
			if err := proto.Unmarshal(encoded, &rs); err != nil {
				t.Fatalf("export body does not hold a ResourceSpans: %v", err)
			}
			out = append(out, &rs)
		}
	}
	return out
}

// spanNames is every span name the collector was sent, in the order it received
// them.
func (c *collector) spanNames(t *testing.T) []string {
	t.Helper()

	var names []string
	for _, rs := range c.resourceSpans(t) {
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				names = append(names, span.GetName())
			}
		}
	}
	return names
}

// syncBuffer is a log sink safe to read from the test while the SDK's own
// goroutine may still be writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newLogger() (*slog.Logger, *syncBuffer) {
	sink := &syncBuffer{}
	return slog.New(slog.NewTextHandler(sink, nil)), sink
}

// testContext is the cli.Context a test hands Start: an injected environment
// and a logger nothing else reads.
func testContext(log *slog.Logger, env map[string]string) cli.Context {
	return cli.Context{
		Log: log,
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		}),
	}
}
