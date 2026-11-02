//go:build linux || darwin

package native_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/gabrielmbmb/trocker/internal/native"
)

type report struct {
	Args                                            []string
	Input, Directory, Inherited, Override, Identity string
	Address                                         string
}

func helperOptions(t *testing.T, mode string) native.Options {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return native.Options{Args: []string{executable, "-test.run=^TestNativeHelper$", "--"}, Directory: t.TempDir(), Environment: map[string]string{"TROCKER_TEST_NATIVE": mode}, Streams: native.Streams{In: bytes.NewReader(nil), Out: io.Discard, Err: io.Discard}}
}

func TestNativeStreamsArgumentsEnvironmentAndExit(t *testing.T) {
	t.Setenv("TROCKER_NATIVE_INHERITED", "inherited")
	t.Setenv("TROCKER_NATIVE_OVERRIDE", "parent")
	for _, code := range []int{0, 2, 37} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			options := helperOptions(t, "echo")
			arguments := []string{"a b", "", "--help", "--unknown", "--", "$HOME", "*"}
			options.Args = append(options.Args, arguments...)
			options.Environment["TROCKER_NATIVE_OVERRIDE"] = "binding"
			options.Environment["TROCKER_WORKSPACE_ID"] = "workspace"
			options.Environment["TROCKER_NATIVE_EXIT"] = strconv.Itoa(code)
			var output, diagnostics bytes.Buffer
			options.Streams = native.Streams{In: bytes.NewBufferString("input\n"), Out: &output, Err: &diagnostics}
			err := native.Run(t.Context(), options)
			var child *native.ExitError
			if code == 0 && err != nil {
				var wrapped interface{ Unwrap() error }
				if errors.As(err, &wrapped) {
					t.Fatalf("%v; detail: %v", err, wrapped.Unwrap())
				}
				t.Fatal(err)
			}
			if code != 0 && (!errors.As(err, &child) || child.Code != code) {
				t.Fatalf("exit: %v", err)
			}
			var received report
			if err := json.Unmarshal(output.Bytes(), &received); err != nil {
				t.Fatalf("output %q: %v", &output, err)
			}
			directory, err := filepath.EvalSymlinks(received.Directory)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(received.Args, arguments) || received.Input != "input\n" || directory != mustCanonicalDirectory(t, options.Directory) || received.Inherited != "inherited" || received.Override != "binding" || received.Identity != "workspace" {
				t.Fatalf("child inputs changed: %+v", received)
			}
			if diagnostics.String() != "child stderr\n" {
				t.Fatalf("stderr: %q", &diagnostics)
			}
		})
	}
}

func TestNativeLaunchFailuresAndPreCancellation(t *testing.T) {
	options := helperOptions(t, "echo")
	options.Args = []string{filepath.Join(t.TempDir(), "secret-missing")}
	err := native.Run(t.Context(), options)
	var child *native.ExitError
	if err == nil || errors.As(err, &child) || bytes.Contains([]byte(err.Error()), []byte("secret")) {
		t.Fatal("launch error exposed command or became a child exit", err)
	}
	path := filepath.Join(t.TempDir(), "nonexecutable")
	if err := os.WriteFile(path, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	options.Args = []string{path}
	if err := native.Run(t.Context(), options); !errors.Is(err, os.ErrPermission) {
		t.Fatal("permission failure", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := native.Run(ctx, helperOptions(t, "echo")); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-cancellation", err)
	}
}

func TestNativeProcessGroupCleanup(t *testing.T) {
	for _, mode := range []string{"tree", "tree-exit", "ignore"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			running, interrupt := context.WithCancel(ctx)
			defer interrupt()
			options := helperOptions(t, mode)
			reader, writer := io.Pipe()
			defer func() {
				if err := reader.Close(); err != nil {
					t.Error(err)
				}
			}()
			options.Streams.Out = writer
			done := make(chan error, 1)
			go func() { err := native.Run(running, options); done <- errors.Join(err, writer.Close()) }()
			var ready report
			if err := json.NewDecoder(reader).Decode(&ready); err != nil {
				t.Fatal("child readiness", err)
			}
			var listen net.ListenConfig
			unrelated, err := listen.Listen(ctx, "tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := unrelated.Close(); err != nil {
					t.Error(err)
				}
			}()
			if mode != "tree-exit" {
				interrupt()
			}
			if err := <-done; mode == "tree-exit" && err != nil || mode != "tree-exit" && !errors.Is(err, context.Canceled) {
				t.Fatal("process cleanup", err)
			}
			// SIGKILL delivery and descriptor release may follow leader reaping briefly.
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				listener, err := listen.Listen(ctx, "tcp4", ready.Address)
				if err == nil {
					if err := listener.Close(); err != nil {
						t.Fatal(err)
					}
					break
				}
				select {
				case <-deadline.C:
					t.Fatal("owned descendant retained its listener")
				case <-ticker.C:
				}
			}
			connection, err := (&net.Dialer{}).DialContext(ctx, "tcp4", unrelated.Addr().String())
			if err != nil {
				t.Fatal("unrelated listener lost", err)
			}
			if err := connection.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func mustCanonicalDirectory(t *testing.T, path string) string {
	t.Helper()
	result, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func helperTermination() <-chan os.Signal {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return signals
}
func ignoreTermination() { signal.Ignore(os.Interrupt, syscall.SIGTERM) }

func TestNativeHelper(t *testing.T) {
	mode := os.Getenv("TROCKER_TEST_NATIVE")
	if mode == "" {
		return
	}
	switch mode {
	case "echo":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(90)
		}
		directory, err := os.Getwd()
		if err != nil {
			os.Exit(91)
		}
		index := slices.Index(os.Args, "--")
		value := report{Args: os.Args[index+1:], Input: string(input), Directory: directory, Inherited: os.Getenv("TROCKER_NATIVE_INHERITED"), Override: os.Getenv("TROCKER_NATIVE_OVERRIDE"), Identity: os.Getenv("TROCKER_WORKSPACE_ID")}
		if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
			os.Exit(92)
		}
		if _, err := io.WriteString(os.Stderr, "child stderr\n"); err != nil {
			os.Exit(93)
		}
		code, err := strconv.Atoi(os.Getenv("TROCKER_NATIVE_EXIT"))
		if err != nil {
			code = 0
		}
		os.Exit(code)
	case "tree", "tree-exit":
		waitForTermination := helperTermination()
		executable, err := os.Executable()
		if err != nil {
			os.Exit(94)
		}
		child := exec.Command(executable, "-test.run=^TestNativeHelper$") //nolint:gosec // Executes this test binary.
		child.Env = append(os.Environ(), "TROCKER_TEST_NATIVE=descendant")
		child.Stderr = os.Stderr
		if mode == "tree-exit" {
			output, err := child.StdoutPipe()
			if err != nil {
				os.Exit(99)
			}
			if err := child.Start(); err != nil {
				os.Exit(95)
			}
			var ready report
			if err := json.NewDecoder(output).Decode(&ready); err != nil {
				os.Exit(99)
			}
			if err := json.NewEncoder(os.Stdout).Encode(ready); err != nil {
				os.Exit(99)
			}
			os.Exit(0)
		}
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			os.Exit(95)
		}
		<-waitForTermination
		os.Exit(0)
	case "descendant", "ignore":
		ignoreTermination()
		var listen net.ListenConfig
		listener, err := listen.Listen(t.Context(), "tcp4", "127.0.0.1:0")
		if err != nil {
			os.Exit(96)
		}
		if err := json.NewEncoder(os.Stdout).Encode(report{Address: listener.Addr().String()}); err != nil {
			os.Exit(97)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(98)
	}
}
