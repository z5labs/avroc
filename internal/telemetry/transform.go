// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package telemetry

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// resourceSpans groups a batch of finished spans into the shape OTLP carries
// them in: one entry per resource, each holding one entry per instrumentation
// scope.
//
// The grouping is by linear search over slices rather than by map, so the
// resources and the scopes come out in the order the batch first mentioned them.
// The collections are two and three long in practice, so the search costs
// nothing, and the alternative is an encoding whose bytes depend on Go's map
// iteration order — the way every other ordering bug in this repository was
// introduced.
func resourceSpans(spans []sdktrace.ReadOnlySpan) []*tracepb.ResourceSpans {
	var (
		resources []*resource.Resource
		out       []*tracepb.ResourceSpans
	)

	for _, span := range spans {
		if span == nil {
			continue
		}

		res := span.Resource()
		index := indexOfResource(resources, res)
		if index < 0 {
			resources = append(resources, res)
			out = append(out, &tracepb.ResourceSpans{
				Resource:  resourceProto(res),
				SchemaUrl: schemaURL(res),
			})
			index = len(out) - 1
		}

		rs := out[index]
		scope := span.InstrumentationScope()
		ss := scopeSpansFor(rs, scope)
		ss.Spans = append(ss.Spans, spanProto(span))
	}

	return out
}

// indexOfResource finds a resource already seen in this batch, by identity
// first and by equality second.
//
// Every span from one tracer provider carries the same *resource.Resource, so
// identity answers it in one comparison. Equality is the fallback for a batch a
// test assembled by hand from separate but equal resources, which would
// otherwise be reported as two resources that a collector then has to reconcile.
func indexOfResource(seen []*resource.Resource, res *resource.Resource) int {
	for i, candidate := range seen {
		if candidate == res {
			return i
		}
		if candidate != nil && res != nil && candidate.Equal(res) {
			return i
		}
	}
	return -1
}

// scopeSpansFor returns the ScopeSpans within rs that scope's spans belong in,
// appending a new one the first time a scope is seen.
func scopeSpansFor(rs *tracepb.ResourceSpans, scope instrumentation.Scope) *tracepb.ScopeSpans {
	for _, ss := range rs.GetScopeSpans() {
		if ss.GetScope().GetName() == scope.Name &&
			ss.GetScope().GetVersion() == scope.Version &&
			ss.GetSchemaUrl() == scope.SchemaURL {
			return ss
		}
	}

	ss := &tracepb.ScopeSpans{
		Scope: &commonpb.InstrumentationScope{
			Name:       scope.Name,
			Version:    scope.Version,
			Attributes: keyValues(scope.Attributes.ToSlice()),
		},
		SchemaUrl: scope.SchemaURL,
	}
	rs.ScopeSpans = append(rs.ScopeSpans, ss)
	return ss
}

func schemaURL(res *resource.Resource) string {
	if res == nil {
		return ""
	}
	return res.SchemaURL()
}

func resourceProto(res *resource.Resource) *resourcepb.Resource {
	if res == nil {
		return nil
	}
	return &resourcepb.Resource{Attributes: keyValues(res.Attributes())}
}

// spanProto converts one finished span.
//
// The identifiers are copied out of the span context into local arrays before
// they are sliced: [trace.SpanContext.TraceID] returns a value, and slicing a
// value returned by a call is not something Go will let you keep.
func spanProto(span sdktrace.ReadOnlySpan) *tracepb.Span {
	sc := span.SpanContext()
	traceID := sc.TraceID()
	spanID := sc.SpanID()

	out := &tracepb.Span{
		TraceId:                traceID[:],
		SpanId:                 spanID[:],
		TraceState:             sc.TraceState().String(),
		Name:                   span.Name(),
		Kind:                   spanKind(span.SpanKind()),
		StartTimeUnixNano:      uint64(span.StartTime().UnixNano()), //nolint:gosec // a time after 1970 is not negative
		EndTimeUnixNano:        uint64(span.EndTime().UnixNano()),   //nolint:gosec // as above
		Attributes:             keyValues(span.Attributes()),
		DroppedAttributesCount: count(span.DroppedAttributes()),
		Events:                 events(span.Events()),
		DroppedEventsCount:     count(span.DroppedEvents()),
		Links:                  links(span.Links()),
		DroppedLinksCount:      count(span.DroppedLinks()),
		Status:                 status(span.Status()),
	}

	if parent := span.Parent(); parent.IsValid() {
		parentID := parent.SpanID()
		out.ParentSpanId = parentID[:]
	}

	return out
}

// count narrows one of the SDK's dropped-item counts to the width OTLP carries
// it at. The SDK never reports a negative one, and a negative one wrapping to
// four billion would be worse than the zero it is clamped to.
func count(n int) uint32 {
	if n < 0 {
		return 0
	}
	return uint32(n) //nolint:gosec // clamped above
}

func spanKind(kind trace.SpanKind) tracepb.Span_SpanKind {
	switch kind {
	case trace.SpanKindInternal:
		return tracepb.Span_SPAN_KIND_INTERNAL
	case trace.SpanKindServer:
		return tracepb.Span_SPAN_KIND_SERVER
	case trace.SpanKindClient:
		return tracepb.Span_SPAN_KIND_CLIENT
	case trace.SpanKindProducer:
		return tracepb.Span_SPAN_KIND_PRODUCER
	case trace.SpanKindConsumer:
		return tracepb.Span_SPAN_KIND_CONSUMER
	default:
		return tracepb.Span_SPAN_KIND_UNSPECIFIED
	}
}

func status(s sdktrace.Status) *tracepb.Status {
	out := &tracepb.Status{Message: s.Description}
	switch s.Code {
	case codes.Ok:
		out.Code = tracepb.Status_STATUS_CODE_OK
		// The specification gives the description to the error case alone, so a
		// description set on a span that then succeeded is dropped rather than
		// shipped as an explanation of a success.
		out.Message = ""
	case codes.Error:
		out.Code = tracepb.Status_STATUS_CODE_ERROR
	default:
		out.Code = tracepb.Status_STATUS_CODE_UNSET
		out.Message = ""
	}
	return out
}

func events(in []sdktrace.Event) []*tracepb.Span_Event {
	if len(in) == 0 {
		return nil
	}

	out := make([]*tracepb.Span_Event, 0, len(in))
	for _, event := range in {
		out = append(out, &tracepb.Span_Event{
			Name:                   event.Name,
			TimeUnixNano:           uint64(event.Time.UnixNano()), //nolint:gosec // a time after 1970 is not negative
			Attributes:             keyValues(event.Attributes),
			DroppedAttributesCount: count(event.DroppedAttributeCount),
		})
	}
	return out
}

func links(in []sdktrace.Link) []*tracepb.Span_Link {
	if len(in) == 0 {
		return nil
	}

	out := make([]*tracepb.Span_Link, 0, len(in))
	for _, link := range in {
		traceID := link.SpanContext.TraceID()
		spanID := link.SpanContext.SpanID()
		out = append(out, &tracepb.Span_Link{
			TraceId:                traceID[:],
			SpanId:                 spanID[:],
			TraceState:             link.SpanContext.TraceState().String(),
			Attributes:             keyValues(link.Attributes),
			DroppedAttributesCount: count(link.DroppedAttributeCount),
		})
	}
	return out
}

// keyValues converts attributes, preserving their order. The SDK does not
// promise one, but it does not scramble one either, and reordering here would
// add a second source of variation on top of whatever the SDK did.
func keyValues(in []attribute.KeyValue) []*commonpb.KeyValue {
	if len(in) == 0 {
		return nil
	}

	out := make([]*commonpb.KeyValue, 0, len(in))
	for _, kv := range in {
		out = append(out, &commonpb.KeyValue{
			Key:   string(kv.Key),
			Value: anyValue(kv.Value),
		})
	}
	return out
}

// anyValue converts one attribute value.
//
// [attribute.Value] is a closed set of eight types plus INVALID, and every one
// of them is handled: an attribute that arrived as a value and left as nothing
// would be a hole in a trace that only shows up in a collector.
func anyValue(value attribute.Value) *commonpb.AnyValue {
	switch value.Type() {
	case attribute.BOOL:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: value.AsBool()}}
	case attribute.INT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value.AsInt64()}}
	case attribute.FLOAT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: value.AsFloat64()}}
	case attribute.STRING:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value.AsString()}}
	case attribute.BOOLSLICE:
		return arrayValue(value.AsBoolSlice(), func(v bool) *commonpb.AnyValue {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v}}
		})
	case attribute.INT64SLICE:
		return arrayValue(value.AsInt64Slice(), func(v int64) *commonpb.AnyValue {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}
		})
	case attribute.FLOAT64SLICE:
		return arrayValue(value.AsFloat64Slice(), func(v float64) *commonpb.AnyValue {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v}}
		})
	case attribute.STRINGSLICE:
		return arrayValue(value.AsStringSlice(), func(v string) *commonpb.AnyValue {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}
		})
	default:
		// INVALID, and anything a future SDK adds. Its rendering is what
		// attribute.Value itself would print, which is more use to whoever is
		// reading the trace than an absent attribute.
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value.String()}}
	}
}

func arrayValue[T any](values []T, convert func(T) *commonpb.AnyValue) *commonpb.AnyValue {
	array := &commonpb.ArrayValue{Values: make([]*commonpb.AnyValue, 0, len(values))}
	for _, value := range values {
		array.Values = append(array.Values, convert(value))
	}
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: array}}
}
