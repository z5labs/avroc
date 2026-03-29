// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"testing"

	"github.com/z5labs/avroc/internal/avrocpb"
	"google.golang.org/protobuf/proto"
)

func TestToPascalCase(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "simple", in: "hello", want: "Hello"},
		{name: "already pascal", in: "Hello", want: "Hello"},
		{name: "snake_case", in: "hello_world", want: "HelloWorld"},
		{name: "with hyphen", in: "hello-world", want: "HelloWorld"},
		{name: "qualified name", in: "com.example.MyType", want: "MyType"},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toPascalCase(tt.in)
			if got != tt.want {
				t.Errorf("toPascalCase(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestGoTypeForIdent(t *testing.T) {
	tests := []struct {
		name  string
		ident string
		want  string
	}{
		{name: "null", ident: "null", want: ""},
		{name: "boolean", ident: "boolean", want: "bool"},
		{name: "int", ident: "int", want: "int32"},
		{name: "long", ident: "long", want: "int64"},
		{name: "float", ident: "float", want: "float32"},
		{name: "double", ident: "double", want: "float64"},
		{name: "bytes", ident: "bytes", want: "[]byte"},
		{name: "string", ident: "string", want: "string"},
		{name: "custom type", ident: "MyRecord", want: "MyRecord"},
		{name: "qualified custom type", ident: "com.example.MyRecord", want: "MyRecord"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ident := &avrocpb.Ident{Value: proto.String(tt.ident)}
			got := goTypeForIdent(ident)
			if got != tt.want {
				t.Errorf("goTypeForIdent(%q) = %q, want %q", tt.ident, got, tt.want)
			}
		})
	}
}

func TestUnionTypeName(t *testing.T) {
	tests := []struct {
		recordName string
		fieldName  string
		want       string
	}{
		{"", "value", "ValueUnion"},
		{"", "my_field", "MyFieldUnion"},
		{"Person", "address", "PersonAddressUnion"},
		{"MyRecord", "status", "MyRecordStatusUnion"},
	}

	for _, tt := range tests {
		t.Run(tt.recordName+"_"+tt.fieldName, func(t *testing.T) {
			got := unionTypeName(tt.recordName, tt.fieldName)
			if got != tt.want {
				t.Errorf("unionTypeName(%q, %q) = %q, want %q", tt.recordName, tt.fieldName, got, tt.want)
			}
		})
	}
}
