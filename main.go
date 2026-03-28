package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/z5labs/avro-go/idl"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Kill, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	code := run(ctx)
	os.Exit(code)
}

func run(ctx context.Context) int {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		AddSource: true,
	}))

	fs := flag.NewFlagSet("avroc", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Println("avroc - A tool for working with Avro files")

		fmt.Println("Usage:")
		fmt.Println("  avroc [options] FILE...")

		fmt.Println("Flags:")
		fs.PrintDefaults()
	}

	err := fs.Parse(os.Args[1:])
	if err != nil {
		log.ErrorContext(ctx, "failed to parse flags", slog.Any("error", err))
		return 1
	}

	args := fs.Args()
	if len(args) == 0 {
		log.ErrorContext(ctx, "no IDL files provided")
		fs.Usage()
		return 1
	}

	files := make([]*idl.File, len(args))
	for i, arg := range args {
		f, err := parseIDL(arg)
		if err != nil {
			log.ErrorContext(ctx, "failed to parse IDL file", slog.String("file", arg), slog.Any("error", err))
			return 1
		}

		files[i] = f
	}
	log.InfoContext(ctx, "parsed idl files", slog.Int("num_of_files", len(files)))

	return 0
}

func parseIDL(path string) (_ *idl.File, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, f.Close())
	}()

	return idl.Parse(f)
}
