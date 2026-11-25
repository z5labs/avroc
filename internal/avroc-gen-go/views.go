// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
)

// A <Record>View, behind views=true (#251), indexes one encoded record from a
// prefix of its bytes without decoding or copying it:
//
//	var v RecordView
//	err := v.Parse(b) // b need hold only v.PrefixLen() bytes
//	key := v.Key()    // a slice of b
//
// It is what a reader of in-memory blocks and run files holds instead of a
// decoded record: it can read a record's key, learn a large value's size
// before the value has arrived, and tell "need more bytes" apart from
// "corrupt", all without allocating.
//
// The prefix is the encoding up to the record's last field when that field is
// bytes or string — through its length, but not its payload — and the same
// rule applied to a nested record's last field, recursively. For any other
// last field it is the whole record. Everything before the prefix's end is
// validated by Parse exactly as UnmarshalAvroBinary validates it, so the two
// agree on which input is corrupt; the payload after it has no structure to
// validate.
//
// Each record's Parse is its own function, and a nested record is parsed by
// calling its view's Parse — unlike Compare<Record>Avro, which flattens. A
// nested record field's accessor returns that record's view, and a view that
// was parsed as part of an outer one is that view, so the nesting is what the
// state has to look like anyway, and nothing here needs a sequence cut short.

// viewKind is what one field of a view reads.
type viewKind int

const (
	viewNull viewKind = iota
	viewBoolean
	viewInt
	viewLong
	viewFloat
	viewDouble
	viewBytes
	viewString
	viewFixed
	viewEnum
	viewRecord
)

// viewField is one field of a record, as its view indexes it.
type viewField struct {
	// index is the field's position in its record, which is what the view's
	// unexported state for it is named by: at<index>, len<index>, val<index>
	// and rec<index> cannot collide with each other whatever the fields are
	// called, and being lower case they cannot collide with an accessor.
	index int
	// name is the field's Avro name, and accessor the Go name of the method
	// returning it.
	name     string
	accessor string
	kind     viewKind
	// goType is the accessor's result for an enum or a fixed, and the nested
	// view's type for a record.
	goType string
	// recordType is a nested record's Go type, which Decode decodes into.
	recordType string
	size       int
	symbols    int
	// width is the field's encoded size when it is the same for every value,
	// and -1 otherwise; min is the fewest bytes any value of it can occupy.
	width int
	min   int
	// offsetConst names the field's offset constant, for a field before the
	// record's first variable-size field, and offset is its value.
	offsetConst string
	offset      int
	// nested is a record field's own plan.
	nested *viewPlan
	// typ describes the field's type for the generated comment.
	typ string
}

// viewPlan is everything one record's view is generated from.
type viewPlan struct {
	typeName string
	viewName string
	compare  string
	fields   []viewField
	// tail is the index of the field the prefix ends inside — a last field of
	// type bytes or string, or a last nested record that has a tail of its own
	// — or -1 when the prefix is the whole record.
	tail int
	// tailCompared is set when Compare<Record>Avro reads the tail's payload, so
	// that View.Compare needs both records complete.
	tailCompared bool
	// suffix[i] is the fewest bytes the fields after field i can occupy, all
	// of which lie inside the prefix. Need adds it to the end of the field that
	// ran out, which is what keeps Need a lower bound while making it as large
	// as the bytes already seen allow.
	suffix []int
}

// viewTypeName is the name of a record's view.
func viewTypeName(r *avrocpb.Record) string {
	return toPascalCase(r.GetName()) + "View"
}

// viewOffsetName is the name of a field's offset constant.
func viewOffsetName(r *avrocpb.Record, f *avrocpb.Field) string {
	return toPascalCase(r.GetName()) + toFieldName(f.GetName()) + "Offset"
}

// width is the record's encoded size when every value has the same one, and
// -1 otherwise.
func (p *viewPlan) width() int {
	n := 0
	for _, f := range p.fields {
		if f.width < 0 {
			return -1
		}
		n += f.width
	}
	return n
}

// min is the fewest bytes any value of the record can occupy.
func (p *viewPlan) min() int {
	n := 0
	for _, f := range p.fields {
		n += f.min
	}
	return n
}

// viewPlanFor plans r's view. A field the view cannot index is the reason r is
// outside the scope, and is returned as the error.
func (named namedTypes) viewPlanFor(r *avrocpb.Record) (viewPlan, error) {
	return named.viewPlan(r, r, "", map[string]bool{})
}

func (named namedTypes) viewPlan(root, r *avrocpb.Record, prefix string, visiting map[string]bool) (viewPlan, error) {
	recordName := avroName(r.GetFullName(), r.GetName())
	visiting[recordName] = true
	defer delete(visiting, recordName)

	p := viewPlan{
		typeName: toPascalCase(r.GetName()),
		viewName: viewTypeName(r),
		compare:  compareFuncName(r),
		tail:     -1,
	}

	for i, f := range r.GetFields() {
		refuse := func(typ string) error {
			where := fmt.Sprintf("field %s of record %s", f.GetName(), recordName)
			if prefix != "" {
				where += fmt.Sprintf(" (reached as %s%s)", prefix, f.GetName())
			}
			return fmt.Errorf(
				"views=true cannot generate %s for record %s: %s has type %s; "+
					"only primitive, enum, fixed and record fields can be viewed",
				viewTypeName(root), avroName(root.GetFullName(), root.GetName()), where, typ,
			)
		}

		vf := viewField{index: i, name: f.GetName(), accessor: toFieldName(f.GetName())}
		t := named.resolve(f.GetType())
		switch v := t.GetType().(type) {
		case *avrocpb.Type_Reference:
			if !isPrimitiveRef(t) {
				return viewPlan{}, refuse(v.Reference.GetName())
			}
			vf.typ = v.Reference.GetName()
			switch vf.typ {
			case "null":
				vf.kind = viewNull
			case "boolean":
				vf.kind, vf.width, vf.min = viewBoolean, 1, 1
			case "int":
				vf.kind, vf.width, vf.min = viewInt, -1, 1
			case "long":
				vf.kind, vf.width, vf.min = viewLong, -1, 1
			case "float":
				vf.kind, vf.width, vf.min = viewFloat, 4, 4
			case "double":
				vf.kind, vf.width, vf.min = viewDouble, 8, 8
			case "bytes":
				vf.kind, vf.width, vf.min = viewBytes, -1, 1
			case "string":
				vf.kind, vf.width, vf.min = viewString, -1, 1
			default:
				return viewPlan{}, refuse(vf.typ)
			}
		case *avrocpb.Type_EnumType:
			vf.kind, vf.width, vf.min = viewEnum, -1, 1
			vf.goType = toPascalCase(v.EnumType.GetName())
			vf.symbols = len(v.EnumType.GetValues())
			vf.typ = "enum " + avroName(v.EnumType.GetFullName(), v.EnumType.GetName())
		case *avrocpb.Type_Fixed:
			vf.kind = viewFixed
			vf.size = int(v.Fixed.GetSize())
			vf.width, vf.min = vf.size, vf.size
			vf.goType = toPascalCase(v.Fixed.GetName())
			vf.typ = fmt.Sprintf("fixed %s (%d bytes)", avroName(v.Fixed.GetFullName(), v.Fixed.GetName()), vf.size)
		case *avrocpb.Type_Record:
			nestedName := avroName(v.Record.GetFullName(), v.Record.GetName())
			if visiting[nestedName] {
				return viewPlan{}, refuse("record " + nestedName + ", which contains itself")
			}
			nested, err := named.viewPlan(root, v.Record, prefix+f.GetName()+".", visiting)
			if err != nil {
				return viewPlan{}, err
			}
			vf.kind = viewRecord
			vf.nested = &nested
			vf.goType = nested.viewName
			vf.recordType = nested.typeName
			vf.width, vf.min = nested.width(), nested.min()
			vf.typ = "record " + nestedName
		default:
			return viewPlan{}, refuse(typeConstructor(t))
		}
		p.fields = append(p.fields, vf)
	}

	// The fields before the first variable-size one are at the same offset in
	// every encoding, and each is given a constant naming it.
	offset := 0
	for i := range p.fields {
		if p.fields[i].width < 0 {
			break
		}
		p.fields[i].offset = offset
		p.fields[i].offsetConst = viewOffsetName(r, r.GetFields()[i])
		offset += p.fields[i].width
	}

	if n := len(p.fields); n > 0 {
		last := p.fields[n-1]
		lastField := r.GetFields()[n-1]
		ignoredLast := lastField.GetSortOrder() == avrocpb.SortOrder_SORT_ORDER_IGNORE
		switch {
		case last.kind == viewBytes || last.kind == viewString:
			p.tail = n - 1
			p.tailCompared = !ignoredLast
		case last.kind == viewRecord && last.nested.tail >= 0:
			p.tail = n - 1
			p.tailCompared = !ignoredLast && last.nested.tailCompared
		}
	}

	p.suffix = make([]int, len(p.fields))
	for i, rest := len(p.fields)-1, 0; i >= 0; i-- {
		p.suffix[i] = rest
		rest += p.fields[i].min
	}
	return p, nil
}

// checkViewScope refuses, before a file is written, every record views=true
// would have to generate a view for and cannot: checkCompareScope's reasoning,
// and like it every refusal is reported, in descriptor order.
func checkViewScope(schemas []*avrocpb.Schema) error {
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
			if _, err := named.viewPlanFor(rec.Record); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// needsFail reports whether any field's Parse can fail, which is when the
// view's fail method is emitted: a record of null and zero-size fixed fields
// alone parses from any input, and an unused method is one the linter refuses
// in a committed package.
func (p *viewPlan) needsFail() bool {
	for _, f := range p.fields {
		if f.min > 0 || f.kind == viewRecord {
			return true
		}
	}
	return false
}

// viewMember is one field or method of a view, for the identifier registry.
type viewMember struct {
	name   string
	source string
}

// members is every field and method a view declares, as generateView writes
// them. The registry and the emitter both read it, so the two cannot disagree.
func (p *viewPlan) members(compare bool) []viewMember {
	members := []viewMember{
		{"b", "unexported field b"},
		{"ok", "unexported field ok"},
		{"need", "unexported field need"},
		{"prefix", "unexported field prefix"},
		{"size", "unexported field size"},
	}
	for _, f := range p.fields {
		for _, s := range f.state(p) {
			members = append(members, viewMember{s.name, fmt.Sprintf("unexported field %s for field %s", s.name, f.name)})
		}
	}
	for _, m := range []string{"Parse", "Need", "PrefixLen", "Len", "Complete", "Bytes", "Decode"} {
		members = append(members, viewMember{m, "method " + m})
	}
	if p.needsFail() {
		members = append(members, viewMember{"fail", "unexported method fail"})
	}
	if compare {
		members = append(members, viewMember{"Compare", "method Compare"})
	}
	for _, f := range p.fields {
		for _, a := range f.accessors(p) {
			members = append(members, viewMember{a, fmt.Sprintf("accessor %s for field %s", a, f.name)})
		}
	}
	return members
}

// viewState is one unexported field of a view, holding part of the index of
// one record field.
type viewState struct {
	name   string
	goType string
}

// state is the unexported fields f is indexed in: an offset for a value read
// where it lies, unless the offset is a constant; a payload's offset and
// length; a varint's decoded value, since Parse had to decode it to validate
// it; a nested record's view.
func (f viewField) state(p *viewPlan) []viewState {
	i := strconv.Itoa(f.index)
	switch f.kind {
	case viewBoolean, viewFloat, viewDouble, viewFixed:
		if f.offsetConst == "" {
			return []viewState{{"at" + i, "int"}}
		}
	case viewInt:
		return []viewState{{"val" + i, "int32"}}
	case viewLong:
		return []viewState{{"val" + i, "int64"}}
	case viewEnum:
		return []viewState{{"val" + i, f.goType}}
	case viewBytes, viewString:
		if p.tail == f.index {
			// The tail's payload runs to the end of the record.
			return []viewState{{"at" + i, "int"}}
		}
		return []viewState{{"at" + i, "int"}, {"len" + i, "int"}}
	case viewRecord:
		return []viewState{{"rec" + i, f.goType}}
	}
	return nil
}

// accessors is the methods f is read through: one returning its value, a
// <Field>Bytes for a fixed or string returning the bytes it lies in, and a
// <Field>Len for the tail's payload. A null field has none.
func (f viewField) accessors(p *viewPlan) []string {
	if f.kind == viewNull {
		return nil
	}
	out := []string{f.accessor}
	if f.kind == viewFixed || f.kind == viewString {
		out = append(out, f.accessor+"Bytes")
	}
	if p.tail == f.index && (f.kind == viewBytes || f.kind == viewString) {
		out = append(out, f.accessor+"Len")
	}
	return out
}

// viewCursor is where Parse has reached: a constant while every field so far
// has had a constant width, and the variable pos from the first one that has
// not.
type viewCursor struct {
	constant bool
	at       int
}

// pos is the cursor as an expression.
func (c viewCursor) pos() string {
	if c.constant {
		return strconv.Itoa(c.at)
	}
	return "pos"
}

// plus is the cursor plus n as an expression.
func (c viewCursor) plus(n int) string {
	if c.constant {
		return strconv.Itoa(c.at + n)
	}
	if n == 0 {
		return "pos"
	}
	return fmt.Sprintf("pos+%d", n)
}

// rest is b from the cursor on.
func (c viewCursor) rest() string {
	if c.constant {
		if c.at == 0 {
			return "b"
		}
		return fmt.Sprintf("b[%d:]", c.at)
	}
	return "b[pos:]"
}

// short is the condition that fewer than n bytes are left at the cursor.
func (c viewCursor) short(n int) string {
	if c.constant {
		return fmt.Sprintf("len(b) < %d", c.at+n)
	}
	return fmt.Sprintf("len(b)-pos < %d", n)
}

// generateView writes r's view: its type and offset constants, Parse, the
// lengths, an accessor per field, Decode, and — under compare=true — Compare.
func generateView(cb *codeBuilder, p viewPlan, compare bool) {
	generateViewType(cb, p, compare)
	generateViewParse(cb, p)
	generateViewLengths(cb, p)
	for _, f := range p.fields {
		generateViewAccessors(cb, p, f)
	}
	generateViewDecode(cb, p)
	if compare {
		generateViewCompare(cb, p)
	}
}

func generateViewType(cb *codeBuilder, p viewPlan, compare bool) {
	cb.newline()
	cb.writef("// %s indexes a %s encoded at the start of a buffer, without decoding or\n", p.viewName, p.typeName)
	cb.writeln("// copying it: Parse reads the encoding's structure, and each accessor reads one")
	cb.writeln("// field where it lies.")
	cb.writeln("//")
	if p.tail >= 0 {
		cb.writef("// Parse needs only the encoding's prefix — everything up to the payload of the\n")
		cb.writef("// last field, %s — so a record's other fields, and the size of that payload,\n", p.tailPath())
		cb.writeln("// can be read before the payload has arrived. Complete reports whether it has.")
	} else {
		cb.writeln("// Parse needs the whole encoding, and a view Parse succeeded on is always")
		cb.writeln("// Complete.")
	}
	cb.writeln("//")
	cb.writef("// A %s, and every slice it returns, aliases the buffer it parsed, and is\n", p.viewName)
	cb.writeln("// valid only as long as that buffer is unchanged. Before a successful Parse, and")
	cb.writeln("// after a failed one, the view is empty, and every accessor panics.")
	cb.writef("type %s struct {\n", p.viewName)
	cb.writeln("\tb      []byte")
	cb.writeln("\tok     bool")
	cb.writeln("\tneed   int")
	cb.writeln("\tprefix int")
	cb.writeln("\tsize   int")
	for _, f := range p.fields {
		for _, s := range f.state(&p) {
			cb.writef("\t%s %s\n", s.name, s.goType)
		}
	}
	cb.writeln("}")

	var consts []viewField
	for _, f := range p.fields {
		if f.offsetConst != "" {
			consts = append(consts, f)
		}
	}
	if len(consts) > 0 {
		cb.newline()
		if len(consts) == 1 {
			cb.writef("// %s is the offset of field %s, which is the same in every\n", consts[0].offsetConst, consts[0].name)
			cb.writef("// encoding of a %s.\n", p.typeName)
		} else {
			cb.writef("// The offsets of %s's fields before its first variable-size field, which are\n", p.typeName)
			cb.writeln("// the same in every encoding.")
		}
		if len(consts) == 1 {
			cb.writef("const %s = %d\n", consts[0].offsetConst, consts[0].offset)
			return
		}
		cb.writeln("const (")
		for _, f := range consts {
			cb.writef("\t%s = %d\n", f.offsetConst, f.offset)
		}
		cb.writeln(")")
	}
}

// tailPath is the dotted path of the field the prefix ends in.
func (p *viewPlan) tailPath() string {
	f := p.fields[p.tail]
	if f.kind == viewRecord {
		return f.name + "." + f.nested.tailPath()
	}
	return f.name
}

func generateViewParse(cb *codeBuilder, p viewPlan) {
	v := p.viewName
	cb.newline()
	cb.writef("// Parse indexes the %s encoded at the start of b. b need hold only the\n", p.typeName)
	cb.writeln("// record's prefix, PrefixLen bytes, and anything after the record is left")
	cb.writeln("// alone. Parse never allocates.")
	cb.writeln("//")
	cb.writeln("// It returns io.ErrUnexpectedEOF when b ends before the prefix does, and more")
	cb.writeln("// bytes might parse; Need is then the length b must at least have. It returns")
	cb.writeln("// avro.ErrOverflow, avro.ErrNegativeLength or avro.ErrInvalidIndex when the")
	cb.writeln("// encoding is corrupt, which no more bytes can mend. The errors are avro-go's")
	cb.writeln("// sentinels, unwrapped. After any error the view is empty.")
	cb.writef("func (v *%s) Parse(b []byte) error {\n", v)

	cur := viewCursor{constant: true}
	for i, f := range p.fields {
		after := p.suffix[i]
		if i > 0 {
			cb.newline()
		}
		if f.width < 0 && cur.constant {
			cb.writef("\tpos := %d\n\n", cur.at)
			cur.constant = false
		}
		cb.writef("\t// %s: %s\n", f.name, f.typ)

		switch f.kind {
		case viewNull:
			// No bytes, nothing to index.
		case viewBoolean, viewFloat, viewDouble, viewFixed:
			if f.width > 0 {
				cb.writef("\tif %s {\n\t\treturn v.fail(io.ErrUnexpectedEOF, %s)\n\t}\n", cur.short(f.width), cur.plus(f.width+after))
			}
			if f.offsetConst == "" {
				cb.writef("\tv.at%d = pos\n", f.index)
			}
			if cur.constant {
				cur.at += f.width
			} else if f.width > 0 {
				cb.writef("\tpos += %d\n", f.width)
			}
		case viewInt, viewLong:
			consume := "ConsumeLong"
			if f.kind == viewInt {
				consume = "ConsumeInt"
			}
			cb.writeln("\t{")
			cb.writef("\t\tx, k := avro.%s(b[pos:])\n", consume)
			cb.writef("\t\tif k < 0 {\n\t\t\treturn v.fail(avro.ConsumeError(k), len(b)+%d)\n\t\t}\n", 1+after)
			cb.writef("\t\tv.val%d = x\n", f.index)
			cb.writeln("\t\tpos += k")
			cb.writeln("\t}")
		case viewEnum:
			cb.writeln("\t{")
			cb.writeln("\t\ti, k := 0, 1")
			cond := "len(b) > pos && b[pos]&0x81 == 0"
			if f.symbols <= 63 {
				cond += fmt.Sprintf(" && int(b[pos]>>1) < %d", f.symbols)
			}
			cb.writef("\t\tif %s {\n", cond)
			cb.writeln("\t\t\ti = int(b[pos] >> 1)")
			cb.writeln("\t\t} else {")
			cb.writef("\t\t\ti, k = avro.ConsumeIndex(b[pos:], %d)\n", f.symbols)
			cb.writef("\t\t\tif k < 0 {\n\t\t\t\treturn v.fail(avro.ConsumeError(k), len(b)+%d)\n\t\t\t}\n", 1+after)
			cb.writeln("\t\t}")
			cb.writef("\t\tv.val%d = %s(i)\n", f.index, f.goType)
			cb.writeln("\t\tpos += k")
			cb.writeln("\t}")
		case viewBytes, viewString:
			cb.writeln("\t{")
			cb.writeln("\t\tl, k := 0, 1")
			cb.writeln("\t\tif len(b) > pos && b[pos]&0x81 == 0 {")
			cb.writeln("\t\t\tl = int(b[pos] >> 1)")
			cb.writeln("\t\t} else {")
			cb.writeln("\t\t\tl, k = avro.ConsumeLength(b[pos:])")
			cb.writef("\t\t\tif k < 0 {\n\t\t\t\treturn v.fail(avro.ConsumeError(k), len(b)+%d)\n\t\t\t}\n", 1+after)
			cb.writeln("\t\t}")
			if p.tail == f.index {
				cb.writef("\t\tv.at%d = pos + k\n", f.index)
				cb.writeln("\t\tv.prefix, v.size = pos+k, pos+k+l")
			} else {
				cb.writef("\t\tif len(b)-pos-k < l {\n\t\t\treturn v.fail(io.ErrUnexpectedEOF, pos+k+l+%d)\n\t\t}\n", after)
				cb.writef("\t\tv.at%d, v.len%d = pos+k, l\n", f.index, f.index)
				cb.writeln("\t\tpos += k + l")
			}
			cb.writeln("\t}")
		case viewRecord:
			rec := fmt.Sprintf("v.rec%d", f.index)
			cb.writef("\tif err := %s.Parse(%s); err != nil {\n", rec, cur.rest())
			cb.writef("\t\treturn v.fail(err, %s+%s.need)\n", cur.plus(after), rec)
			cb.writeln("\t}")
			switch {
			case p.tail == f.index:
				cb.writef("\tv.prefix, v.size = pos+%s.prefix, pos+%s.size\n", rec, rec)
			case f.width >= 0:
				// A record of constant width is complete once it parses.
				if cur.constant {
					cur.at += f.width
				} else if f.width > 0 {
					cb.writef("\tpos += %d\n", f.width)
				}
			default:
				cb.writef("\tif len(b)-pos < %s.size {\n", rec)
				cb.writef("\t\treturn v.fail(io.ErrUnexpectedEOF, pos+%s.size+%d)\n", rec, after)
				cb.writeln("\t}")
				cb.writef("\tpos += %s.size\n", rec)
			}
		}
	}

	if len(p.fields) > 0 {
		cb.newline()
	}
	cb.writeln("\tv.b = b")
	cb.writeln("\tv.ok = true")
	if p.tail < 0 {
		cb.writef("\tv.prefix, v.size = %s, %s\n", cur.pos(), cur.pos())
	}
	cb.writeln("\tv.need = v.prefix")
	cb.writeln("\treturn nil")
	cb.writeln("}")

	if p.needsFail() {
		cb.newline()
		cb.writeln("// fail empties v, records need as what Parse needs when err is")
		cb.writeln("// io.ErrUnexpectedEOF, and returns err.")
		cb.writef("func (v *%s) fail(err error, need int) error {\n", v)
		cb.writef("\t*v = %s{}\n", v)
		cb.writeln("\tif err == io.ErrUnexpectedEOF {")
		cb.writeln("\t\tv.need = need")
		cb.writeln("\t}")
		cb.writeln("\treturn err")
		cb.writeln("}")
	}
}

// writeViewEmpty writes the panic every accessor of an empty view makes.
func writeViewEmpty(cb *codeBuilder, p viewPlan, method string) {
	cb.writef("\tif !v.ok {\n\t\tpanic(\"%s.%s called on an empty view\")\n\t}\n", p.viewName, method)
}

// writeViewIncomplete writes the panic a method that reads the tail's payload
// makes on a view that does not hold it.
func writeViewIncomplete(cb *codeBuilder, p viewPlan, method string) {
	writeViewEmpty(cb, p, method)
	cb.writef("\tif len(v.b) < v.size {\n\t\tpanic(\"%s.%s called on an incomplete view\")\n\t}\n", p.viewName, method)
}

func generateViewLengths(cb *codeBuilder, p viewPlan) {
	v := p.viewName

	cb.newline()
	cb.writeln("// Need is the length the buffer given to the last Parse had to have at least:")
	cb.writeln("// after io.ErrUnexpectedEOF, more than that buffer held and no more than")
	cb.writeln("// PrefixLen, so that no shorter buffer can parse and one at least this long")
	cb.writeln("// parses, is corrupt, or needs strictly more. It is PrefixLen after a")
	cb.writeln("// successful Parse, and 0 on a view never parsed or one found corrupt.")
	cb.writef("func (v *%s) Need() int {\n\treturn v.need\n}\n", v)

	cb.newline()
	if p.tail >= 0 {
		cb.writef("// PrefixLen is the length of the record's prefix: its encoding through the\n")
		cb.writef("// length of %s, which is all Parse reads.\n", p.tailPath())
	} else {
		cb.writeln("// PrefixLen is the length of the record's prefix, which is the whole record.")
	}
	cb.writef("func (v *%s) PrefixLen() int {\n", v)
	writeViewEmpty(cb, p, "PrefixLen")
	cb.writeln("\treturn v.prefix")
	cb.writeln("}")

	cb.newline()
	cb.writeln("// Len is the length of the whole record's encoding, which the prefix alone")
	cb.writeln("// determines.")
	cb.writef("func (v *%s) Len() int {\n", v)
	writeViewEmpty(cb, p, "Len")
	cb.writeln("\treturn v.size")
	cb.writeln("}")

	cb.newline()
	cb.writeln("// Complete reports whether the buffer v parsed holds the whole record, Len")
	cb.writeln("// bytes. It is false on an empty view, rather than a panic.")
	cb.writef("func (v *%s) Complete() bool {\n", v)
	cb.writeln("\treturn v.ok && len(v.b) >= v.size")
	cb.writeln("}")

	cb.newline()
	cb.writeln("// Bytes is the record's encoding, the first Len bytes of the buffer v parsed.")
	cb.writeln("// It panics unless v is Complete.")
	cb.writef("func (v *%s) Bytes() []byte {\n", v)
	writeViewIncomplete(cb, p, "Bytes")
	cb.writeln("\treturn v.b[:v.size:v.size]")
	cb.writeln("}")
}

// at is the expression for the offset a constant-width field begins at.
func (f viewField) at() string {
	if f.offsetConst != "" {
		return f.offsetConst
	}
	return fmt.Sprintf("v.at%d", f.index)
}

// payload is the expression for a bytes or string field's payload.
func (f viewField) payload(p viewPlan) string {
	if p.tail == f.index {
		return fmt.Sprintf("v.b[v.at%d:v.size:v.size]", f.index)
	}
	return fmt.Sprintf("v.b[v.at%[1]d : v.at%[1]d+v.len%[1]d : v.at%[1]d+v.len%[1]d]", f.index)
}

func generateViewAccessors(cb *codeBuilder, p viewPlan, f viewField) {
	v := p.viewName
	isTail := p.tail == f.index
	guard := func(method string) {
		if isTail && (f.kind == viewBytes || f.kind == viewString) {
			writeViewIncomplete(cb, p, method)
			return
		}
		writeViewEmpty(cb, p, method)
	}
	tailNote := func() {
		if isTail {
			cb.writeln("//")
			cb.writeln("// It panics unless v is Complete.")
		}
	}

	switch f.kind {
	case viewNull:
		return
	case viewBoolean:
		cb.newline()
		cb.writef("// %s is field %s.\n", f.accessor, f.name)
		cb.writef("func (v *%s) %s() bool {\n", v, f.accessor)
		guard(f.accessor)
		cb.writef("\treturn v.b[%s] != 0\n", f.at())
		cb.writeln("}")
	case viewFloat, viewDouble:
		goType, consume := "float32", "ConsumeFloat"
		if f.kind == viewDouble {
			goType, consume = "float64", "ConsumeDouble"
		}
		cb.newline()
		cb.writef("// %s is field %s.\n", f.accessor, f.name)
		cb.writef("func (v *%s) %s() %s {\n", v, f.accessor, goType)
		guard(f.accessor)
		cb.writef("\tx, _ := avro.%s(v.b[%s:])\n", consume, f.at())
		cb.writeln("\treturn x")
		cb.writeln("}")
	case viewInt, viewLong, viewEnum:
		goType := "int32"
		switch f.kind {
		case viewLong:
			goType = "int64"
		case viewEnum:
			goType = f.goType
		}
		cb.newline()
		cb.writef("// %s is field %s.\n", f.accessor, f.name)
		cb.writef("func (v *%s) %s() %s {\n", v, f.accessor, goType)
		guard(f.accessor)
		cb.writef("\treturn v.val%d\n", f.index)
		cb.writeln("}")
	case viewFixed:
		span := fmt.Sprintf("%[1]s : %[1]s+%[2]d", f.at(), f.size)
		cb.newline()
		cb.writef("// %s is a copy of field %s.\n", f.accessor, f.name)
		cb.writef("func (v *%s) %s() %s {\n", v, f.accessor, f.goType)
		guard(f.accessor)
		cb.writef("\treturn %s(v.b[%s])\n", f.goType, span)
		cb.writeln("}")
		cb.newline()
		cb.writef("// %sBytes is the bytes field %s lies in, a slice of the buffer v parsed.\n", f.accessor, f.name)
		cb.writef("func (v *%s) %sBytes() []byte {\n", v, f.accessor)
		guard(f.accessor + "Bytes")
		cb.writef("\treturn v.b[%s : %s+%d]\n", span, f.at(), f.size)
		cb.writeln("}")
	case viewBytes:
		cb.newline()
		cb.writef("// %s is field %s, a slice of the buffer v parsed whose capacity is its\n", f.accessor, f.name)
		cb.writeln("// length.")
		tailNote()
		cb.writef("func (v *%s) %s() []byte {\n", v, f.accessor)
		guard(f.accessor)
		cb.writef("\treturn %s\n", f.payload(p))
		cb.writeln("}")
	case viewString:
		cb.newline()
		cb.writef("// %s is a copy of field %s.\n", f.accessor, f.name)
		tailNote()
		cb.writef("func (v *%s) %s() string {\n", v, f.accessor)
		guard(f.accessor)
		cb.writef("\treturn string(%s)\n", f.payload(p))
		cb.writeln("}")
		cb.newline()
		cb.writef("// %sBytes is the bytes field %s lies in, a slice of the buffer v parsed\n", f.accessor, f.name)
		cb.writeln("// whose capacity is its length.")
		tailNote()
		cb.writef("func (v *%s) %sBytes() []byte {\n", v, f.accessor)
		guard(f.accessor + "Bytes")
		cb.writef("\treturn %s\n", f.payload(p))
		cb.writeln("}")
	case viewRecord:
		cb.newline()
		cb.writef("// %s is field %s's view, which shares the buffer v parsed. It is part of v:\n", f.accessor, f.name)
		cb.writeln("// parsing v again changes it, and parsing it changes what v reports.")
		cb.writef("func (v *%s) %s() *%s {\n", v, f.accessor, f.goType)
		guard(f.accessor)
		cb.writef("\treturn &v.rec%d\n", f.index)
		cb.writeln("}")
	}

	if isTail && (f.kind == viewBytes || f.kind == viewString) {
		cb.newline()
		cb.writef("// %sLen is the length of field %s's payload, which is known from the\n", f.accessor, f.name)
		cb.writeln("// prefix alone.")
		cb.writef("func (v *%s) %sLen() int {\n", v, f.accessor)
		writeViewEmpty(cb, p, f.accessor+"Len")
		cb.writef("\treturn v.size - v.at%d\n", f.index)
		cb.writeln("}")
	}
}

func generateViewDecode(cb *codeBuilder, p viewPlan) {
	cb.newline()
	cb.writef("// Decode sets x to the record v indexes: a bytes field to a slice of the\n")
	cb.writeln("// buffer v parsed, a string field to a copy, and a fixed field by value. It")
	cb.writeln("// allocates only for string fields, and panics unless v is Complete.")
	cb.writef("func (v *%s) Decode(x *%s) {\n", p.viewName, p.typeName)
	writeViewIncomplete(cb, p, "Decode")
	for _, f := range p.fields {
		switch f.kind {
		case viewNull:
		case viewRecord:
			cb.writef("\tv.rec%d.Decode(&x.%s)\n", f.index, f.accessor)
		default:
			cb.writef("\tx.%s = v.%s()\n", f.accessor, f.accessor)
		}
	}
	cb.writeln("}")
}

func generateViewCompare(cb *codeBuilder, p viewPlan) {
	cb.newline()
	cb.writef("// Compare orders the records v and w index by the Avro specification's sort\n")
	cb.writef("// order, as %s orders their encodings: -1, 0 or +1 as v's record\n", p.compare)
	cb.writeln("// sorts before, equal to or after w's. It never allocates.")
	cb.writeln("//")
	if p.tailCompared {
		cb.writef("// %s is compared, so Compare panics unless both views are Complete.\n", p.tailPath())
	} else {
		cb.writeln("// It reads nothing past either record's prefix, and panics only on an empty")
		cb.writeln("// view.")
	}
	cb.writef("func (v *%s) Compare(w *%s) int {\n", p.viewName, p.viewName)
	cb.writef("\tif !v.ok || !w.ok {\n\t\tpanic(\"%s.Compare called on an empty view\")\n\t}\n", p.viewName)
	if p.tailCompared {
		cb.writef("\tif len(v.b) < v.size || len(w.b) < w.size {\n\t\tpanic(\"%s.Compare called on an incomplete view\")\n\t}\n", p.viewName)
	}
	cb.writef("\tc, _, _ := %s(v.b, w.b)\n", p.compare)
	cb.writeln("\treturn c")
	cb.writeln("}")
}
