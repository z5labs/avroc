// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"github.com/z5labs/avroc/avrocpb"
)

// directMethod is one of the two methods every type this generator writes
// MarshalAvroBinary for is given beside it (#249):
//
//	SizeAvro() int                   // the length of the encoding
//	AppendAvro(dst []byte) []byte    // dst extended by exactly that encoding
//
// They are one walk over the type, emitted twice, because they have to agree
// to the byte: SizeAvro is what a caller checks a record against a block's
// remaining room with before AppendAvro writes it there, and a size that
// disagreed with the bytes appended would overrun the block it said fitted.
// Every construct is handled in one place for both methods, so neither can
// gain a case the other lacks.
//
// Both mirror MarshalAvroBinary's encoding exactly — the same fields in the
// same order, a container as one block of all its items followed by the
// zero-count block, a fixed inline wherever it is addressable. The difference
// is the destination: a []byte the caller owns rather than an io.Writer, which
// is what makes them allocation-free. A fixed's x[:] passed to an io.Writer
// moves the array to the heap (#245); passed to append, it is only copied
// from. And because every value they reach is addressable — a record field
// through the pointer receiver, a slice element, a map's range variable, a
// branch's Value through its own receiver — there is no position in which a
// copy has to be taken first, so the map-value and union-branch exceptions
// MarshalAvroBinary documents do not arise here.
type directMethod int

const (
	sizeMethod directMethod = iota
	appendMethod
)

// generateDirectMethods writes SizeAvro and AppendAvro for a collected type,
// with the receiver its MarshalAvroBinary has.
func generateDirectMethods(cb *codeBuilder, ttg typeToGenerate, fixed fixedTypes) {
	for _, m := range []directMethod{sizeMethod, appendMethod} {
		switch v := ttg.typ.GetType().(type) {
		case *avrocpb.Type_Record:
			m.generateRecord(cb, v.Record, fixed)
		case *avrocpb.Type_EnumType:
			m.generateEnum(cb, v.EnumType)
		case *avrocpb.Type_Fixed:
			m.generateFixed(cb, v.Fixed)
		case *avrocpb.Type_Union:
			unionName := unionTypeName(ttg.recordName, ttg.fieldName)
			for i, branch := range v.Union.GetTypes() {
				m.generateUnionMember(cb, unionName, branch, i, fixed)
			}
		}
	}
}

// signature is the method's declaration up to its opening brace, for a
// receiver of the given form ("x *Record", "x Hash").
func (m directMethod) signature(receiver string) string {
	if m == sizeMethod {
		return "func (" + receiver + ") SizeAvro() int {"
	}
	return "func (" + receiver + ") AppendAvro(dst []byte) []byte {"
}

// result is the method's accumulator: n, the size so far, or dst, the bytes so
// far.
func (m directMethod) result() string {
	if m == sizeMethod {
		return "n"
	}
	return "dst"
}

func (m directMethod) generateRecord(cb *codeBuilder, r *avrocpb.Record, fixed fixedTypes) {
	cb.newline()
	cb.writeln(m.signature("x *" + toPascalCase(r.GetName())))

	fields := r.GetFields()
	if len(fields) == 0 {
		if m == sizeMethod {
			cb.writeln("\treturn 0")
		} else {
			cb.writeln("\treturn dst")
		}
		cb.writeln("}")
		return
	}

	if m == sizeMethod {
		cb.writeln("\tn := 0")
	}
	for _, f := range fields {
		m.generateValue(cb, f.GetType(), "x."+toFieldName(f.GetName()), "\t", 0, fixed)
	}
	cb.writef("\treturn %s\n", m.result())
	cb.writeln("}")
}

// generateEnum writes an enum's method. The ordinal is encoded as
// MarshalAvroBinary encodes it — WriteInt over int32(x) — so the two agree for
// every value of the type and not only for the ones a symbol names.
func (m directMethod) generateEnum(cb *codeBuilder, e *avrocpb.Enum) {
	cb.newline()
	cb.writeln(m.signature("x " + toPascalCase(e.GetName())))
	if m == sizeMethod {
		cb.writeln("\treturn avro.SizeLong(int64(int32(x)))")
	} else {
		cb.writeln("\treturn avro.AppendInt(dst, int32(x))")
	}
	cb.writeln("}")
}

// generateFixed writes a fixed type's method. Its size is the schema's, so it
// is written as the constant it is.
func (m directMethod) generateFixed(cb *codeBuilder, f *avrocpb.Fixed) {
	cb.newline()
	cb.writeln(m.signature("x " + toPascalCase(f.GetName())))
	if m == sizeMethod {
		cb.writef("\treturn %d\n", f.GetSize())
	} else {
		cb.writeln("\treturn avro.AppendFixed(dst, x[:])")
	}
	cb.writeln("}")
}

// generateUnionMember writes a union branch type's method: the branch index,
// as MarshalAvroBinary writes it, then the branch's value.
func (m directMethod) generateUnionMember(cb *codeBuilder, unionName string, t *avrocpb.Type, index int, fixed fixedTypes) {
	cb.newline()
	cb.writeln(m.signature("x " + unionMemberName(unionName, t)))

	if isNullType(t) {
		if m == sizeMethod {
			cb.writef("\treturn avro.SizeIndex(%d)\n", index)
		} else {
			cb.writef("\treturn avro.AppendIndex(dst, %d)\n", index)
		}
		cb.writeln("}")
		return
	}

	if m == sizeMethod {
		cb.writef("\tn := avro.SizeIndex(%d)\n", index)
	} else {
		cb.writef("\tdst = avro.AppendIndex(dst, %d)\n", index)
	}
	m.generateValue(cb, t, "x.Value", "\t", 0, fixed)
	cb.writef("\treturn %s\n", m.result())
	cb.writeln("}")
}

// generateValue writes the statements that size or append the value of type t
// held at expr, which is always addressable.
//
// depth is how many containers deep expr already is, and names each nested
// container's loop variables so that one never shadows the loop it is inside
// (#237).
func (m directMethod) generateValue(cb *codeBuilder, t *avrocpb.Type, expr string, indent string, depth int, fixed fixedTypes) {
	if t == nil {
		return
	}

	if fixed.isFixed(t) {
		if m == sizeMethod {
			cb.writef("%sn += len(%s)\n", indent, expr)
		} else {
			cb.writef("%sdst = avro.AppendFixed(dst, %s[:])\n", indent, expr)
		}
		return
	}

	switch v := t.GetType().(type) {
	case *avrocpb.Type_Reference:
		if isPrimitiveRef(t) {
			m.generatePrimitive(cb, v.Reference.GetName(), expr, indent)
			return
		}
		m.generateCall(cb, expr, indent)
	case *avrocpb.Type_Record, *avrocpb.Type_EnumType, *avrocpb.Type_Union:
		// A union is the field's union interface, whose branch writes its own
		// index.
		m.generateCall(cb, expr, indent)
	case *avrocpb.Type_Array:
		m.generateArray(cb, v.Array, expr, indent, depth, fixed)
	case *avrocpb.Type_MapType:
		m.generateMap(cb, v.MapType, expr, indent, depth, fixed)
	}
}

// generateCall delegates to the value's own method: a named type's, or the
// union interface's.
func (m directMethod) generateCall(cb *codeBuilder, expr string, indent string) {
	if m == sizeMethod {
		cb.writef("%sn += %s.SizeAvro()\n", indent, expr)
	} else {
		cb.writef("%sdst = %s.AppendAvro(dst)\n", indent, expr)
	}
}

// constantSize is the encoded size of a primitive whose size does not depend
// on its value — boolean, float and double — and false for every other type.
//
// SizeAvro writes such a value as the constant rather than as an expression
// over it, and so a container of them is sized by multiplying rather than by a
// loop: a loop whose body never mentions its variable is one Go refuses to
// compile, and multiplying is the O(1) answer anyway.
func constantSize(t *avrocpb.Type) (int, bool) {
	if !isPrimitiveRef(t) {
		return 0, false
	}
	switch t.GetReference().GetName() {
	case "boolean":
		return 1, true
	case "float":
		return 4, true
	case "double":
		return 8, true
	}
	return 0, false
}

// generatePrimitive writes a primitive value with avro-go's Size and Append
// functions, which are the bytes BinaryWriter's Write methods write. A
// primitive whose size does not depend on its value is sized as that constant.
// null is no bytes at all.
func (m directMethod) generatePrimitive(cb *codeBuilder, name string, expr string, indent string) {
	if m == sizeMethod {
		var size string
		switch name {
		case "boolean":
			cb.writef("%sn++\n", indent)
			return
		case "int":
			size = "avro.SizeLong(int64(" + expr + "))"
		case "long":
			size = "avro.SizeLong(" + expr + ")"
		case "float":
			size = "4"
		case "double":
			size = "8"
		case "bytes", "string":
			size = "avro.SizeBytes(len(" + expr + "))"
		default:
			return
		}
		cb.writef("%sn += %s\n", indent, size)
		return
	}

	var fn string
	switch name {
	case "boolean":
		fn = "AppendBool"
	case "int":
		fn = "AppendInt"
	case "long":
		fn = "AppendLong"
	case "float":
		fn = "AppendFloat"
	case "double":
		fn = "AppendDouble"
	case "bytes":
		fn = "AppendBytes"
	case "string":
		fn = "AppendString"
	default:
		return
	}
	cb.writef("%sdst = avro.%s(dst, %s)\n", indent, fn, expr)
}

// generateBlockCount opens a container's single block, which is written only
// when the container is not empty: its item count, as MarshalAvroBinary writes
// it. The caller closes the if it opens.
func (m directMethod) generateBlockCount(cb *codeBuilder, expr string, indent string) {
	cb.writef("%sif len(%s) > 0 {\n", indent, expr)
	if m == sizeMethod {
		cb.writef("%s\tn += avro.SizeLong(int64(len(%s)))\n", indent, expr)
	} else {
		cb.writef("%s\tdst = avro.AppendLong(dst, int64(len(%s)))\n", indent, expr)
	}
}

// generateBlockEnd writes the zero-count block that ends every container: one
// byte, zero.
func (m directMethod) generateBlockEnd(cb *codeBuilder, indent string) {
	if m == sizeMethod {
		cb.writef("%sn++\n", indent)
	} else {
		cb.writef("%sdst = append(dst, 0)\n", indent)
	}
}

func (m directMethod) generateArray(cb *codeBuilder, arr *avrocpb.Array, expr string, indent string, depth int, fixed fixedTypes) {
	i := depthName("i", depth)

	m.generateBlockCount(cb, expr, indent)
	if size, ok := constantSize(arr.GetItems()); ok && m == sizeMethod {
		cb.writef("%s\tn += len(%s) * %d\n", indent, expr, size)
	} else {
		cb.writef("%s\tfor %s := range %s {\n", indent, i, expr)
		m.generateValue(cb, arr.GetItems(), expr+"["+i+"]", indent+"\t\t", depth+1, fixed)
		cb.writef("%s\t}\n", indent)
	}
	cb.writef("%s}\n", indent)
	m.generateBlockEnd(cb, indent)
}

// generateMap ranges over the map's keys and values together, so the value is
// the range variable: addressable, which is what lets a record value's pointer
// method and a fixed value's slice reach it with no copy taken first.
func (m directMethod) generateMap(cb *codeBuilder, mt *avrocpb.Map, expr string, indent string, depth int, fixed fixedTypes) {
	k := depthName("k", depth)
	v := depthName("v", depth)

	m.generateBlockCount(cb, expr, indent)
	if size, ok := constantSize(mt.GetValues()); ok && m == sizeMethod {
		// Every key still has to be measured, but not the values.
		cb.writef("%s\tn += len(%s) * %d\n", indent, expr, size)
		cb.writef("%s\tfor %s := range %s {\n", indent, k, expr)
		cb.writef("%s\t\tn += avro.SizeBytes(len(%s))\n", indent, k)
		cb.writef("%s\t}\n", indent)
		cb.writef("%s}\n", indent)
		m.generateBlockEnd(cb, indent)
		return
	}
	cb.writef("%s\tfor %s, %s := range %s {\n", indent, k, v, expr)
	if m == sizeMethod {
		cb.writef("%s\t\tn += avro.SizeBytes(len(%s))\n", indent, k)
	} else {
		cb.writef("%s\t\tdst = avro.AppendString(dst, %s)\n", indent, k)
	}
	m.generateValue(cb, mt.GetValues(), v, indent+"\t\t", depth+1, fixed)
	cb.writef("%s\t}\n", indent)
	cb.writef("%s}\n", indent)
	m.generateBlockEnd(cb, indent)
}
