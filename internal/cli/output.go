package cli

import (
	"io"

	"github.com/moby/term"
	"github.com/spf13/cobra"
)

// Cobra's help printer does not return write errors to Execute.
type outputWriter struct {
	io.Writer
	err error
}

func (writer *outputWriter) Write(data []byte) (int, error) {
	if writer.err != nil {
		return 0, writer.err
	}
	n, err := writer.Writer.Write(data)
	if err == nil && n < len(data) {
		err = io.ErrShortWrite
	}
	writer.err = err
	return n, err
}

// rawCommandOutput exposes the underlying stream so terminal detection sees the
// real file descriptor and exec/log output bypasses the CLI's error-tracking wrapper.
func rawCommandOutput(command *cobra.Command) io.Writer {
	output := command.OutOrStdout()
	if wrapper, ok := output.(*outputWriter); ok {
		return wrapper.Writer
	}
	return output
}

func isTerminal(output io.Writer) bool {
	_, terminal := term.GetFdInfo(output)
	return terminal
}
