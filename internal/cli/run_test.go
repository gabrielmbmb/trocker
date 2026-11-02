package cli_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/cli"
)

func TestInformationalCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no arguments", want: "Usage:"},
		{name: "help command", args: []string{"help"}, want: "Usage:"},
		{name: "help flag", args: []string{"--help"}, want: "Usage:"},
		{name: "short help flag", args: []string{"-h"}, want: "Usage:"},
		{name: "version command", args: []string{"version"}, want: "trocker 0.7.2\n"},
		{name: "version flag", args: []string{"--version"}, want: "trocker 0.7.2\n"},
		{name: "command help", args: []string{"help", "down"}, want: "--volumes"},
		{name: "command help flag", args: []string{"down", "--help"}, want: "--volumes"},
		{name: "native help", args: []string{"run", "--help"}, want: "SERVICE -- COMMAND"},
		{name: "shell completion", args: []string{"completion", "bash"}, want: "__start_trocker"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			if err := cli.Run(test.args, "0.7.2", &output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Errorf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}

func TestInvalidArgumentsDoNotProduceSuccessOutput(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"unknown"},
		{"--unknown"},
		{"version", "unexpected"},
		{"help", "unexpected"},
		{"help", "completion", "unexpected"},
		{"completion", "bash", "unexpected"},
		{"down", "--unknown"},
		{"down", "api"},
		{"stop", "--volumes"},
		{"start", "--unknown"},
		{"plan", "unexpected"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			err := cli.Run(args, "dev", &output)
			if !errors.Is(err, cli.ErrUsage) {
				t.Fatalf("error = %v, want usage error", err)
			}
			if !strings.Contains(err.Error(), args[len(args)-1]) {
				t.Errorf("error %q does not identify the invalid argument", err)
			}
			if output.Len() != 0 {
				t.Errorf("invalid invocation wrote success output: %q", output.String())
			}
		})
	}
}

func TestNativeArgumentsPassValidation(t *testing.T) {
	t.Chdir(t.TempDir())

	for _, args := range [][]string{
		{"run", "web", "--", "pnpm", "dev"},
		{"run", "web", "--", "child", "--help", "--unknown", "--", "value"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			err := cli.Run(args, "dev", &output)
			if err == nil {
				t.Fatal("unregistered invocation unexpectedly succeeded")
			}
			if errors.Is(err, cli.ErrUsage) {
				t.Fatalf("valid invocation was rejected as invalid arguments: %v", err)
			}
			if output.Len() != 0 {
				t.Errorf("failed invocation wrote success output: %q", output.String())
			}
		})
	}
}

func TestNativeCommandRequiresServiceAndSeparatedChildCommand(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"run"},
		{"run", "web"},
		{"run", "web", "--"},
		{"run", "web", "pnpm", "dev"},
		{"run", "web", "pnpm", "--", "dev"},
		{"run", "--", "web", "pnpm"},
		{"run", "", "--", "pnpm"},
		{"run", "web", "--", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			err := cli.Run(args, "dev", &output)
			if !errors.Is(err, cli.ErrUsage) || !strings.Contains(err.Error(), "SERVICE -- COMMAND") {
				t.Fatalf("error = %v, want native-command usage error", err)
			}
			if output.Len() != 0 {
				t.Errorf("invalid invocation wrote success output: %q", output.String())
			}
		})
	}
}

func TestOutputFailureIsNotReportedAsSuccessOrUsageError(t *testing.T) {
	t.Parallel()

	writeErr := errors.New("output unavailable")
	for _, args := range [][]string{nil, {"--help"}, {"help", "down"}, {"version"}, {"--version"}, {"completion", "bash"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			err := cli.Run(args, "dev", failingWriter{err: writeErr})
			if !errors.Is(err, writeErr) {
				t.Fatalf("error = %v, want wrapped output error", err)
			}
			if errors.Is(err, cli.ErrUsage) {
				t.Fatal("output failure was classified as invalid arguments")
			}
		})
	}
}

func TestInitRequiresInteractiveConfirmation(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	err := cli.Run([]string{"init"}, "dev", &output)
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") || errors.Is(err, cli.ErrUsage) {
		t.Fatalf("unexpected init error: %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("noninteractive init reported success")
	}
}

func TestShortOutputWriteIsReportedAsFailure(t *testing.T) {
	t.Parallel()

	if err := cli.Run([]string{"--help"}, "dev", failingWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want short write", err)
	}
}

type failingWriter struct {
	err error
}

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

func BenchmarkInformationalCommands(b *testing.B) {
	for _, command := range []string{"help", "version"} {
		b.Run(command, func(b *testing.B) {
			args := []string{command}
			for b.Loop() {
				if err := cli.Run(args, "dev", io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
