package compose

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/compose/v5/pkg/api"
	sdk "github.com/docker/compose/v5/pkg/compose"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

type LifecycleOperation string

const (
	Start LifecycleOperation = "start"
	Stop  LifecycleOperation = "stop"
	Down  LifecycleOperation = "down"
)

type LifecycleOptions struct {
	Operation LifecycleOperation
	Services  []string
	Volumes   bool
}

func (o LifecycleOptions) Validate() error {
	switch o.Operation {
	case Start, Stop:
		if o.Volumes {
			return fmt.Errorf("volume removal is only supported by down")
		}
	case Down:
		if len(o.Services) > 0 {
			return fmt.Errorf("down does not accept service selection")
		}
	default:
		return fmt.Errorf("unsupported lifecycle operation")
	}
	return nil
}

// ApplyLifecycle uses the SDK's live-project reconstruction, not current files
// or startup transformations. Start can only start existing containers.
func (t *Target) ApplyLifecycle(ctx context.Context, registration workspace.Registration, options LifecycleOptions, output io.Writer) error {
	if err := options.Validate(); err != nil {
		return err
	}
	if err := t.checkLifecycleOwnership(ctx, registration, options); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := &progressOutput{out: output, cancel: cancel}
	service, err := t.service(sdk.WithEventProcessor(composeProgress(stream, interactiveProgress(output), true)))
	if err != nil {
		return &lifecycleError{operation: options.Operation, cause: err}
	}
	project := registration.ProjectName()
	switch options.Operation {
	case Start:
		err = service.Start(ctx, project, api.StartOptions{AttachTo: options.Services})
	case Stop:
		err = service.Stop(ctx, project, api.StopOptions{Services: options.Services})
	case Down:
		err = service.Down(ctx, project, api.DownOptions{Volumes: options.Volumes, RemoveOrphans: true})
	}
	if err := stream.Err(); err != nil {
		return err
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &lifecycleError{operation: options.Operation, cause: err}
	}
	return nil
}

type lifecycleError struct {
	operation LifecycleOperation
	cause     error
}

func (e *lifecycleError) Error() string { return fmt.Sprintf("workspace %s failed", e.operation) }
func (e *lifecycleError) Help() string {
	if e.operation == Start {
		return "check existing containers and port conflicts; use trocker up to apply configuration changes"
	}
	return "inspect workspace status and resources, correct the problem, then retry"
}

func (e *lifecycleError) Note() string {
	return "registration is preserved; completed resource changes are not rolled back"
}
func (e *lifecycleError) Unwrap() error { return e.cause }
