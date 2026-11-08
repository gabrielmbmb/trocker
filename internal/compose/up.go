package compose

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/containerd/errdefs"
	"github.com/docker/compose/v5/pkg/api"
	sdk "github.com/docker/compose/v5/pkg/compose"
)

type UpOptions struct {
	Services []string
	Profiles []string
	Detach   bool
	// Build rebuilds every built image; NoBuild fails instead of building a
	// missing one. Neither builds only the images that are missing.
	Build   bool
	NoBuild bool
}

func (t *Target) Up(ctx context.Context, project *types.Project, options UpOptions, output io.Writer) error {
	project, err := withBuildPolicy(project, options.Build)
	if err != nil {
		return err
	}
	if options.NoBuild {
		if err := t.requireBuiltImages(ctx, project); err != nil {
			return err
		}
	}
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
	if !options.NoBuild {
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

// withBuildPolicy stops Compose from pulling workspace build tags, which exist
// only on this daemon: a pull always fails and only delays the build. Compose
// then builds a missing image, and with rebuild it builds every image again.
// An explicit pull_policy: build is kept, since it already means "never pull".
func withBuildPolicy(project *types.Project, rebuild bool) (*types.Project, error) {
	policy := types.PullPolicyNever
	if rebuild {
		policy = types.PullPolicyBuild
	}
	return project.WithServicesTransform(func(_ string, service types.ServiceConfig) (types.ServiceConfig, error) {
		if service.Build != nil && (rebuild || service.PullPolicy != types.PullPolicyBuild) {
			service.PullPolicy = policy
		}
		return service, nil
	})
}

func (t *Target) requireBuiltImages(ctx context.Context, project *types.Project) error {
	for _, name := range project.ServiceNames() {
		service := project.Services[name]
		if service.Build == nil {
			continue
		}
		_, err := t.Client.ImageInspect(ctx, service.Image)
		if errdefs.IsNotFound(err) {
			return &unbuiltImageError{service: name}
		}
		if err != nil {
			return &observationError{err}
		}
	}
	return nil
}

type unbuiltImageError struct{ service string }

func (e *unbuiltImageError) Error() string {
	return fmt.Sprintf("service %s has no image built for this workspace", e.service)
}

func (*unbuiltImageError) Help() string { return "run trocker up without --no-build" }

type startupError struct{ cause error }

func (e *startupError) Error() string { return "workspace startup failed" }
func (e *startupError) Help() string {
	return "check image/build configuration and service health, then retry trocker up"
}

func (e *startupError) Note() string {
	return "partial resources and volumes were preserved; no rollback was attempted"
}
func (e *startupError) Unwrap() error { return e.cause }
