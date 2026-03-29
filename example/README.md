# avroc Example

This example demonstrates using `avroc` to generate Go types from an Avro IDL schema.

## Schema

The `schema.avdl` file contains an example schema based on the [Apache Avro documentation](https://avro.apache.org/docs/1.12.0/idl-language/#schemaavdl).

## Generating Go Types

To generate Go types from the schema:

```bash
# Build avroc tools (from repo root)
go build -o ./bin/avroc ./cmd/avroc
go build -o ./bin/avroc-gen-go ./cmd/avroc-gen-go

# Generate Go types
PATH="$PWD/bin:$PATH" ./bin/avroc -go_out=example/gen example/schema.avdl
```

## Generated Output

The generator produces a Go file in `gen/` containing:

- `Kind` - an enum type with `KindFOO`, `KindBAR`, `KindBAZ` constants
- `MD5` - a fixed 16-byte type
- `NullableHashUnion` - a union interface with `NullableHashUnionNull` and `NullableHashUnionMD5` implementations
- `TestRecord` - a struct with fields for name, kind, hash, and nullable hash
