// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package telemetry

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/z5labs/avroc/internal/cli"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// The OpenTelemetry environment variables avroc reads, and the only ones it
// reads.
//
// The signal-specific forms (OTEL_EXPORTER_OTLP_TRACES_ENDPOINT and friends) are
// deliberately absent rather than half-present: honouring one of them and not
// the others is the configuration that looks like it worked. They are a set, and
// a later story may add the set.
const (
	envSDKDisabled    = "OTEL_SDK_DISABLED"
	envServiceName    = "OTEL_SERVICE_NAME"
	envResourceAttrs  = "OTEL_RESOURCE_ATTRIBUTES"
	envOTLPEndpoint   = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTLPProtocol   = "OTEL_EXPORTER_OTLP_PROTOCOL"
	envOTLPHeaders    = "OTEL_EXPORTER_OTLP_HEADERS"
	envTracesSampler  = "OTEL_TRACES_SAMPLER"
	envTracesSamplerA = "OTEL_TRACES_SAMPLER_ARG"
)

const (
	// DefaultServiceName is what service.name is when the environment does not
	// say. An operator who sets nothing still gets a name a collector can group
	// by, and it is the name of the executable rather than of the host or the
	// user, both of which vary between two runs of the same build.
	DefaultServiceName = "avroc"

	// ShutdownTimeout bounds the final flush. It is short on purpose: a
	// collector that has stopped answering must not hold up the exit status of a
	// build, and a person waiting on `go generate` notices five seconds.
	ShutdownTimeout = 5 * time.Second

	// ExportTimeout bounds one export request, and is shorter than
	// ShutdownTimeout so that a request begun during the flush can fail and be
	// reported inside it rather than being cut off by it.
	ExportTimeout = 3 * time.Second

	// protocolHTTPProtobuf is the one OTLP protocol this build speaks.
	//
	// gRPC is not the default and is not supported at all: otlptracegrpc brings
	// grpc-go and its dependency tree into an executable that ships inside a
	// scratch image, for a transport that carries the same bytes as the one
	// net/http already carries. Honouring OTEL_EXPORTER_OTLP_PROTOCOL=grpc is a
	// thing a later story may add.
	protocolHTTPProtobuf = "http/protobuf"

	// tracesPath is what the OTLP specification appends to a base endpoint for
	// the trace signal.
	tracesPath = "/v1/traces"
)

// config is the resolved telemetry configuration: everything read out of the
// environment, in the form the provider needs it.
//
// It is resolved in one pass and validated there, so that [Start] either has a
// configuration it can honour completely or has an error naming what it could
// not.
type config struct {
	// enabled is false for every off case there is — disabled by the operator,
	// no endpoint configured — so that Start has one thing to test.
	enabled bool

	// endpoint is the absolute URL of the traces receiver, signal path included.
	endpoint string

	headers map[string]string

	serviceName string
	resource    *resource.Resource
	sampler     sdktrace.Sampler
}

// configFromEnv resolves the configuration from env, which is
// [cli.Context]'s environment and never the process's.
//
// A nil environment is an empty one rather than a panic: cli.Context is
// constructed by tests as well as by main, and "no environment" and "an
// environment with none of these variables in it" describe the same run.
func configFromEnv(env cli.Environment, version string) (config, error) {
	lookup := func(key string) string {
		if env == nil {
			return ""
		}
		value, ok := env.LookupEnv(key)
		if !ok {
			return ""
		}
		return strings.TrimSpace(value)
	}

	// The two off switches come first, and neither of them reads anything else:
	// a run that is not exporting must not be able to fail on the syntax of a
	// variable it was never going to use.
	if strings.EqualFold(lookup(envSDKDisabled), "true") {
		return config{}, nil
	}
	rawEndpoint := lookup(envOTLPEndpoint)
	if rawEndpoint == "" {
		return config{}, nil
	}

	if protocol := lookup(envOTLPProtocol); protocol != "" && protocol != protocolHTTPProtobuf {
		return config{}, fmt.Errorf("%s=%q: this build of avroc speaks only %q", envOTLPProtocol, protocol, protocolHTTPProtobuf)
	}

	endpoint, err := tracesEndpoint(rawEndpoint)
	if err != nil {
		return config{}, err
	}

	headers, err := parseKeyValues(envOTLPHeaders, lookup(envOTLPHeaders))
	if err != nil {
		return config{}, err
	}

	attributes, err := parseKeyValues(envResourceAttrs, lookup(envResourceAttrs))
	if err != nil {
		return config{}, err
	}

	sampler, err := parseSampler(lookup(envTracesSampler), lookup(envTracesSamplerA))
	if err != nil {
		return config{}, err
	}

	serviceName := lookup(envServiceName)
	if serviceName == "" {
		// OTEL_SERVICE_NAME wins over service.name in OTEL_RESOURCE_ATTRIBUTES,
		// which the specification requires and which is also the only order that
		// lets the more specific variable be the shorter one to type.
		serviceName = attributes[string(semconv.ServiceNameKey)]
	}
	if serviceName == "" {
		serviceName = DefaultServiceName
	}

	return config{
		enabled:     true,
		endpoint:    endpoint,
		headers:     headers,
		serviceName: serviceName,
		resource:    newResource(serviceName, version, attributes),
		sampler:     sampler,
	}, nil
}

// newResource builds the resource every span this run produces carries: the
// service name, this build's version, and whatever OTEL_RESOURCE_ATTRIBUTES
// added.
//
// The two the SDK is given last win, so service.name written into
// OTEL_RESOURCE_ATTRIBUTES cannot contradict the name resolved above, and
// service.version cannot claim a version this executable was not built at.
func newResource(serviceName, version string, attributes map[string]string) *resource.Resource {
	attrs := make([]attribute.KeyValue, 0, len(attributes)+2)
	for _, key := range sortedKeys(attributes) {
		attrs = append(attrs, attribute.String(key, attributes[key]))
	}
	attrs = append(attrs,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(version),
	)

	return resource.NewWithAttributes(semconv.SchemaURL, attrs...)
}

// sortedKeys orders a map's keys so that nothing downstream of it depends on
// Go's map iteration order. The resource is not generated output, but every
// ordered-by-accident collection in this repository is one somebody eventually
// has to diff.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// tracesEndpoint turns OTEL_EXPORTER_OTLP_ENDPOINT into the absolute URL the
// exporter posts to, which is that endpoint with the trace signal's path
// appended — the rule the OTLP specification gives for the base, non
// signal-specific variable.
func tracesEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s=%q is not a URL: %w", envOTLPEndpoint, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%s=%q: scheme must be http or https", envOTLPEndpoint, raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%s=%q: no host", envOTLPEndpoint, raw)
	}

	u.Path = strings.TrimSuffix(u.Path, "/") + tracesPath
	u.RawQuery = ""
	u.Fragment = ""

	return u.String(), nil
}

// parseKeyValues reads the comma-separated key=value form both
// OTEL_EXPORTER_OTLP_HEADERS and OTEL_RESOURCE_ATTRIBUTES are written in, with
// percent-encoded values.
//
// An empty entry is skipped, because a trailing comma is a typo nobody means
// anything by. An entry that is not key=value is an error naming the variable:
// it is the one that means the operator wrote something they expected to take
// effect.
func parseKeyValues(name, raw string) (map[string]string, error) {
	pairs := make(map[string]string)
	if raw == "" {
		return pairs, nil
	}

	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		key, value, found := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, fmt.Errorf("%s: %q is not key=value", name, entry)
		}

		// Percent-decoded with PathUnescape rather than QueryUnescape: the
		// encoding here is W3C Baggage's, in which "+" is a plus sign and not a
		// space.
		decoded, err := url.PathUnescape(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("%s: value of %q is not percent-encoded: %w", name, key, err)
		}
		pairs[key] = decoded
	}

	return pairs, nil
}

// parseSampler resolves OTEL_TRACES_SAMPLER and its argument.
//
// The default is parentbased_always_on, which is the specification's and the
// SDK's. A sampler this build does not know is an error rather than a silent
// fall back to that default: an operator who asked for always_off and got
// always_on has a bill, and one who asked for jaeger_remote and got a warning
// has a decision.
func parseSampler(name, arg string) (sdktrace.Sampler, error) {
	ratio := func() (float64, error) {
		if arg == "" {
			return 1, nil
		}
		value, err := strconv.ParseFloat(arg, 64)
		if err != nil {
			return 0, fmt.Errorf("%s=%q is not a number: %w", envTracesSamplerA, arg, err)
		}
		if value < 0 || value > 1 {
			return 0, fmt.Errorf("%s=%q is not a ratio between 0 and 1", envTracesSamplerA, arg)
		}
		return value, nil
	}

	switch name {
	case "", "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), nil
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample()), nil
	case "parentbased_traceidratio":
		value, err := ratio()
		if err != nil {
			return nil, err
		}
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(value)), nil
	case "always_on":
		return sdktrace.AlwaysSample(), nil
	case "always_off":
		return sdktrace.NeverSample(), nil
	case "traceidratio":
		value, err := ratio()
		if err != nil {
			return nil, err
		}
		return sdktrace.TraceIDRatioBased(value), nil
	default:
		return nil, fmt.Errorf("%s=%q is not a sampler this build of avroc knows", envTracesSampler, name)
	}
}
