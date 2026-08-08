// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenpcf

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"

	"github.com/sourcegraph/conc/pool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func Main(ctx context.Context, cli cli.Context) int {
	flags := flag.NewFlagSet("avroc-gen-pcf", flag.ContinueOnError)
	flags.Usage = func() {}

	err := flags.Parse(cli.Args)
	if errors.Is(err, flag.ErrHelp) {
		err = printHelp(os.Stdout)
		if err != nil {
			cli.Log.ErrorContext(ctx, "failed to print help", slog.Any("error", err))
			return 1
		}
		return 0
	}
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to parse flags", slog.Any("error", err))
		return 1
	}

	args := flags.Args()
	if len(args) != 1 {
		cli.Log.ErrorContext(ctx, "unix socket address must be provided")
		return 1
	}

	ls, err := net.ListenUnix("unix", &net.UnixAddr{
		Name: args[0],
		Net:  "unix",
	})
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to listen on unix socket", slog.Any("error", err))
		return 1
	}

	generatorService := &generatorService{}

	srv := grpc.NewServer(
		grpc.Creds(insecure.NewCredentials()),
	)
	avrocpb.RegisterGeneratorServer(srv, generatorService)

	pool := pool.New().WithContext(ctx)

	pool.Go(func(ctx context.Context) error {
		<-ctx.Done()
		srv.GracefulStop()
		return nil
	})
	pool.Go(func(ctx context.Context) error {
		return srv.Serve(ls)
	})

	err = pool.Wait()
	if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		cli.Log.ErrorContext(ctx, "failed to serve gRPC server", slog.Any("error", err))
		return 1
	}

	return 0
}

func printHelp(w io.Writer) error {
	_, err := fmt.Fprintln(w, "Usage: avroc-gen-pcf <unix socket address>")
	return err
}
