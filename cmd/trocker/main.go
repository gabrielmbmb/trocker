package main

import (
	"context"
	"errors"
	"os"

	"github.com/gabrielmbmb/trocker/internal/cli"
	"github.com/gabrielmbmb/trocker/internal/native"
)

const exitUsage = 2

var version = "dev"

func main() {
	ctx, cancel := native.NotifyContext(context.Background())
	defer cancel()
	if err := cli.RunContext(ctx, os.Args[1:], version, os.Stdin, os.Stdout, os.Stderr); err != nil {
		if child, ok := errors.AsType[*native.ExitError](err); ok {
			os.Exit(child.Code)
		}
		if err := cli.WriteError(os.Stderr, err); err != nil {
			os.Exit(1)
		}
		if errors.Is(err, cli.ErrUsage) {
			os.Exit(exitUsage)
		}
		os.Exit(1)
	}
}
