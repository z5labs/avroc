// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package cli

import (
	"io/fs"
	"log/slog"
)

type Environment interface {
	LookupEnv(key string) (string, bool)
}

type EnvironmentFunc func(string) (string, bool)

func (f EnvironmentFunc) LookupEnv(key string) (string, bool) {
	return f(key)
}

type Context struct {
	Log  *slog.Logger
	Env  Environment
	Fs   fs.FS
	Args []string
}
