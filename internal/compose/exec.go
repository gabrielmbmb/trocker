package compose

import (
	"context"
	"errors"
	"io"

	"github.com/docker/cli/cli"
	"github.com/docker/compose/v5/pkg/api"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

type ExecOptions struct {
	Service     string
	Command     []string
	Environment []string
	WorkingDir  string
	User        string
	Index       int
	Privileged  bool
	Detach      bool
	Interactive bool
	Tty         bool
}

type ExecStreams struct {
	In       io.Reader
	Out, Err io.Writer
}

// Exec targets a running service container in the supplied ownership-validated
// snapshot. A non-zero child status is returned separately from execution
// failures so callers can propagate it without a Trocker diagnostic.
func (t *Target) Exec(ctx context.Context, registration workspace.Registration, containers []api.ContainerSummary, options ExecOptions, streams ExecStreams) (int, error) {
	if err := requireService(containers, options.Service, true); err != nil {
		return 0, err
	}
	if err := requireRunningService(containers, options.Service, options.Index); err != nil {
		return 0, err
	}
	service, err := t.serviceWithStreams(streams.In, streams.Out, streams.Err)
	if err != nil {
		return 0, &execError{cause: err}
	}
	_, err = service.Exec(ctx, registration.ProjectName(), api.RunOptions{
		Service:     options.Service,
		Command:     options.Command,
		Environment: options.Environment,
		WorkingDir:  options.WorkingDir,
		User:        options.User,
		Index:       options.Index,
		Privileged:  options.Privileged,
		Detach:      options.Detach,
		Interactive: options.Interactive,
		Tty:         options.Tty,
	})
	// Compose reports the child status as a StatusError. A negative status means
	// the daemon connection failed rather than the command exiting.
	var status cli.StatusError
	if errors.As(err, &status) && status.StatusCode >= 0 {
		return status.StatusCode, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, &execError{cause: err}
	}
	return 0, nil
}

type execError struct{ cause error }

func (*execError) Error() string { return "cannot execute a command in the workspace container" }
func (*execError) Help() string {
	return "check that the service is running and the Docker target is reachable"
}
func (e *execError) Unwrap() error { return e.cause }
