// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	avrocgenpcf "github.com/z5labs/avroc/internal/avroc-gen-pcf"
	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/plugin"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Kill, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	code := avrocgenpcf.Main(ctx, cli.Context{
		// Diagnostics go to standard error in docs/plugin/SPEC.md's format, so
		// that avroc parses them back into its own structured log at the level
		// the generator meant rather than surfacing a slog dump verbatim.
		Log: slog.New(plugin.NewDiagnosticHandler(os.Stderr, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			return os.LookupEnv(key)
		}),
		Args: os.Args[1:],
	})
	os.Exit(code)
}
