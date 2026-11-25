// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"errors"
	"fmt"
	"strings"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
)

// Compare<Record>Avro, behind compare=true (#250), orders two encoded records
// by the specification's sort order without decoding either of them:
//
//	func CompareRecordAvro(a, b []byte) (c, na, nb int)
//
// It is what a sort or a merge join over encoded records calls, so it reads
// both buffers where they lie, allocates nothing, and stops at the first field
// that differs. na and nb follow avro-go's Consume convention, one per side and
// each side consumed independently: a count of the bytes read from that side,
// or a negative error code for avro.ConsumeError — so a caller holding only a
// record's prefix learns which side needs more bytes, and tells that apart from
// one that is corrupt.
//
// A record is compared as a flat sequence of leaf operations rather than as
// calls into nested records' own functions. A nested record's fields are
// inlined with its sign folded in — a descending record of descending fields
// compares them ascending — and a field the outer record ignores skips every
// leaf of it. Flattening is also what makes "a trailing run of ignored fields
// is never read" a property of the whole record rather than of each record
// separately: the run is cut from the end of the sequence, so a nested record's
// trailing ignored fields are read (to be skipped) exactly when something after
// them is compared, and never otherwise.
//
// The cost accepted for it is size: the generated code grows with the number
// of paths from the record to its leaves rather than with the number of
// types, so a record reused at several positions on several levels is
// inlined once per position. That is a schema's own size made visible rather
// than a cardinality the user does not control, and no cap is imposed.

// compareKind is what one leaf operation reads.
type compareKind int

const (
	compareBoolean compareKind = iota
	compareInt
	compareLong
	compareFloat
	compareDouble
	// compareBytes is bytes and string alike: both are a length and a payload,
	// and the specification orders both by unsigned bytes.
	compareBytes
	compareFixed
	compareEnum
	// compareOpaque is a field Compare cannot read — an array, a map, a union,
	// or a record containing itself. It is allowed only where it is never read:
	// in the trailing run of ignored fields, which compareOps cuts.
	compareOpaque
)

// compareOp is one leaf of a flattened record.
type compareOp struct {
	kind compareKind
	// path is the field's dotted path from the record being compared, which
	// is what the generated comment and a diagnostic name it by.
	path string
	// skip is set for a field whose order is "ignore", or that sits inside one.
	skip bool
	// desc is set when the field's result is negated: an odd number of
	// descending orders on the way down to it.
	desc bool
	// size is a fixed's size, and symbols an enum's symbol count.
	size    int
	symbols int

	// The declaring record, the field's own name and its type: for the
	// generated comment, and for the diagnostic an opaque operation becomes
	// when it is read.
	record string
	field  string
	typ    string
}

// comparePlan is the flattened, trimmed sequence of operations one record's
// Compare performs.
type comparePlan struct {
	ops []compareOp
	// trimmed is set when a trailing run of ignored fields was cut, so the
	// generated comment can say how much of each record a and b must hold.
	trimmed bool
}

// namedTypes is every record, enum and fixed the descriptor defines, by full
// name.
//
// A field's type is often a reference to a named type defined elsewhere — in a
// schema written earlier in the descriptor, or earlier in this one — and
// Compare has to know what that type is to read it. Looking the definition up
// by the full name the reference carries is walking the IR rather than
// re-deriving resolution (docs/ir/SPEC.md), the same reasoning fixedTypes
// documents.
type namedTypes map[string]*avrocpb.Type

func collectNamedTypes(schemas []*avrocpb.Schema) namedTypes {
	named := namedTypes{}
	for _, schema := range schemas {
		named.collect(schema.GetType())
		for _, t := range schema.GetTypes() {
			named.collect(t)
		}
	}
	return named
}

func (named namedTypes) collect(t *avrocpb.Type) {
	switch v := t.GetType().(type) {
	case *avrocpb.Type_Record:
		named[v.Record.GetFullName()] = t
		for _, f := range v.Record.GetFields() {
			named.collect(f.GetType())
		}
	case *avrocpb.Type_EnumType:
		named[v.EnumType.GetFullName()] = t
	case *avrocpb.Type_Fixed:
		named[v.Fixed.GetFullName()] = t
	case *avrocpb.Type_Array:
		named.collect(v.Array.GetItems())
	case *avrocpb.Type_MapType:
		named.collect(v.MapType.GetValues())
	case *avrocpb.Type_Union:
		for _, branch := range v.Union.GetTypes() {
			named.collect(branch)
		}
	}
}

// resolve returns the definition a named reference refers to, and t itself
// for anything else, including a reference to a name the descriptor does not
// define.
func (named namedTypes) resolve(t *avrocpb.Type) *avrocpb.Type {
	ref := t.GetReference()
	if ref == nil || ref.GetKind() != avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED {
		return t
	}
	if def, ok := named[ref.GetName()]; ok {
		return def
	}
	return t
}

// compareFuncName is the name of a record's Compare function.
func compareFuncName(r *avrocpb.Record) string {
	return "Compare" + toPascalCase(r.GetName()) + "Avro"
}

// comparePlanFor flattens r and cuts its trailing run of ignored operations.
// An opaque operation left after the cut is the reason r is outside Compare's
// scope, and is returned as the error.
func (named namedTypes) comparePlanFor(r *avrocpb.Record) (comparePlan, error) {
	var ops []compareOp
	named.appendOps(&ops, r, "", false, false, map[string]bool{})

	end := len(ops)
	for end > 0 && ops[end-1].skip {
		end--
	}
	plan := comparePlan{ops: ops[:end], trimmed: end < len(ops)}

	for _, op := range plan.ops {
		if op.kind != compareOpaque {
			continue
		}
		where := fmt.Sprintf("field %s of record %s", op.field, op.record)
		if op.path != op.field {
			where += fmt.Sprintf(" (reached as %s)", op.path)
		}
		return comparePlan{}, fmt.Errorf(
			"compare=true cannot generate %s for record %s: %s has type %s; "+
				"only primitive, enum, fixed and record fields can be compared, and a field of any other type "+
				"is allowed only in a trailing run of order \"ignore\" fields",
			compareFuncName(r), avroName(r.GetFullName(), r.GetName()), where, op.typ,
		)
	}
	return plan, nil
}

// appendOps appends the leaf operations of r's fields, with desc and ignored
// the sign and the ignore inherited from the fields r is nested in. visiting
// is the records on the way down, so that a record containing itself is
// reported rather than flattened forever.
func (named namedTypes) appendOps(ops *[]compareOp, r *avrocpb.Record, prefix string, desc, ignored bool, visiting map[string]bool) {
	recordName := avroName(r.GetFullName(), r.GetName())
	visiting[recordName] = true
	defer delete(visiting, recordName)

	for _, f := range r.GetFields() {
		op := compareOp{
			path:   prefix + f.GetName(),
			skip:   ignored || f.GetSortOrder() == avrocpb.SortOrder_SORT_ORDER_IGNORE,
			desc:   desc != (f.GetSortOrder() == avrocpb.SortOrder_SORT_ORDER_DESC),
			record: recordName,
			field:  f.GetName(),
		}

		t := named.resolve(f.GetType())
		switch v := t.GetType().(type) {
		case *avrocpb.Type_Reference:
			if !isPrimitiveRef(t) {
				op.kind, op.typ = compareOpaque, v.Reference.GetName()
				break
			}
			op.typ = v.Reference.GetName()
			switch op.typ {
			case "null":
				// Always equal, and no bytes to skip.
				continue
			case "boolean":
				op.kind = compareBoolean
			case "int":
				op.kind = compareInt
			case "long":
				op.kind = compareLong
			case "float":
				op.kind = compareFloat
			case "double":
				op.kind = compareDouble
			case "bytes", "string":
				op.kind = compareBytes
			default:
				op.kind, op.typ = compareOpaque, v.Reference.GetName()
			}
		case *avrocpb.Type_EnumType:
			op.kind, op.symbols = compareEnum, len(v.EnumType.GetValues())
			op.typ = "enum " + avroName(v.EnumType.GetFullName(), v.EnumType.GetName())
		case *avrocpb.Type_Fixed:
			if v.Fixed.GetSize() == 0 {
				// Always equal, and no bytes to skip.
				continue
			}
			op.kind, op.size = compareFixed, int(v.Fixed.GetSize())
			op.typ = fmt.Sprintf("fixed %s (%d bytes)", avroName(v.Fixed.GetFullName(), v.Fixed.GetName()), op.size)
		case *avrocpb.Type_Record:
			nested := avroName(v.Record.GetFullName(), v.Record.GetName())
			if visiting[nested] {
				op.kind, op.typ = compareOpaque, "record "+nested+", which contains itself"
				break
			}
			named.appendOps(ops, v.Record, op.path+".", op.desc, op.skip, visiting)
			continue
		default:
			op.kind, op.typ = compareOpaque, typeConstructor(t)
		}
		*ops = append(*ops, op)
	}
}

// checkCompareScope refuses, before a file is written, every record compare=true
// would have to generate a Compare for and cannot: checkSingleObjectRoots'
// reasoning, for the same kind of option.
//
// Every record is checked and every refusal reported, in descriptor order, so
// that a person fixing a schema sees each record in the way at once.
func checkCompareScope(schemas []*avrocpb.Schema) error {
	named := collectNamedTypes(schemas)
	var errs []error
	for _, schema := range schemas {
		if err := ir.Validate(schema); err != nil {
			return err
		}
		for _, ttg := range collectTypes(schema) {
			rec, ok := ttg.typ.GetType().(*avrocpb.Type_Record)
			if !ok {
				continue
			}
			if _, err := named.comparePlanFor(rec.Record); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// maxWordwiseFixed is the largest fixed compared as big-endian words rather
// than with bytes.Compare. A big-endian word compares as its bytes do, and is a
// load and a compare where bytes.Compare is a call; past a few words the call
// is the cheaper of the two, and the generated code stays short.
const maxWordwiseFixed = 32

// needsBytes, needsCmp and needsBinary report which standard library packages
// a plan's generated code calls into. A skipped operation calls none of them.
func (p comparePlan) needsBytes() bool {
	for _, op := range p.ops {
		if op.skip {
			continue
		}
		if op.kind == compareBytes || (op.kind == compareFixed && op.size > maxWordwiseFixed) {
			return true
		}
	}
	return false
}

func (p comparePlan) needsCmp() bool {
	for _, op := range p.ops {
		if op.skip {
			continue
		}
		switch op.kind {
		case compareBoolean, compareInt, compareLong, compareFloat, compareDouble, compareEnum:
			return true
		case compareFixed:
			if op.size <= maxWordwiseFixed {
				return true
			}
		}
	}
	return false
}

func (p comparePlan) needsBinary() bool {
	for _, op := range p.ops {
		if !op.skip && op.kind == compareFixed && op.size <= maxWordwiseFixed && op.size > 1 {
			return true
		}
	}
	return false
}

// generateCompareFunc writes Compare<Record>Avro for r.
func generateCompareFunc(cb *codeBuilder, r *avrocpb.Record, plan comparePlan) {
	name := compareFuncName(r)
	typeName := toPascalCase(r.GetName())

	cb.newline()
	cb.writef("// %s compares the %s records encoded at the start of a and b by\n", name, typeName)
	cb.writeln("// the Avro specification's sort order, without decoding them: field by field")
	cb.writeln("// in schema order, a descending field's result negated and an ignored field")
	cb.writeln("// skipped.")
	cb.writeln("//")
	cb.writeln("// c is -1, 0 or +1 as a's record sorts before, equal to or after b's. na and")
	cb.writeln("// nb follow avro-go's Consume convention, each side consumed independently: a")
	cb.writeln("// non-negative count is the bytes read from that side, and reading stops at")
	cb.writeln("// the first field that differs; a negative count is an error code for")
	cb.writeln("// avro.ConsumeError. c is 0 whenever either is negative.")
	if plan.trimmed && len(plan.ops) > 0 {
		cb.writeln("//")
		cb.writef("// Nothing after field %s is compared, so a and b need hold the\n", plan.ops[len(plan.ops)-1].path)
		cb.writeln("// encoding only through it.")
	}
	cb.writeln("//")
	cb.writef("// %s never allocates, and never panics on any input.\n", name)
	cb.writef("func %s(a, b []byte) (c, na, nb int) {\n", name)

	if len(plan.ops) == 0 {
		cb.writeln("\treturn 0, 0, 0")
		cb.writeln("}")
		return
	}

	cb.writeln("\tvar ka, kb int")
	for _, op := range plan.ops {
		cb.newline()
		cb.writef("\t// %s\n", op.describe())
		op.emit(cb)
	}
	cb.newline()
	cb.writeln("\treturn 0, na, nb")
	cb.writeln("}")
}

// describe is the comment a leaf's code is introduced by.
func (op compareOp) describe() string {
	typ := op.typ
	switch op.kind {
	case compareBytes, compareFixed:
		typ += ", by unsigned bytes"
	case compareEnum:
		typ += ", by ordinal"
	}
	order := "ascending"
	switch {
	case op.skip:
		order = "ignored, skipped"
	case op.desc:
		order = "descending"
	}
	return fmt.Sprintf("%s: %s; %s", op.path, typ, order)
}

// emit writes one leaf's statements. Each reads both sides into ka and kb —
// the field's byte count on that side, or a negative error code — returns the
// error case through writeCompareFail, then compares, and advances na and nb
// past the field.
//
// The one-byte case of every varint, length and index is decided here rather
// than in avro-go: a helper with a fast path and a call to its slow path is one
// Go will not inline, so the fast path has to be emitted to be fast.
// avro-go's CompareLong, CompareBytes, ConsumeIndex and the other Consume
// functions are the slow path, for the encodings longer than a byte, and for
// every error, which is how each error is reported with the code avro-go
// itself would give it.
func (op compareOp) emit(cb *codeBuilder) {
	switch op.kind {
	case compareBoolean:
		op.emitWidth(cb, 1, "\tc = cmp.Compare(min(a[na], 1), min(b[nb], 1))\n")
	case compareFloat:
		op.emitFloat(cb, "float32", "ConsumeFloat", 4)
	case compareDouble:
		op.emitFloat(cb, "float64", "ConsumeDouble", 8)
	case compareFixed:
		op.emitWidth(cb, op.size, fixedCompare(op.size))
	case compareInt, compareLong:
		op.emitVarint(cb)
	case compareBytes:
		op.emitBytes(cb)
	case compareEnum:
		op.emitEnum(cb)
	}
	op.emitDecide(cb)
	cb.writeln("\tna += ka")
	cb.writeln("\tnb += kb")
}

// fixedCompare is the statements comparing a fixed of size bytes at a[na:] and
// b[nb:], both already known to be there: big-endian words of 8, 4, 2 and 1
// bytes, each compared only while the ones before it were equal, or
// bytes.Compare past maxWordwiseFixed.
func fixedCompare(size int) string {
	if size > maxWordwiseFixed {
		return fmt.Sprintf("\tc = bytes.Compare(a[na:na+%d], b[nb:nb+%d])\n", size, size)
	}
	var out strings.Builder
	for off := 0; off < size; {
		var word string
		width := 8
		for width > size-off {
			width /= 2
		}
		at := func(buf, n string) string {
			if off == 0 {
				return buf + "[" + n + ":]"
			}
			return fmt.Sprintf("%s[%s+%d:]", buf, n, off)
		}
		switch width {
		case 8, 4, 2:
			fn := fmt.Sprintf("binary.BigEndian.Uint%d", width*8)
			word = fmt.Sprintf("cmp.Compare(%s(%s), %s(%s))", fn, at("a", "na"), fn, at("b", "nb"))
		case 1:
			if off == 0 {
				word = "cmp.Compare(a[na], b[nb])"
			} else {
				word = fmt.Sprintf("cmp.Compare(a[na+%d], b[nb+%d])", off, off)
			}
		}
		if off == 0 {
			fmt.Fprintf(&out, "\tc = %s\n", word)
		} else {
			fmt.Fprintf(&out, "\tif c == 0 {\n\t\tc = %s\n\t}\n", word)
		}
		off += width
	}
	return out.String()
}

// emitWidth reads a value of a constant width, and compares it with compare
// unless the field is skipped.
func (op compareOp) emitWidth(cb *codeBuilder, width int, compare string) {
	cb.writef("\tka, kb = %d, %d\n", width, width)
	cb.writef("\tif len(a)-na < %d {\n\t\tka = -1\n\t}\n", width)
	cb.writef("\tif len(b)-nb < %d {\n\t\tkb = -1\n\t}\n", width)
	writeCompareFail(cb, "\t")
	if !op.skip {
		cb.buf.WriteString(compare)
	}
}

func (op compareOp) emitFloat(cb *codeBuilder, goType, consume string, width int) {
	if op.skip {
		op.emitWidth(cb, width, "")
		return
	}
	cb.writeln("\t{")
	cb.writef("\t\tvar fa, fb %s\n", goType)
	cb.writef("\t\tfa, ka = avro.%s(a[na:])\n", consume)
	cb.writef("\t\tfb, kb = avro.%s(b[nb:])\n", consume)
	writeCompareFail(cb, "\t\t")
	cb.writeln("\t\tc = cmp.Compare(fa, fb)")
	cb.writeln("\t}")
}

// emitVarint reads an int or a long. A one-byte zigzag varint is a value in
// [-64, 63], decoded as an int8. An int's longer encoding goes through
// ConsumeInt rather than CompareLong, so a value that does not fit in 32 bits
// is reported as avro.ErrOverflow, as the decoder reports it.
func (op compareOp) emitVarint(cb *codeBuilder) {
	consume := "ConsumeLong"
	if op.kind == compareInt {
		consume = "ConsumeInt"
	}

	if op.skip {
		for _, side := range []struct{ buf, n, k string }{{"a", "na", "ka"}, {"b", "nb", "kb"}} {
			cb.writef("\tif len(%s) > %s && %s[%s] < 0x80 {\n", side.buf, side.n, side.buf, side.n)
			cb.writef("\t\t%s = 1\n", side.k)
			cb.writeln("\t} else {")
			cb.writef("\t\t_, %s = avro.%s(%s[%s:])\n", side.k, consume, side.buf, side.n)
			cb.writeln("\t}")
		}
		writeCompareFail(cb, "\t")
		return
	}

	cb.writeln("\tif len(a) > na && len(b) > nb && a[na] < 0x80 && b[nb] < 0x80 {")
	cb.writeln("\t\tka, kb = 1, 1")
	cb.writeln("\t\tc = cmp.Compare(int8(a[na]>>1)^-int8(a[na]&1), int8(b[nb]>>1)^-int8(b[nb]&1))")
	cb.writeln("\t} else {")
	if op.kind == compareLong {
		cb.writeln("\t\tc, ka, kb = avro.CompareLong(a[na:], b[nb:])")
		writeCompareFail(cb, "\t\t")
	} else {
		cb.writeln("\t\tvar va, vb int32")
		cb.writeln("\t\tva, ka = avro.ConsumeInt(a[na:])")
		cb.writeln("\t\tvb, kb = avro.ConsumeInt(b[nb:])")
		writeCompareFail(cb, "\t\t")
		cb.writeln("\t\tc = cmp.Compare(va, vb)")
	}
	cb.writeln("\t}")
}

// emitBytes reads a bytes or string value. A one-byte length is a byte below
// 0x80 whose low bit is clear — an odd one is a negative length, which the slow
// path reports.
func (op compareOp) emitBytes(cb *codeBuilder) {
	if op.skip {
		for _, side := range []struct{ buf, n, k string }{{"a", "na", "ka"}, {"b", "nb", "kb"}} {
			cb.writef("\tif len(%s) > %s && %s[%s]&0x81 == 0 {\n", side.buf, side.n, side.buf, side.n)
			cb.writef("\t\t%s = 1 + int(%s[%s]>>1)\n", side.k, side.buf, side.n)
			cb.writef("\t\tif len(%s)-%s < %s {\n\t\t\t%s = -1\n\t\t}\n", side.buf, side.n, side.k, side.k)
			cb.writeln("\t} else {")
			cb.writef("\t\t_, %s = avro.ConsumeBytes(%s[%s:])\n", side.k, side.buf, side.n)
			cb.writeln("\t}")
		}
		writeCompareFail(cb, "\t")
		return
	}

	cb.writeln("\tif len(a) > na && len(b) > nb && a[na]&0x81 == 0 && b[nb]&0x81 == 0 {")
	cb.writeln("\t\tka, kb = 1+int(a[na]>>1), 1+int(b[nb]>>1)")
	cb.writeln("\t\tif len(a)-na < ka {\n\t\t\tka = -1\n\t\t}")
	cb.writeln("\t\tif len(b)-nb < kb {\n\t\t\tkb = -1\n\t\t}")
	writeCompareFail(cb, "\t\t")
	cb.writeln("\t\tc = bytes.Compare(a[na+1:na+ka], b[nb+1:nb+kb])")
	cb.writeln("\t} else {")
	cb.writeln("\t\tc, ka, kb = avro.CompareBytes(a[na:], b[nb:])")
	writeCompareFail(cb, "\t\t")
	cb.writeln("\t}")
}

// emitEnum reads an enum's ordinal. A one-byte index is an even byte below
// 0x80; one naming no symbol, like every longer encoding, goes through
// ConsumeIndex, which reports it as avro.ErrInvalidIndex. With more than 63
// symbols every one-byte index names one, and the bound is left out.
func (op compareOp) emitEnum(cb *codeBuilder) {
	indent := "\t"
	if !op.skip {
		cb.writeln("\t{")
		cb.writeln("\t\tvar oa, ob int")
		indent = "\t\t"
	}
	for _, side := range []struct{ buf, n, k, o string }{{"a", "na", "ka", "oa"}, {"b", "nb", "kb", "ob"}} {
		cond := fmt.Sprintf("len(%s) > %s && %s[%s]&0x81 == 0", side.buf, side.n, side.buf, side.n)
		if op.symbols <= 63 {
			cond += fmt.Sprintf(" && int(%s[%s]>>1) < %d", side.buf, side.n, op.symbols)
		}
		cb.writef("%sif %s {\n", indent, cond)
		target := "_"
		if op.skip {
			cb.writef("%s\t%s = 1\n", indent, side.k)
		} else {
			target = side.o
			cb.writef("%s\t%s, %s = int(%s[%s]>>1), 1\n", indent, side.o, side.k, side.buf, side.n)
		}
		cb.writef("%s} else {\n", indent)
		cb.writef("%s\t%s, %s = avro.ConsumeIndex(%s[%s:], %d)\n", indent, target, side.k, side.buf, side.n, op.symbols)
		cb.writef("%s}\n", indent)
	}
	writeCompareFail(cb, indent)
	if !op.skip {
		cb.writeln("\t\tc = cmp.Compare(oa, ob)")
		cb.writeln("\t}")
	}
}

// emitDecide returns as soon as a compared field differs, with the counts
// through that field.
func (op compareOp) emitDecide(cb *codeBuilder) {
	if op.skip {
		return
	}
	result := "c"
	if op.desc {
		result = "-c"
	}
	cb.writeln("\tif c != 0 {")
	cb.writef("\t\treturn %s, na + ka, nb + kb\n", result)
	cb.writeln("\t}")
}

// writeCompareFail returns when either side failed to read the field: c is 0,
// the failing side reports its own code, and the other its count through the
// field — or its own code, if it failed too.
func writeCompareFail(cb *codeBuilder, indent string) {
	cb.writef("%sif ka < 0 || kb < 0 {\n", indent)
	cb.writef("%s\tif ka >= 0 {\n%s\t\tka += na\n%s\t}\n", indent, indent, indent)
	cb.writef("%s\tif kb >= 0 {\n%s\t\tkb += nb\n%s\t}\n", indent, indent, indent)
	cb.writef("%s\treturn 0, ka, kb\n", indent)
	cb.writef("%s}\n", indent)
}
