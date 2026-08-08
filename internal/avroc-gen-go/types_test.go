// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"testing"

	"github.com/z5labs/avroc/avrocpb"
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

func TestGoTypeForReference(t *testing.T) {
	tests := []struct {
		name string
		ref  *avrocpb.Reference
		want string
	}{
		{name: "null", ref: primRef("null"), want: ""},
		{name: "boolean", ref: primRef("boolean"), want: "bool"},
		{name: "int", ref: primRef("int"), want: "int32"},
		{name: "long", ref: primRef("long"), want: "int64"},
		{name: "float", ref: primRef("float"), want: "float32"},
		{name: "double", ref: primRef("double"), want: "float64"},
		{name: "bytes", ref: primRef("bytes"), want: "[]byte"},
		{name: "string", ref: primRef("string"), want: "string"},
		{name: "named type", ref: namedRef("com.example.MyRecord"), want: "MyRecord"},
		// A named type whose simple name collides with a primitive's is still
		// a named type: the kind decides, not the spelling.
		{name: "named type spelled like a primitive", ref: namedRef("com.example.string"), want: "String"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := goTypeForReference(tt.ref)
			if got != tt.want {
				t.Errorf("goTypeForReference(%q) = %q, want %q", tt.ref.GetName(), got, tt.want)
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
