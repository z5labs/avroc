// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Command tls-egress is the fixture `dagger call tls-egress` runs on both ends
// of the wire (.dagger/tls_egress.go, #198): an OTLP/HTTP receiver listening
// over TLS and in plaintext at once, and the client that posts to it from inside
// the published image.
//
// It exists because docs/container/SPEC.md's "No certificate authorities" is a
// claim about what a program *inside* the image can reach. The image is scratch
// and carries no CA bundle, so an OTLP endpoint named with `https://` fails
// certificate verification and one named with `http://` on the local network
// does not; neither half of that can be checked against a collector that speaks
// only plaintext, and the endpoint the determinism stage configures is not a
// collector at all.
//
// Three modes, one program. The client and the server are the same executable
// because the property under test is a Go TLS client's view of the image's root
// store, and building the two from one source is what makes "the client trusts
// nothing" a statement about the image rather than about two builds that might
// differ. Generating the certificate is separate from serving it because the
// check copies that certificate into a derived image to exercise the documented
// remedy, and the copy has to be the certificate the server presents:
//
//	go run ./internal/tools/tls-egress -generate -dir /certs -host collector
//	go run ./internal/tools/tls-egress -dir /certs -tls :8443 -plain :4318
//	go run ./internal/tools/tls-egress -probe https://collector:8443/v1/traces
//
// The probe reports on standard output and exits zero whatever happened —
// `ok: …` or `error: …`. Its verdict is the thing being read, so a failure to
// connect is data rather than a broken step, and the check names which of the
// two it required.
//
// The receiver is deliberately strict: 200 only for a POST of a non-empty
// protobuf body at the signal's path, and 4xx for anything else, so that "the
// probe got a 200" means a well-formed export arrived rather than that something
// opened a connection. What arrives is discarded — internal/telemetry's own
// tests are where an export's bytes are decoded, and a second decoder here would
// be a second thing to keep in step with the transform.
//
// It lives under internal/ for internal/tools/ir-descriptor-set's reason: it is
// not part of avroc's published surface, and putting it in cmd/ would make a
// check's fixture look like a shipped binary.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "tls-egress: %v\n", err)
		os.Exit(1)
	}
}

// The files the modes share, inside -dir.
const (
	certFile = "cert.pem"
	keyFile  = "key.pem"
)

// tracesPath is where OTLP/HTTP puts the trace signal, and it is the only path
// this receiver answers. An exporter appends it to the endpoint it was
// configured with, so a request arriving anywhere else is a client that built
// its URL wrongly.
const tracesPath = "/v1/traces"

// protobufContentType is what an OTLP/HTTP export carries. The other encoding
// OTLP defines is JSON, which nothing in this repository produces.
const protobufContentType = "application/x-protobuf"

// probeTimeout bounds one probe. It is short because every outcome the check
// distinguishes — a refused connection, a rejected certificate, a 200 — arrives
// in milliseconds over a container network, and the only thing a longer bound
// would buy is a slower report of a collector that is not there.
const probeTimeout = 15 * time.Second

// run is separated from main so that the exit path is the only thing main owns.
func run(args []string) error {
	flags := flag.NewFlagSet("tls-egress", flag.ContinueOnError)
	dir := flags.String("dir", "", "directory holding (or to hold) cert.pem and key.pem")
	generate := flags.Bool("generate", false, "write a self-signed certificate for -host and exit")
	host := flags.String("host", "localhost", "DNS name the generated certificate is valid for")
	tlsAddr := flags.String("tls", ":8443", "address to serve OTLP over TLS on")
	plainAddr := flags.String("plain", ":4318", "address to serve OTLP in plaintext on")
	probe := flags.String("probe", "", "post an export to this URL, report the outcome and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if rest := flags.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected arguments %v", rest)
	}

	if *probe != "" {
		report(postExport(*probe))
		return nil
	}
	if *dir == "" {
		return errors.New("-dir is required: name the directory holding the certificate")
	}
	if *generate {
		return writeCertificate(*dir, *host)
	}
	return serve(*dir, *tlsAddr, *plainAddr)
}

// postExport posts one export to url and reports what happened.
//
// The body is not a real trace. What is under test is the transport and the
// trust store the client verifies with, and a client that got as far as a status
// code got past both; encoding a span here would be a second producer of OTLP
// bytes in a repository that already has exactly one.
func postExport(url string) error {
	client := &http.Client{Timeout: probeTimeout}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte{0x0a, 0x00}))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", protobufContentType)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s responded %s", url, resp.Status)
	}
	return nil
}

// report writes the probe's verdict as the one line the check reads.
func report(err error) {
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	fmt.Println("ok")
}

// writeCertificate writes a self-signed certificate for host, and the key that
// goes with it, into dir.
//
// The certificate is its own issuer and is marked as a CA, so that a client
// handed cert.pem as the whole of its roots builds a chain of one and trusts it.
// That is the shape of the remedy docs/container/SPEC.md documents: a bundle the
// consumer supplies, holding whatever they have decided to trust.
func writeCertificate(dir, host string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating a key: %w", err)
	}

	// Backdated by an hour so that a clock skew of minutes between the engine
	// and a container cannot make a freshly written certificate not yet valid,
	// and short-lived because it exists for the length of one check.
	now := time.Now()
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("creating the certificate: %w", err)
	}
	encodedKey, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("encoding the key: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := writePEM(filepath.Join(dir, certFile), "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	return writePEM(filepath.Join(dir, keyFile), "EC PRIVATE KEY", encodedKey, 0o600)
}

func writePEM(name, block string, der []byte, mode os.FileMode) error {
	encoded := pem.EncodeToMemory(&pem.Block{Type: block, Bytes: der})
	if encoded == nil {
		return fmt.Errorf("encoding %s: not representable as PEM", name)
	}
	if err := os.WriteFile(name, encoded, mode); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	return nil
}

// serve runs both listeners and returns when either of them stops.
//
// Both, rather than one process per listener, because the two are one fixture:
// the check posts from the same image to the same host over `https://` and over
// `http://`, and the difference between the two probes has to be the scheme and
// nothing else — least of all which container happened to be up.
func serve(dir, tlsAddr, plainAddr string) error {
	cert := filepath.Join(dir, certFile)
	key := filepath.Join(dir, keyFile)

	failed := make(chan error, 2)
	go func() {
		log.Printf("serving OTLP over TLS on %s", tlsAddr)
		failed <- newServer(tlsAddr).ListenAndServeTLS(cert, key)
	}()
	go func() {
		log.Printf("serving OTLP in plaintext on %s", plainAddr)
		failed <- newServer(plainAddr).ListenAndServe()
	}()
	return <-failed
}

// maxExportBytes bounds one export. An OTLP request carrying a build's spans is
// kilobytes, so this is generous by three orders of magnitude and is not a limit
// anything here is expected to reach — it is there so that the failure mode of a
// client posting something enormous is a 4xx naming the size rather than the
// fixture growing without bound and the check failing for a reason that has
// nothing to do with certificates.
const maxExportBytes = 1 << 20

func newServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           http.HandlerFunc(receive),
		ReadHeaderTimeout: 10 * time.Second,
		// Pinned rather than left to the runtime's default, because this fixture
		// is the one place in the repository whose subject *is* TLS: what the
		// check measures is a client's trust store, and a run that had quietly
		// negotiated an older protocol would be measuring it under conditions
		// nobody chose. It applies to the plaintext listener too and costs
		// nothing there, since newServer is one definition of what a listener is.
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

// receive answers an export.
//
// It is strict on purpose. The check's success condition is a 200, and a
// receiver that answered 200 to any request at all would make that condition
// hold for a client that posted to the wrong path or sent nothing — which is the
// difference between "the export arrived" and "something opened a connection".
func receive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxExportBytes))
	if err != nil {
		reject(w, r, http.StatusBadRequest, fmt.Sprintf("unreadable body: %v", err))
		return
	}

	switch {
	case r.Method != http.MethodPost:
		reject(w, r, http.StatusMethodNotAllowed, "an export is a POST")
	case r.URL.Path != tracesPath:
		reject(w, r, http.StatusNotFound, "the trace signal is at "+tracesPath)
	case r.Header.Get("Content-Type") != protobufContentType:
		reject(w, r, http.StatusUnsupportedMediaType, "an export is "+protobufContentType)
	case len(body) == 0:
		reject(w, r, http.StatusBadRequest, "an export carries spans")
	default:
		log.Printf("accepted %d bytes at %s over %s", len(body), r.URL.Path, scheme(r))
		w.WriteHeader(http.StatusOK)
	}
}

func reject(w http.ResponseWriter, r *http.Request, status int, why string) {
	log.Printf("rejected %s %s over %s: %s", r.Method, r.URL.Path, scheme(r), why)
	http.Error(w, why, status)
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "TLS"
	}
	return "plaintext"
}
