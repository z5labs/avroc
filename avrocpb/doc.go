// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Package avrocpb is the Go form of the resolved IR every avroc generator is
// handed.
//
// It is the one package in this module a third-party generator is expected to
// import, which is why it lives at an importable path rather than under
// internal/: the IR is a contract, and a contract the only audience for it
// cannot import is not one. docs/ir/SPEC.md is the contract itself; this
// package is only its spelling in Go.
//
// Every file here except this one is generated from proto/ by protoc-gen-go
// and protoc-gen-go-grpc. Do not edit them — change the .proto and regenerate.
// The Go module tag this package is released under moves for Go's reasons and
// is not the IR version; that is GenerateRequest.version, a single monotonic
// integer, and reading it before anything else is a consumer's first
// obligation.
package avrocpb
