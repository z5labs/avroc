// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"

	avro "github.com/z5labs/avro-go"
	"google.golang.org/protobuf/proto"
)

func TestSchemaFingerprint(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("Person"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.Person"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("string")},
							},
						},
					},
				},
			},
		},
	}

	fp, err := schemaFingerprint(schema)
	if err != nil {
		t.Fatalf("schemaFingerprint failed: %v", err)
	}

	// Compute expected fingerprint independently.
	pcf := `{"name":"com.example.Person","type":"record","fields":[{"name":"name","type":"string"}]}`
	var expected [8]byte
	binary.LittleEndian.PutUint64(expected[:], avro.Fingerprint64([]byte(pcf)))

	if fp != expected {
		t.Errorf("fingerprint mismatch\ngot:  %v\nwant: %v", fp, expected)
	}
}

func TestGenerateFingerprintMethod(t *testing.T) {
	fp := [8]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}

	cb := &codeBuilder{}
	generateFingerprintMethod(cb, "Person", fp)

	code := cb.String()

	if !strings.Contains(code, "func (x *Person) Fingerprint() [8]byte {") {
		t.Errorf("generated code missing Fingerprint method signature:\n%s", code)
	}

	if !strings.Contains(code, "return [8]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}") {
		t.Errorf("generated code missing fingerprint byte literal:\n%s", code)
	}
}

func TestGenerateFingerprintMethod_ValidGoSyntax(t *testing.T) {
	fp := [8]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11}

	cb := &codeBuilder{}
	cb.writeln("package test")
	cb.newline()
	cb.writeln("type Person struct{}")
	generateFingerprintMethod(cb, "Person", fp)

	validateGoSyntax(t, cb.String())
}
