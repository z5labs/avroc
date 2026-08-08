// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"encoding/binary"

	"github.com/z5labs/avroc/internal/avrocpb"

	avro "github.com/z5labs/avro-go"
)

// schemaFingerprint computes the 8-byte CRC-64-AVRO fingerprint for a schema
// by first converting it to Parsing Canonical Form and then hashing.
func schemaFingerprint(schema *avrocpb.Schema) [8]byte {
	pcf := canonicalForm(schema)
	fp64 := avro.Fingerprint64([]byte(pcf))
	var fp [8]byte
	binary.LittleEndian.PutUint64(fp[:], fp64)
	return fp
}

// generateFingerprintMethod generates the Fingerprint() [8]byte method for a
// record type. The fingerprint is a precomputed CRC-64-AVRO hash embedded as
// a byte literal for maximum performance.
func generateFingerprintMethod(cb *codeBuilder, name string, fp [8]byte) {
	cb.newline()
	cb.writef("func (x *%s) Fingerprint() [8]byte {\n", name)
	cb.writef("\treturn [8]byte{0x%02x, 0x%02x, 0x%02x, 0x%02x, 0x%02x, 0x%02x, 0x%02x, 0x%02x}\n",
		fp[0], fp[1], fp[2], fp[3], fp[4], fp[5], fp[6], fp[7])
	cb.writeln("}")
}
