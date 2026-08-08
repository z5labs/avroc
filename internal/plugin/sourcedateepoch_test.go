// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"testing"
	"time"

	"github.com/z5labs/avroc/internal/cli"
)

// envValue is an environment holding exactly one variable, so that a case can
// say what SOURCE_DATE_EPOCH is without anything else being readable.
func envValue(value string) cli.Environment {
	return cli.EnvironmentFunc(func(key string) (string, bool) {
		if key != SourceDateEpochVar {
			return "", false
		}
		return value, true
	})
}

func envUnset() cli.Environment {
	return cli.EnvironmentFunc(func(string) (string, bool) { return "", false })
}

func TestSourceDateEpoch(t *testing.T) {
	testCases := []struct {
		name string
		env  cli.Environment
		want time.Time
		ok   bool
	}{
		{
			name: "unset omits the timestamp",
			env:  envUnset(),
			ok:   false,
		},
		{
			name: "empty is unset",
			env:  envValue(""),
			ok:   false,
		},
		{
			name: "the epoch itself is a value, not an absence",
			env:  envValue("0"),
			want: time.Unix(0, 0).UTC(),
			ok:   true,
		},
		{
			name: "a count of seconds",
			env:  envValue("1717372800"),
			want: time.Date(2024, time.June, 3, 0, 0, 0, 0, time.UTC),
			ok:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := SourceDateEpoch(tc.env)
			if err != nil {
				t.Fatalf("SourceDateEpoch returned an error: %v", err)
			}
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !got.Equal(tc.want) {
				t.Errorf("time = %v, want %v", got, tc.want)
			}
			if ok && got.Location() != time.UTC {
				t.Errorf("location = %v, want UTC", got.Location())
			}
		})
	}
}

// TestSourceDateEpochRejects covers the values that must fail the run rather
// than fall back to the clock. A malformed value is somebody trying to pin the
// output down and getting the spelling wrong, so the one thing that must not
// happen is a build that quietly carries on being nondeterministic.
func TestSourceDateEpochRejects(t *testing.T) {
	testCases := []struct {
		name  string
		value string
	}{
		{name: "not a number", value: "yesterday"},
		{name: "an RFC 3339 timestamp", value: "2024-06-03T00:00:00Z"},
		{name: "fractional seconds", value: "1717372800.5"},
		{name: "surrounded by whitespace", value: " 1717372800 "},
		{name: "hexadecimal", value: "0x665d3f00"},
		{name: "negative", value: "-1"},
		{name: "wider than an int64", value: "99999999999999999999"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := SourceDateEpoch(envValue(tc.value))
			if err == nil {
				t.Fatalf("expected an error for %q, got %v (ok=%v)", tc.value, got, ok)
			}
			if ok {
				t.Errorf("ok = true alongside an error")
			}
			if !got.IsZero() {
				t.Errorf("time = %v, want the zero time alongside an error", got)
			}
		})
	}
}

// TestSourceDateEpochReadsNothingElse pins the half of the rule that is about
// the rest of the environment: a generator's output may vary with
// SOURCE_DATE_EPOCH and with nothing else, so this is the only key the lookup
// is allowed to reach for. TZ in particular would change the rendered date
// without changing the instant.
func TestSourceDateEpochReadsNothingElse(t *testing.T) {
	var read []string
	env := cli.EnvironmentFunc(func(key string) (string, bool) {
		read = append(read, key)
		return "1717372800", true
	})

	if _, _, err := SourceDateEpoch(env); err != nil {
		t.Fatalf("SourceDateEpoch returned an error: %v", err)
	}

	if len(read) != 1 || read[0] != SourceDateEpochVar {
		t.Errorf("looked up %v, want exactly [%s]", read, SourceDateEpochVar)
	}
}
