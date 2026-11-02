//go:build linux || darwin

// Package native executes an owned foreground process, not a supervisor.
package native

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"time"
)

func Run(ctx context.Context, options Options) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(options.Args) == 0 || options.Args[0] == "" {
		return fmt.Errorf("native command is empty")
	}
	command := exec.Command(options.Args[0], options.Args[1:]...) //nolint:gosec // Explicit user-selected executable and argv; no shell interpretation.
	command.Dir = options.Directory
	command.Stdin, command.Stdout, command.Stderr = options.Streams.In, options.Streams.Out, options.Streams.Err
	command.Env = command.Environ()
	keys := make([]string, 0, len(options.Environment))
	for key := range options.Environment {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		command.Env = append(command.Env, key+"="+options.Environment[key])
	}
	// Escaped descendants must not keep inherited output pipes open indefinitely.
	command.WaitDelay = time.Second
	process, err := prepareProcess(command)
	if err != nil {
		return err
	}
	defer func() {
		if restoreErr := process.restore(); restoreErr != nil {
			err = restoreErr
		}
	}()
	if err := command.Start(); err != nil {
		return &launchError{cause: err}
	}
	done := make(chan error, 1)
	go func() { done <- process.wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		if err := process.signal(ctx); err != nil {
			return errors.Join(err, finishProcess(process, done))
		}
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case waitErr = <-done:
		case <-timer.C:
			if err := process.kill(); err != nil {
				return err
			}
			waitErr = <-done
		}
		timer.Stop()
	}
	if exited, ok := errors.AsType[*exec.ExitError](waitErr); ok {
		err = &ExitError{Code: processExitCode(exited)}
	} else if waitErr != nil {
		err = &executionError{cause: waitErr}
	}
	if ctx.Err() != nil {
		if err == nil {
			if cause, ok := errors.AsType[*signalCause](context.Cause(ctx)); ok {
				err = &ExitError{Code: signalExitCode(cause.signal)}
			}
		}
		err = errors.Join(err, ctx.Err())
	}
	return err
}

func finishProcess(process *process, done <-chan error) error {
	if err := process.kill(); err != nil {
		return err
	}
	return <-done
}
