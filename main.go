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

	"github.com/z5labs/avroc/internal/avroc"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Kill, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	code := avroc.Main(ctx, avroc.NewCLI(
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			AddSource: true,
		})),
		avroc.EnvironmentFunc(func(key string) (string, bool) {
			return os.LookupEnv(key)
		}),
		os.DirFS("/"),
	))
	os.Exit(code)
}
