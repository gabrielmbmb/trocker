package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrUsage identifies invalid arguments for the executable's usage exit status.
var ErrUsage = errors.New("invalid command line")

// Run executes a fresh command tree so flags and output are isolated per invocation.
func Run(args []string, version string, output io.Writer) error {
	return RunContext(context.Background(), args, version, strings.NewReader(""), output, io.Discard)
}

// RunContext connects process cancellation and explicit streams to the CLI.
func RunContext(ctx context.Context, args []string, version string, input io.Reader, output, diagnostics io.Writer) error {
	writer := &outputWriter{Writer: output}
	command := newRootCommand(version, writer)
	// A nil slice makes Cobra read the host process's arguments.
	command.SetArgs(append([]string{}, args...))
	command.SetIn(input)
	command.SetErr(diagnostics)

	err := command.ExecuteContext(context.WithValue(ctx, argumentsKey{}, args))
	if writer.err != nil {
		return fmt.Errorf("write command output: %w", writer.err)
	}
	return err
}
