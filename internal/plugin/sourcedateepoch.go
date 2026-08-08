// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"fmt"
	"strconv"
	"time"

	"github.com/z5labs/avroc/internal/cli"
)

// SourceDateEpochVar is the environment variable docs/plugin/SPEC.md's
// "Determinism" names, and the only part of the environment a generator's
// output may vary with.
const SourceDateEpochVar = "SOURCE_DATE_EPOCH"

// SourceDateEpoch reports the timestamp a generator must use where a timestamp
// in its output is genuinely unavoidable, and whether there is one at all.
//
// It is the only clock in this repository's generator half, and it is not a
// clock: the value comes from the environment, so two runs of the same
// executable over the same descriptor produce the same timestamp however far
// apart they are. docs/plugin/SPEC.md's "Determinism" (#120) is normative —
// a plugin MUST use SOURCE_DATE_EPOCH when it is set and MUST NOT read the
// clock — and internal/plugin.TestNoGeneratorReadsTheClock is what holds
// every generator here to it.
//
// The three cases, and why each is what it is:
//
//   - Set to a count of seconds since the Unix epoch: that instant in UTC,
//     with ok true. UTC rather than the local zone because TZ is part of the
//     environment a generator's output may not vary with, and a timestamp
//     rendered in the builder's zone varies with it.
//   - Unset, or set to the empty string: the zero time, with ok false, and the
//     caller SHOULD omit the timestamp rather than reach for the clock. A
//     missing generation date costs a reader nothing and a present one costs
//     every regeneration a diff. Empty counts as unset because an exported-but-
//     empty variable is how a shell spells "no value", and reading it as a
//     malformed one would fail runs that meant to set nothing.
//   - Set to anything else: an error, which the caller reports rather than
//     works around. Falling back to the clock on a malformed value would make
//     the output nondeterministic exactly when somebody was trying to pin it
//     down, which is the failure this whole rule exists to prevent. A negative
//     count is malformed for the same reason it is in the Reproducible Builds
//     definition: it is a typo, not a date before 1970 that anybody meant.
//
// No generator here currently emits a timestamp — nothing in Go source, an
// Avro schema in JSON or a Parsing Canonical Form needs one — so this has no
// caller in this repository yet. It exists because the alternative to one
// tested implementation is the first generator that needs a date inventing its
// own, and every implementation of this that reads the clock as a fallback is
// a generator that is deterministic until the day somebody looks.
func SourceDateEpoch(env cli.Environment) (t time.Time, ok bool, err error) {
	raw, set := env.LookupEnv(SourceDateEpochVar)
	if !set || raw == "" {
		return time.Time{}, false, nil
	}

	seconds, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr != nil {
		return time.Time{}, false, fmt.Errorf("%s=%q is not a decimal count of seconds since the Unix epoch", SourceDateEpochVar, raw)
	}
	if seconds < 0 {
		return time.Time{}, false, fmt.Errorf("%s=%q is negative", SourceDateEpochVar, raw)
	}

	return time.Unix(seconds, 0).UTC(), true, nil
}
