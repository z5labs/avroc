// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"io/fs"
	"path/filepath"
	"strings"
)

func isGeneratorExecutable(name string, _ fs.FileMode) bool {
	return strings.HasPrefix(name, "avroc-gen-") && strings.EqualFold(filepath.Ext(name), ".exe")
}

func generatorKey(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}
