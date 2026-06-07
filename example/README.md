# avroc Example

This example demonstrates using `avroc` to generate code from an Avro IDL schema.

## Schema

The `schema.avdl` file contains an example schema based on the [Apache Avro documentation](https://avro.apache.org/docs/1.12.0/idl-language/#schemaavdl).

## Manifest

The generators and their configuration are declared in [`avroc.json`](avroc.json): the
shared input (`schema.avdl`) plus a `go`, `json`, and `pcf` generator, each with its output
directory and options. `avroc init` scaffolds a starter manifest; this example ships a
ready-made one.

## Generating Code

To generate code from the schema:

```bash
# Install the avroc tools (so they are discovered on PATH as avroc-gen-<name>)
go install ./cmd/...

# From this example directory, run the generators declared in avroc.json
cd example
avroc generate
```

This reads `avroc.json` and produces Go types (`gen/`), an Avro JSON schema
(`test_record.avsc`), and a Parsing Canonical Form file (`pcf/test_record.avsc`).

## Generated Output

### Go (`gen/`)

The Go generator produces a Go file in `gen/` containing:

- `Kind` - an enum type with `KindFOO`, `KindBAR`, `KindBAZ` constants
- `MD5` - a fixed 16-byte type
- `TestRecordNullableHashUnion` - a union interface with `TestRecordNullableHashUnionNull` and `TestRecordNullableHashUnionMD5` implementations
- `TestRecord` - a struct with fields for name, kind, hash, and nullable hash

With `encoding=single_object`, the primary record type also includes a `Fingerprint()` method that returns the precomputed CRC-64-AVRO schema fingerprint for [Avro Single Object Encoding](https://avro.apache.org/docs/1.12.0/specification/#single-object-encoding).

### JSON Schema (`test_record.avsc`)

The JSON generator produces an Avro JSON schema file (`.avsc`) following the [Avro specification](https://avro.apache.org/docs/1.12.0/specification/#schema-declaration). Named types are inlined on first use and referenced by name afterwards.

### Parsing Canonical Form (`pcf/test_record.avsc`)

The PCF generator produces a compact JSON file containing the [Avro Parsing Canonical Form](https://avro.apache.org/docs/1.12.0/specification/#parsing-canonical-form-for-schemas) of the schema. Named types are inlined on first use and referenced by their fully-qualified name on subsequent uses. This form is useful for schema validation, fingerprinting, and ensuring interoperability across systems.
