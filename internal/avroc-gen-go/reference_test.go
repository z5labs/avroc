// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"github.com/z5labs/avroc/avrocpb"

	"google.golang.org/protobuf/proto"
)

// primRef builds a resolved reference to an Avro primitive.
func primRef(name string) *avrocpb.Reference {
	return &avrocpb.Reference{
		Name: proto.String(name),
		Kind: avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE.Enum(),
	}
}

// namedRef builds a resolved reference to a named type, whose name is always
// fully qualified.
func namedRef(fullName string) *avrocpb.Reference {
	return &avrocpb.Reference{
		Name: proto.String(fullName),
		Kind: avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED.Enum(),
	}
}

func primType(name string) *avrocpb.Type {
	return &avrocpb.Type{Type: &avrocpb.Type_Reference{Reference: primRef(name)}}
}
