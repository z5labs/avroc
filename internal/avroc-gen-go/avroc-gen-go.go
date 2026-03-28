package avrocgengo

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/z5labs/avroc/internal/cli"
)

func Main(ctx context.Context, cli cli.Context) int {
	flags := flag.NewFlagSet("avroc-gen-go", flag.ContinueOnError)
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
		return 1
	}

	return 0
}

func printHelp(w io.Writer) error {
	_, err := fmt.Fprintln(w, "Usage: avroc-gen-go <unix socket address>")
	return err
}
