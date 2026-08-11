// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/z5labs/avroc/internal/cli"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func environ(pairs map[string]string) cli.Environment {
	return cli.EnvironmentFunc(func(key string) (string, bool) {
		value, ok := pairs[key]
		return value, ok
	})
}

// TestTheEndpointIsTheBaseUrlPlusTheSignalsPath covers the one rule the OTLP
// specification gives for the non signal-specific endpoint variable.
func TestTheEndpointIsTheBaseUrlPlusTheSignalsPath(t *testing.T) {
	endpoints := map[string]string{
		"http://localhost:4318":             "http://localhost:4318/v1/traces",
		"http://localhost:4318/":            "http://localhost:4318/v1/traces",
		"https://collector.example:443":     "https://collector.example:443/v1/traces",
		"https://collector.example/otlp":    "https://collector.example/otlp/v1/traces",
		"https://collector.example/otlp/":   "https://collector.example/otlp/v1/traces",
		"http://localhost:4318?ignored=1":   "http://localhost:4318/v1/traces",
		"http://localhost:4318#alsoIgnored": "http://localhost:4318/v1/traces",
	}

	for raw, want := range endpoints {
		cfg, err := configFromEnv(environ(map[string]string{envOTLPEndpoint: raw}), "v0.0.0")
		if err != nil {
			t.Errorf("%s=%q: %v", envOTLPEndpoint, raw, err)
			continue
		}
		if !cfg.enabled {
			t.Errorf("%s=%q left tracing disabled", envOTLPEndpoint, raw)
			continue
		}
		if cfg.endpoint != want {
			t.Errorf("%s=%q resolved to %q, want %q", envOTLPEndpoint, raw, cfg.endpoint, want)
		}
	}
}

// TestAnEndpointThatIsNotOneIsRefused: the failure has to name the variable, so
// that a warning in a build log is actionable without reading this package.
func TestAnEndpointThatIsNotOneIsRefused(t *testing.T) {
	refused := []string{
		"not a url",
		"grpc://localhost:4317",
		"localhost:4318",
		"http://",
	}

	for _, raw := range refused {
		cfg, err := configFromEnv(environ(map[string]string{envOTLPEndpoint: raw}), "v0.0.0")
		if err == nil {
			t.Errorf("%s=%q was accepted", envOTLPEndpoint, raw)
			continue
		}
		if cfg.enabled {
			t.Errorf("%s=%q was refused and left tracing enabled", envOTLPEndpoint, raw)
		}
		if !strings.Contains(err.Error(), envOTLPEndpoint) {
			t.Errorf("the error for %q does not name %s: %v", raw, envOTLPEndpoint, err)
		}
	}
}

// TestOnlyOTLPOverHTTPProtobufIsSpoken records the one deliberate gap in this
// build's support for the specification.
func TestOnlyOTLPOverHTTPProtobufIsSpoken(t *testing.T) {
	t.Run("the default is http/protobuf", func(t *testing.T) {
		cfg, err := configFromEnv(environ(map[string]string{envOTLPEndpoint: "http://localhost:4318"}), "v0.0.0")
		if err != nil {
			t.Fatalf("configFromEnv returned %v, want nil", err)
		}
		if !cfg.enabled {
			t.Fatal("tracing is disabled with no protocol configured")
		}
	})

	t.Run("http/protobuf may be asked for explicitly", func(t *testing.T) {
		cfg, err := configFromEnv(environ(map[string]string{
			envOTLPEndpoint: "http://localhost:4318",
			envOTLPProtocol: protocolHTTPProtobuf,
		}), "v0.0.0")
		if err != nil {
			t.Fatalf("configFromEnv returned %v, want nil", err)
		}
		if !cfg.enabled {
			t.Fatal("tracing is disabled with http/protobuf configured")
		}
	})

	for _, protocol := range []string{"grpc", "http/json"} {
		t.Run(protocol+" is refused", func(t *testing.T) {
			cfg, err := configFromEnv(environ(map[string]string{
				envOTLPEndpoint: "http://localhost:4318",
				envOTLPProtocol: protocol,
			}), "v0.0.0")
			if err == nil {
				t.Fatalf("%s=%q was accepted", envOTLPProtocol, protocol)
			}
			if cfg.enabled {
				t.Error("tracing is enabled for a protocol this build cannot speak")
			}
			if !strings.Contains(err.Error(), protocolHTTPProtobuf) {
				t.Errorf("the error does not say what this build does speak: %v", err)
			}
		})
	}
}

// TestAnOffRunReadsNothingElse: the two off switches are tested before anything
// that can fail, so a run that is not exporting cannot fail on the syntax of a
// variable it was never going to use.
func TestAnOffRunReadsNothingElse(t *testing.T) {
	offCases := map[string]map[string]string{
		"disabled by the operator": {
			envSDKDisabled:   "true",
			envOTLPEndpoint:  "not a url",
			envTracesSampler: "nonsense",
		},
		"no endpoint": {
			envOTLPProtocol:  "grpc",
			envTracesSampler: "nonsense",
			envOTLPHeaders:   "not a header",
		},
	}

	for name, pairs := range offCases {
		t.Run(name, func(t *testing.T) {
			cfg, err := configFromEnv(environ(pairs), "v0.0.0")
			if err != nil {
				t.Errorf("configFromEnv returned %v, want nil", err)
			}
			if cfg.enabled {
				t.Error("tracing is enabled")
			}
		})
	}
}

// TestOTELSDKDisabledIsOnlyTrue: the specification gives the variable one value
// that means "off", and anything else — including "1" and "yes" — leaves the SDK
// alone.
func TestOTELSDKDisabledIsOnlyTrue(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "True", " true "} {
		cfg, err := configFromEnv(environ(map[string]string{
			envSDKDisabled:  value,
			envOTLPEndpoint: "http://localhost:4318",
		}), "v0.0.0")
		if err != nil {
			t.Fatalf("configFromEnv returned %v, want nil", err)
		}
		if cfg.enabled {
			t.Errorf("%s=%q left tracing enabled", envSDKDisabled, value)
		}
	}

	for _, value := range []string{"", "false", "1", "yes"} {
		cfg, err := configFromEnv(environ(map[string]string{
			envSDKDisabled:  value,
			envOTLPEndpoint: "http://localhost:4318",
		}), "v0.0.0")
		if err != nil {
			t.Fatalf("configFromEnv returned %v, want nil", err)
		}
		if !cfg.enabled {
			t.Errorf("%s=%q disabled tracing", envSDKDisabled, value)
		}
	}
}

func TestHeadersAndResourceAttributesAreParsed(t *testing.T) {
	t.Run("pairs are split, trimmed and percent-decoded", func(t *testing.T) {
		got, err := parseKeyValues(envOTLPHeaders, " api-key = se%20cret , x-tenant=avroc ,")
		if err != nil {
			t.Fatalf("parseKeyValues returned %v, want nil", err)
		}
		want := map[string]string{"api-key": "se cret", "x-tenant": "avroc"}
		if len(got) != len(want) {
			t.Fatalf("parsed %v, want %v", got, want)
		}
		for key, value := range want {
			if got[key] != value {
				t.Errorf("%s = %q, want %q", key, got[key], value)
			}
		}
	})

	t.Run("a plus sign is a plus sign", func(t *testing.T) {
		// W3C Baggage percent-encoding, not form encoding: a token carrying a
		// plus must not arrive as a space.
		got, err := parseKeyValues(envOTLPHeaders, "authorization=Bearer a+b")
		if err != nil {
			t.Fatalf("parseKeyValues returned %v, want nil", err)
		}
		if want := "Bearer a+b"; got["authorization"] != want {
			t.Errorf("authorization = %q, want %q", got["authorization"], want)
		}
	})

	t.Run("an entry that is not key=value is an error", func(t *testing.T) {
		for _, raw := range []string{"just-a-key", "=value"} {
			if _, err := parseKeyValues(envResourceAttrs, raw); err == nil {
				t.Errorf("%q was accepted", raw)
			}
		}
	})

	t.Run("an empty value is a value", func(t *testing.T) {
		got, err := parseKeyValues(envResourceAttrs, "empty=")
		if err != nil {
			t.Fatalf("parseKeyValues returned %v, want nil", err)
		}
		if value, ok := got["empty"]; !ok || value != "" {
			t.Errorf("empty = %q (present: %t), want an empty string that is present", value, ok)
		}
	})
}

// TestOTELServiceNameWinsOverTheResourceAttribute is the precedence the
// specification fixes, and the only order in which the more specific variable is
// also the shorter one to type.
func TestOTELServiceNameWinsOverTheResourceAttribute(t *testing.T) {
	cfg, err := configFromEnv(environ(map[string]string{
		envOTLPEndpoint:  "http://localhost:4318",
		envServiceName:   "from-service-name",
		envResourceAttrs: "service.name=from-resource-attributes",
	}), "v0.0.0")
	if err != nil {
		t.Fatalf("configFromEnv returned %v, want nil", err)
	}
	if got, want := cfg.serviceName, "from-service-name"; got != want {
		t.Errorf("service name = %q, want %q", got, want)
	}

	cfg, err = configFromEnv(environ(map[string]string{
		envOTLPEndpoint:  "http://localhost:4318",
		envResourceAttrs: "service.name=from-resource-attributes",
	}), "v0.0.0")
	if err != nil {
		t.Fatalf("configFromEnv returned %v, want nil", err)
	}
	if got, want := cfg.serviceName, "from-resource-attributes"; got != want {
		t.Errorf("service name = %q, want %q", got, want)
	}

	cfg, err = configFromEnv(environ(map[string]string{envOTLPEndpoint: "http://localhost:4318"}), "v0.0.0")
	if err != nil {
		t.Fatalf("configFromEnv returned %v, want nil", err)
	}
	if got, want := cfg.serviceName, DefaultServiceName; got != want {
		t.Errorf("service name = %q, want %q", got, want)
	}
}

func TestTheSamplerComesFromTheEnvironment(t *testing.T) {
	accepted := map[string]string{
		"":                         sdktrace.ParentBased(sdktrace.AlwaysSample()).Description(),
		"parentbased_always_on":    sdktrace.ParentBased(sdktrace.AlwaysSample()).Description(),
		"parentbased_always_off":   sdktrace.ParentBased(sdktrace.NeverSample()).Description(),
		"always_on":                sdktrace.AlwaysSample().Description(),
		"always_off":               sdktrace.NeverSample().Description(),
		"traceidratio":             sdktrace.TraceIDRatioBased(1).Description(),
		"parentbased_traceidratio": sdktrace.ParentBased(sdktrace.TraceIDRatioBased(1)).Description(),
	}

	for name, want := range accepted {
		sampler, err := parseSampler(name, "")
		if err != nil {
			t.Errorf("%s=%q: %v", envTracesSampler, name, err)
			continue
		}
		if got := sampler.Description(); got != want {
			t.Errorf("%s=%q resolved to %s, want %s", envTracesSampler, name, got, want)
		}
	}

	t.Run("the ratio comes from the argument", func(t *testing.T) {
		sampler, err := parseSampler("traceidratio", "0.25")
		if err != nil {
			t.Fatalf("parseSampler returned %v, want nil", err)
		}
		if got, want := sampler.Description(), sdktrace.TraceIDRatioBased(0.25).Description(); got != want {
			t.Errorf("sampler = %s, want %s", got, want)
		}
	})

	t.Run("a sampler this build does not know is refused", func(t *testing.T) {
		if _, err := parseSampler("jaeger_remote", ""); err == nil {
			t.Error("jaeger_remote was accepted")
		}
	})

	t.Run("an argument that is not a ratio is refused", func(t *testing.T) {
		for _, arg := range []string{"half", "-1", "2"} {
			if _, err := parseSampler("traceidratio", arg); err == nil {
				t.Errorf("%s=%q was accepted", envTracesSamplerA, arg)
			}
		}
	})
}

// TestANilEnvironmentIsAnEmptyOne: cli.Context is built by tests as well as by
// main, and "no environment" and "an environment holding none of these" describe
// the same run.
func TestANilEnvironmentIsAnEmptyOne(t *testing.T) {
	cfg, err := configFromEnv(nil, "v0.0.0")
	if err != nil {
		t.Fatalf("configFromEnv returned %v, want nil", err)
	}
	if cfg.enabled {
		t.Error("tracing is enabled with no environment at all")
	}
}

// TestTheTracingStackCarriesNoGRPCTransport is the executable form of the
// dependency budget that chose OTLP over HTTP in the first place.
//
// The obvious exporter — otlptracehttp — imports the OTLP collector service
// package for one message type, and that package carries the generated gRPC
// service and grpc-gateway stubs with it, so an HTTP-only exporter would link
// grpc-go into an executable that ships inside a scratch image. This package's
// own exporter exists to keep that out, and nothing but a check on go.mod
// notices the day somebody imports the convenient thing instead.
func TestTheTracingStackCarriesNoGRPCTransport(t *testing.T) {
	forbidden := []string{
		"google.golang.org/grpc",
		"github.com/grpc-ecosystem/grpc-gateway",
	}

	path := filepath.Join("..", "..", "go.mod")
	gomod, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	for _, line := range strings.Split(string(gomod), "\n") {
		for _, module := range forbidden {
			if strings.Contains(line, module) {
				t.Errorf("go.mod requires %s (%q): the OTLP exporter is HTTP so that it does not",
					module, strings.TrimSpace(line))
			}
		}
	}
}
