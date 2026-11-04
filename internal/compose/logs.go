package compose

import (
	"context"
	"io"

	"github.com/docker/compose/v5/pkg/api"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

type LogOptions struct {
	Services   []string
	Follow     bool
	Tail       string
	Since      string
	Until      string
	Timestamps bool
}

// Logs reads logs from the supplied ownership-validated containers. Requested
// services are resolved by Compose service name, never by container name.
func (t *Target) Logs(ctx context.Context, registration workspace.Registration, containers []api.ContainerSummary, options LogOptions, output io.Writer) error {
	for _, service := range options.Services {
		if err := requireService(containers, service, false); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := &progressOutput{out: output, cancel: cancel}
	service, err := t.service()
	if err != nil {
		return &logError{cause: err}
	}
	consumer := composeLogs(ctx, stream, interactiveProgress(output))
	err = service.Logs(ctx, registration.ProjectName(), consumer, api.LogOptions{
		Services:   options.Services,
		Follow:     options.Follow,
		Tail:       options.Tail,
		Since:      options.Since,
		Until:      options.Until,
		Timestamps: options.Timestamps,
	})
	if streamErr := stream.Err(); streamErr != nil {
		return streamErr
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &logError{cause: err}
	}
	return nil
}

type logError struct{ cause error }

func (*logError) Error() string { return "cannot read workspace container logs" }
func (*logError) Help() string {
	return "check that the selected services have containers and the Docker target is reachable"
}
func (e *logError) Unwrap() error { return e.cause }
