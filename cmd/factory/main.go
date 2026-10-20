package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/anskumar/ai-coding-factory/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	code := 0
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory:", err)
		var usage *cli.UsageError
		switch {
		case errors.As(err, &usage):
			code = 2
		case errors.Is(err, context.Canceled):
			code = 130
		default:
			code = 1
		}
	}
	os.Exit(code)
}
