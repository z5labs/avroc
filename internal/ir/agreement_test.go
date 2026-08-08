// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	avro "github.com/z5labs/avro-go"
)

// TestGeneratorsAgreeOnCanonicalBytes is the regression test for the failure
// that motivated resolving the IR: avroc-gen-go embedding a fingerprint that
// does not match the schema avroc-gen-pcf publishes beside it.
//
// It reads the two generators' committed output for example/schema.avdl —
// regenerated and diff-checked by the repository's verify step, so it cannot
// drift from what the generators actually emit — and asserts that the
// fingerprint one embeds is the CRC-64-AVRO of the bytes the other published.
// The two agree because both derive from CanonicalJSON in this package; the
// test is what would notice if that stopped being true.
func TestGeneratorsAgreeOnCanonicalBytes(t *testing.T) {
	root := filepath.Join("..", "..", "example")

	published, err := os.ReadFile(filepath.Join(root, "pcf", "test_record.avsc"))
	if err != nil {
		t.Fatalf("failed to read avroc-gen-pcf output: %v", err)
	}

	generated, err := os.ReadFile(filepath.Join(root, "gen", "test_record.go"))
	if err != nil {
		t.Fatalf("failed to read avroc-gen-go output: %v", err)
	}

	embedded := parseFingerprintLiteral(t, string(generated))

	var want [8]byte
	binary.LittleEndian.PutUint64(want[:], avro.Fingerprint64(published))

	if embedded != want {
		t.Errorf("avroc-gen-go's fingerprint does not match avroc-gen-pcf's canonical form\n"+
			"embedded:   %v\n"+
			"CRC-64 of published canonical form: %v\n"+
			"published:  %s", embedded, want, published)
	}
}

// parseFingerprintLiteral extracts the [8]byte literal from the generated
// Fingerprint method.
func parseFingerprintLiteral(t *testing.T, src string) [8]byte {
	t.Helper()

	re := regexp.MustCompile(`return \[8\]byte\{([^}]*)\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("generated Go source carries no Fingerprint byte literal")
	}

	byteRe := regexp.MustCompile(`0x([0-9a-fA-F]{2})`)
	found := byteRe.FindAllStringSubmatch(m[1], -1)
	if len(found) != 8 {
		t.Fatalf("expected 8 bytes in the fingerprint literal, got %d", len(found))
	}

	var fp [8]byte
	for i, b := range found {
		v, err := strconv.ParseUint(b[1], 16, 8)
		if err != nil {
			t.Fatalf("failed to parse fingerprint byte %q: %v", b[1], err)
		}
		fp[i] = byte(v)
	}
	return fp
}
