// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package main

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/z5labs/avroc/internal/avroc"
	"github.com/z5labs/avroc/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Kill, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	code := avroc.Main(ctx, cli.Context{
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			AddSource: true,
		})),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			return os.LookupEnv(key)
		}),
		OpenDir: func(dir string) fs.FS { return os.DirFS(dir) },
		Args:    os.Args[1:],
	})
	os.Exit(code)
}
