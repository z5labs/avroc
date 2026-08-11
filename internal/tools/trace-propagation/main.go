// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Command trace-propagation is the fixture `dagger call trace-propagation` runs
// on both ends of the check (.dagger/trace_propagation.go, #199): the launcher
// that starts avroc inside the published image, and the client that reads the
// finished trace back out of Tempo.
//
// Two modes, one program, for the reason tls-egress has three: they are the two
// halves of one claim — that the trace avroc opened under a Dagger exec arrives
// somewhere it can be read — and each half is useless without the other, so
// building them from one source keeps the traceparent the launcher recorded and
// the trace id the fetcher asks for the same value by construction rather than
// by two call sites agreeing.
//
//	go run ./internal/tools/trace-propagation -launch \
//	    -endpoint http://collector:4318 -record /traceparent -- /usr/local/bin/avroc generate
//	go run ./internal/tools/trace-propagation -fetch -tempo http://tempo:3200 -trace <hex>
//
// # Why there is a launcher at all
//
// **Dagger sets OTEL_EXPORTER_OTLP_ENDPOINT on every exec**, overriding whatever
// the container carried, so that a tool inside reports into Dagger's own trace.
// Measured on v0.21.8, and the same finding .dagger/tls_egress.go records: a
// container given an endpoint of its own reads Dagger's. So a check that needs
// avroc's spans in a collector it can query cannot get there by setting the
// variable on the container — the variable has to be set *inside* the exec,
// after Dagger has had its say, which is what this launcher is.
//
// It changes exactly one thing and re-execs. In particular it leaves TRACEPARENT
// alone: that value is the Dagger exec's own span, it is what makes avroc's root
// span a child of the pipeline, and it is the thing under test. Recording it is
// the launcher's other job, because the check cannot otherwise learn either the
// trace id to ask Tempo for or the span id avroc's root must name as its parent.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// envEndpoint is the OpenTelemetry exporter endpoint variable, and envTraceparent
// the trace-context carrier. They are literals rather than a shared constant
// because this tool is a fixture standing outside avroc, exactly as the probe in
// tls-egress is: what is under test is that avroc reads the names an operator
// sets, so a fixture importing avroc's spelling of them would check the two
// halves against each other instead of against the specification.
const (
	envEndpoint    = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envTraceparent = "TRACEPARENT"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "trace-propagation: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("trace-propagation", flag.ContinueOnError)
	launch := fs.Bool("launch", false, "set the OTLP endpoint, record TRACEPARENT, and exec the remaining arguments")
	fetch := fs.Bool("fetch", false, "read a trace back out of Tempo and print it")
	endpoint := fs.String("endpoint", "", "OTLP endpoint to export to (-launch)")
	record := fs.String("record", "", "file to write the inherited TRACEPARENT to (-launch)")
	tempo := fs.String("tempo", "", "Tempo query API base URL (-fetch)")
	traceID := fs.String("trace", "", "trace id to fetch, in hex (-fetch)")
	timeout := fs.Duration("timeout", 90*time.Second, "how long to wait for the trace to become queryable (-fetch)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch {
	case *launch && *fetch:
		return errors.New("-launch and -fetch are two different jobs; pass one")
	case *launch:
		return launchCommand(*endpoint, *record, fs.Args())
	case *fetch:
		return fetchTrace(os.Stdout, *tempo, *traceID, *timeout)
	}
	return errors.New("pass -launch or -fetch")
}

// launchCommand records the trace context this process was handed, points the
// exporter at endpoint, and replaces itself with argv.
//
// syscall.Exec rather than a child process: what follows is avroc, and the check
// reads its exit status, its output tree and its spans. A wrapper process in
// between would have to forward all three, and the one it would forward wrongly
// is the signal — which is precisely the classification internal/avroc makes of
// a generator and would then be making of itself through a proxy.
func launchCommand(endpoint, record string, argv []string) error {
	if endpoint == "" {
		return errors.New("-launch needs -endpoint")
	}
	if len(argv) == 0 {
		return errors.New("-launch needs a command to run after --")
	}

	// Recorded before anything else can fail, and recorded even when it is
	// empty: an empty file is the finding that Dagger stopped setting
	// TRACEPARENT, and it is a better one than a check that cannot say whether
	// the file was never written or the variable was never set.
	if record != "" {
		if err := writeRecord(record, os.Getenv(envTraceparent)); err != nil {
			return err
		}
	}

	if err := os.Setenv(envEndpoint, endpoint); err != nil {
		return fmt.Errorf("setting %s: %w", envEndpoint, err)
	}

	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", argv[0], err)
	}
	return nil // unreachable: a successful Exec does not return.
}

// writeRecord writes one line, creating the parent directory, because the image
// this runs in is scratch and holds only the directories somebody put there.
func writeRecord(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// fetchTrace polls Tempo for a trace and prints the response body verbatim.
//
// It polls because a trace is queryable only once it has travelled the whole
// path this check is about — the exporter's flush, the collector's batch, the
// ingester — and none of those is synchronous with avroc exiting. The wait is
// inside this process rather than in the pipeline for the same reason the rest
// of it is: a Dagger exec is one cached unit, so a retry loop belongs in the
// command being retried.
//
// A 404 is "not yet" and not a failure. Anything else is reported as it stands,
// because a Tempo that is answering with an error is a different finding from
// one that has not seen the trace.
func fetchTrace(w io.Writer, tempo, traceID string, timeout time.Duration) error {
	if tempo == "" || traceID == "" {
		return errors.New("-fetch needs -tempo and -trace")
	}

	url := tempo + "/api/traces/" + traceID
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 15 * time.Second}

	var last string
	for {
		body, status, err := get(client, url)
		switch {
		case err != nil:
			last = err.Error()
		case status == http.StatusNotFound:
			last = "404: Tempo has not seen the trace"
		case status != http.StatusOK:
			last = fmt.Sprintf("%d: %s", status, truncate(body))
		case !hasSpans(body):
			last = "200 with no spans in it"
		default:
			_, err := w.Write(body)
			return err
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("trace %s did not become queryable at %s within %s. The last answer was %s", traceID, url, timeout, last)
		}
		time.Sleep(time.Second)
	}
}

func get(client *http.Client, url string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	// Tempo answers this route in its protobuf encoding unless asked otherwise,
	// and the check reads JSON.
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// hasSpans reports whether a 200 carries a trace with a span actually in it.
//
// Tempo answers a trace it has only partially received, so "the request
// succeeded" is not the condition worth waiting for; a body holding no span at
// all is one the poll should keep going on rather than hand to a check that
// would report every assertion failing at once.
//
// It counts spans rather than batches, and the difference is not pedantry: a
// `batches` array can be non-empty while every batch in it carries no
// `scopeSpans`, or carries scopes holding no `spans`. Reading only the outer
// length would end the wait on exactly the half-delivered trace this exists to
// wait past, and the check downstream would then report every assertion failing
// at once — which reads as avroc being broken rather than as the trace not
// having arrived yet.
func hasSpans(body []byte) bool {
	var doc struct {
		Batches []struct {
			ScopeSpans []struct {
				Spans []json.RawMessage `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"batches"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return false
	}

	for _, batch := range doc.Batches {
		for _, scope := range batch.ScopeSpans {
			if len(scope.Spans) > 0 {
				return true
			}
		}
	}
	return false
}

func truncate(body []byte) string {
	const limit = 512
	if len(body) <= limit {
		return string(body)
	}
	return string(body[:limit]) + "…"
}
