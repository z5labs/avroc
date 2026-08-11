// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The whole of what this fixture has to get right is here, and it is here rather
// than only in `dagger call trace-propagation` because that check stands up two
// services and two builds and a contributor runs `go test` far more often. What
// the check proves is a property of avroc; what these prove is that the fixture
// measuring it is not the thing that is broken.

// TestTheFetcherWaitsForATraceThatIsNotThereYet is the polling contract, and the
// distinction it turns on is the one the whole fixture exists for: a trace that
// has not arrived yet and a trace that never will look identical for as long as
// the deadline has not passed.
//
// A 404 is Tempo saying "not yet" — the export, the collector's batch and the
// ingester are none of them synchronous with avroc exiting — so it must not end
// the wait. Nothing else here may be retried into oblivion either: the answer
// that eventually comes back has to be the trace, not a timeout that hid a
// server telling us something.
func TestTheFetcherWaitsForATraceThatIsNotThereYet(t *testing.T) {
	const body = `{"batches":[{"scopeSpans":[{"spans":[{"name":"avroc.generate"}]}]}]}`

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api/traces/abc123" {
			t.Errorf("the fetcher asked for %q, want /api/traces/abc123", got)
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	if err := fetchTrace(&out, server.URL, "abc123", 30*time.Second); err != nil {
		t.Fatalf("fetchTrace: %v", err)
	}
	if calls.Load() < 3 {
		t.Errorf("the fetcher gave up after %d calls, so it did not wait through the 404s", calls.Load())
	}
	if got := out.String(); got != body {
		t.Errorf("the fetcher printed %q, want the response body verbatim", got)
	}
}

// TestTheFetcherDoesNotAcceptATraceWithNoSpansInIt: Tempo answers 200 for a
// trace it has only partly received, and that answer is worth waiting past.
// Handing it on would report every assertion in the check failing at once, which
// reads as avroc being broken rather than as the trace not having arrived.
func TestTheFetcherDoesNotAcceptATraceWithNoSpansInIt(t *testing.T) {
	// A batch that is present and empty is the case a check on the outer array
	// alone would accept, and it is the one Tempo actually produces while a
	// trace is still arriving — so it is written out rather than left to the
	// obvious empty-array case.
	for name, body := range map[string]string{
		"no batches":              `{"batches":[]}`,
		"a batch with no scopes":  `{"batches":[{}]}`,
		"a scope with no spans":   `{"batches":[{"scopeSpans":[{}]}]}`,
		"an empty spans array":    `{"batches":[{"scopeSpans":[{"spans":[]}]}]}`,
		"two batches, both empty": `{"batches":[{"scopeSpans":[]},{"scopeSpans":[{"spans":[]}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(server.Close)

			err := fetchTrace(&bytes.Buffer{}, server.URL, "abc123", 100*time.Millisecond)
			if err == nil {
				t.Fatal("fetchTrace accepted a 200 holding no spans")
			}
			if !strings.Contains(err.Error(), "no spans") {
				t.Errorf("the failure does not say what was wrong with the answer: %v", err)
			}
		})
	}
}

// TestTheFetcherReportsWhatTheLastAnswerWas: the timeout is the message somebody
// reads off a red pipeline, so it has to carry the answer that kept coming back.
// "The trace did not turn up" is the same sentence for a Tempo that was 500ing
// and for one that had simply not seen it.
func TestTheFetcherReportsWhatTheLastAnswerWas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("tempo is unwell"))
	}))
	t.Cleanup(server.Close)

	err := fetchTrace(&bytes.Buffer{}, server.URL, "abc123", 100*time.Millisecond)
	if err == nil {
		t.Fatal("fetchTrace returned no error against a server that only ever 500s")
	}
	if !strings.Contains(err.Error(), "tempo is unwell") {
		t.Errorf("the failure does not carry the last answer: %v", err)
	}
}

// TestTheLauncherRecordsTheTraceContextItInherited is the launcher's other job,
// and the check cannot be written without it: the recorded value is where both
// the trace id to ask Tempo for and the span avroc's root must name as its
// parent come from.
func TestTheLauncherRecordsTheTraceContextItInherited(t *testing.T) {
	const inherited = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	t.Setenv(envTraceparent, inherited)

	// A directory that does not exist yet, because the image this runs in is
	// scratch and holds only the directories somebody put there.
	record := filepath.Join(t.TempDir(), "nested", "traceparent")

	// /nonexistent is what stops the exec: everything up to it must have
	// happened, which is exactly the ordering the launcher promises.
	if err := launchCommand("http://collector:4318", record, []string{"/nonexistent"}); err == nil {
		t.Fatal("launchCommand returned no error for a command that cannot be executed")
	}

	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	if strings.TrimSpace(string(got)) != inherited {
		t.Errorf("recorded %q, want the inherited %q", strings.TrimSpace(string(got)), inherited)
	}
	if got := os.Getenv(envEndpoint); got != "http://collector:4318" {
		t.Errorf("%s = %q, want the endpoint the launcher was given — Dagger's would have been left in place", envEndpoint, got)
	}
}

// TestTheLauncherRecordsAnAbsentTraceContextAsEmpty: an empty file is a finding
// and a missing one is an ambiguity. Recorded either way, the check can say
// "Dagger set no TRACEPARENT" instead of failing on a file it cannot find and
// leaving a reader to work out which of the two happened.
func TestTheLauncherRecordsAnAbsentTraceContextAsEmpty(t *testing.T) {
	t.Setenv(envTraceparent, "")

	record := filepath.Join(t.TempDir(), "traceparent")
	if err := launchCommand("http://collector:4318", record, []string{"/nonexistent"}); err == nil {
		t.Fatal("launchCommand returned no error for a command that cannot be executed")
	}

	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	if strings.TrimSpace(string(got)) != "" {
		t.Errorf("recorded %q, want it empty", string(got))
	}
}

// TestTheModesAreNotInterchangeable: the two halves run in different containers
// and neither is a default. A missing flag is a mistake in the pipeline, and it
// has to be an error rather than the other mode running.
func TestTheModesAreNotInterchangeable(t *testing.T) {
	for name, args := range map[string][]string{
		"neither mode":           nil,
		"both modes":             {"-launch", "-fetch"},
		"launch with no command": {"-launch", "-endpoint", "http://collector:4318"},
		"launch with no endpoint": {
			"-launch", "--", "/usr/local/bin/avroc", "generate",
		},
		"fetch with no tempo": {"-fetch", "-trace", "abc123"},
		"fetch with no trace": {"-fetch", "-tempo", "http://tempo:3200"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(args); err == nil {
				t.Error("accepted, want rejected")
			}
		})
	}
}
