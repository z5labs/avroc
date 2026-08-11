// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package main

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole of what this fixture has to get right is here, and it is here rather
// than only in `dagger call tls-egress` because the check that exercises it end
// to end is the slowest thing in the pipeline and a contributor runs `go test`
// far more often. What the check proves is a property of the *image*; what these
// prove is that the fixture measuring it is not the thing that is broken.

func TestTheReceiverAcceptsOnlyAWellFormedExport(t *testing.T) {
	// The strictness is load bearing rather than tidy: the check's success
	// condition is a 200, so a receiver that answered 200 to anything at all
	// would let "the export arrived" hold for a client that posted to the wrong
	// path or sent nothing — which is a green check over an image nobody
	// measured.
	server := httptest.NewServer(http.HandlerFunc(receive))
	defer server.Close()

	testCases := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        string
		status      int
	}{
		{
			name:        "an export",
			method:      http.MethodPost,
			path:        tracesPath,
			contentType: protobufContentType,
			body:        "spans",
			status:      http.StatusOK,
		},
		{
			name:        "not a POST",
			method:      http.MethodGet,
			path:        tracesPath,
			contentType: protobufContentType,
			body:        "spans",
			status:      http.StatusMethodNotAllowed,
		},
		{
			name:        "another signal's path",
			method:      http.MethodPost,
			path:        "/v1/metrics",
			contentType: protobufContentType,
			body:        "points",
			status:      http.StatusNotFound,
		},
		{
			name:        "the encoding this repository does not produce",
			method:      http.MethodPost,
			path:        tracesPath,
			contentType: "application/json",
			body:        "{}",
			status:      http.StatusUnsupportedMediaType,
		},
		{
			name:        "nothing at all",
			method:      http.MethodPost,
			path:        tracesPath,
			contentType: protobufContentType,
			body:        "",
			status:      http.StatusBadRequest,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			req, err := http.NewRequest(testCase.method, server.URL+testCase.path, strings.NewReader(testCase.body))
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}
			req.Header.Set("Content-Type", testCase.contentType)

			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatalf("posting to the receiver: %v", err)
			}
			defer func() {
				_ = resp.Body.Close()
			}()

			if resp.StatusCode != testCase.status {
				t.Errorf("the receiver answered %d, want %d", resp.StatusCode, testCase.status)
			}
		})
	}
}

func TestTheProbeReportsWhatHappened(t *testing.T) {
	// Both verdicts, because the check reads them as opposites: a probe that
	// reported success on a rejected export would turn the TLS half of the check
	// into a statement that something connected.
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer rejecting.Close()

	accepting := httptest.NewServer(http.HandlerFunc(receive))
	defer accepting.Close()

	if err := postExport(accepting.URL + tracesPath); err != nil {
		t.Errorf("posting an export to a receiver that accepts it: %v", err)
	}
	if err := postExport(rejecting.URL + tracesPath); err == nil {
		t.Error("posting an export to a receiver that rejects it reported success")
	}
}

func TestTheGeneratedCertificateIsTrustedByAClientHoldingIt(t *testing.T) {
	// The remedy the check exercises is "a bundle the consumer supplied", and
	// this is the fixture's half of it: a certificate that is its own root, so
	// that a client holding cert.pem and nothing else completes a handshake. A
	// certificate that did not chain to itself would fail the same way an image
	// with no roots does, and the check would be measuring the fixture.
	const host = "collector"

	dir := t.TempDir()
	if err := writeCertificate(dir, host); err != nil {
		t.Fatalf("writing the certificate: %v", err)
	}

	pem, err := os.ReadFile(filepath.Join(dir, certFile))
	if err != nil {
		t.Fatalf("reading the certificate: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("the certificate is not a PEM a client can load as a root")
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(receive))
	keyPair, err := tls.LoadX509KeyPair(filepath.Join(dir, certFile), filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatalf("loading the key pair: %v", err)
	}
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{keyPair},
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	defer server.Close()

	// The server answers on the loopback rather than at the name the certificate
	// is issued for, so the name is what the client is told to expect. That is
	// the same check the image's client makes against `collector`, minus a
	// resolver this test has no business needing.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    roots,
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	}}}

	resp, err := client.Post(server.URL+tracesPath, protobufContentType, strings.NewReader("spans"))
	if err != nil {
		t.Fatalf("posting to a server presenting the generated certificate: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("the receiver answered %d over TLS, want %d", resp.StatusCode, http.StatusOK)
	}
}
