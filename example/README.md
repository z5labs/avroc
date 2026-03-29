# avroc Example

This example demonstrates using `avroc` to generate code from an Avro IDL schema.

## Schema

The `schema.avdl` file contains an example schema based on the [Apache Avro documentation](https://avro.apache.org/docs/1.12.0/idl-language/#schemaavdl).

## Generating Code

To generate code from the schema:

```bash
# Build avroc tools (from repo root)
go build -o ./bin/avroc ./cmd/avroc
go build -o ./bin/avroc-gen-go ./cmd/avroc-gen-go
go build -o ./bin/avroc-gen-json ./cmd/avroc-gen-json

# Generate Go types and JSON schema
PATH="$PWD/bin:$PATH" ./bin/avroc -go_out=example/gen -go_opt=package_name=avro -go_opt=encoding=single_object -json_out=example example/schema.avdl
```

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
