package compose

import (
	"context"
	"io"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	sdk "github.com/docker/compose/v5/pkg/compose"
)

type UpOptions struct {
	Services []string
	Profiles []string
	Detach   bool
	Build    bool
}

func (t *Target) Up(ctx context.Context, project *types.Project, options UpOptions, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	interactive := interactiveProgress(output)
	stream := &progressOutput{out: output, cancel: cancel}
	service, err := t.service(sdk.WithEventProcessor(composeProgress(stream, interactive, options.Detach)))
	if err != nil {
		return &startupError{cause: err}
	}
	logs := composeLogs(ctx, stream, interactive)
	start := api.StartOptions{Project: project, Services: options.Services, Wait: options.Detach, WaitTimeout: 2 * time.Minute}
	if !options.Detach {
		start.Attach = logs
	}
	create := api.CreateOptions{Services: options.Services, IgnoreOrphans: true, Inherit: true, Recreate: api.RecreateDiverged, RecreateDependencies: api.RecreateDiverged}
	if options.Build {
		create.Build = &api.BuildOptions{Quiet: true, Out: io.Discard}
	}
	err = service.Up(ctx, project, api.UpOptions{Create: create, Start: start})
	if err := stream.Err(); err != nil {
		return err
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &startupError{cause: err}
	}
	return nil
}

type startupError struct{ cause error }

func (e *startupError) Error() string { return "workspace startup failed" }
func (e *startupError) Help() string {
	return "check image/build configuration and service health, then retry trocker up"
}

func (e *startupError) Note() string {
	return "partial resources and volumes were preserved; no rollback was attempted"
}
func (e *startupError) Unwrap() error { return e.cause }
