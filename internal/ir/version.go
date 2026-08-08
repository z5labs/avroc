// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import "fmt"

// Version is the IR contract version this build of avroc writes onto every
// descriptor it emits, and the only version the generators in this repository
// understand. It is the integer docs/ir/SPEC.md's "The version field"
// specifies: monotonic, and deliberately not a major and a minor.
//
// Bumping it is the last step of a breaking change, not a routine one. A change
// is breaking, and requires this constant to advance to the next integer, when
// a conforming consumer cannot ignore it and remain correct — removing a field
// or reusing its number, changing what a field means, or adding a member to a
// closed set such as the type constructors, TypeRefKind or SortOrder. Adding a
// field a consumer can ignore and still handle the descriptor correctly leaves
// it alone; that is what makes an additive change free, and it is the only
// reason this constant can stay put across most edits. docs/ir/SPEC.md's
// "Compatibility" is normative for which is which.
//
// One bump covers every breaking change made since the last released version:
// the version identifies a contract, not an edit.
//
// This is neither avroc's release version nor the Go module tag of the package
// the generated IR types live in. One IR version outlives many of both.
const Version int32 = 1

// CheckVersion is the first thing a consumer of a descriptor does with it.
//
// docs/ir/SPEC.md requires the version to be read before anything else, and a
// version the consumer does not know to fail the invocation with a diagnostic
// naming both versions, rather than the consumer proceeding on the parts it
// recognises. Reading it first is the whole of the rule: a consumer that walks
// the schemas and checks the version afterwards has already spent its
// diagnostics on a document it was never entitled to read, so what a user sees
// is a complaint about a type they cannot fix instead of a plugin that is too
// old for the avroc in front of it.
//
// It takes the integer rather than the message carrying it, because which
// message that is belongs to the contract that delivers the descriptor, not to
// the version rule.
func CheckVersion(version int32) error {
	if version == Version {
		return nil
	}
	if version == 0 {
		// Zero is both "the field was never set" and "a producer wrote 0",
		// which no conforming producer does. Either way it is not a version
		// this consumer knows, and saying so is more use than printing 0.
		return fmt.Errorf("descriptor carries no IR version; this generator understands IR version %d", Version)
	}
	return fmt.Errorf("descriptor is IR version %d; this generator understands IR version %d", version, Version)
}
