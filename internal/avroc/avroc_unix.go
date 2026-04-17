// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

//go:build !windows

package avroc

import (
	"io/fs"
	"strings"
)

func isGeneratorExecutable(name string, mode fs.FileMode) bool {
	return strings.HasPrefix(name, "avroc-gen-") && mode.IsRegular() && mode&0o111 != 0
}

func generatorKey(name string) string {
	return name
}
