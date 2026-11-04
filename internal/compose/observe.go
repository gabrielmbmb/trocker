package compose

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	sdk "github.com/docker/compose/v5/pkg/compose"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

const WorkspaceLabel = "io.trocker.workspace-id"

// service keeps SDK operations on the already resolved daemon. Callers opt in
// to progress rendering; read-only queries remain silent.
func (t *Target) service(options ...sdk.Option) (api.Compose, error) {
	return t.serviceWithStreams(strings.NewReader(""), io.Discard, io.Discard, options...)
}

func (t *Target) serviceWithStreams(input io.Reader, output, diagnostics io.Writer, options ...sdk.Option) (api.Compose, error) {
	cli, err := command.NewDockerCli(command.WithAPIClient(t.Client), command.WithInputStream(readCloser(input)), command.WithOutputStream(output), command.WithErrorStream(diagnostics))
	if err != nil {
		return nil, &observationError{err}
	}
	// DockerCli still resolves endpoint metadata with an injected client. Pin it
	// to the already validated host instead of reselecting the ambient context.
	if err := cli.Initialize(&flags.ClientOptions{Hosts: []string{t.Host}, LogLevel: "error"}); err != nil {
		return nil, &observationError{err}
	}
	options = append(options, sdk.WithPrompt(func(string, bool) (bool, error) {
		return false, fmt.Errorf("compose requested a destructive or interactive change; automatic approval is disabled")
	}))
	return sdk.NewComposeService(cli, options...)
}

// readCloser preserves an existing file descriptor so the Docker CLI can detect
// a terminal and set raw mode for interactive exec.
func readCloser(input io.Reader) io.ReadCloser {
	if input == nil {
		return io.NopCloser(strings.NewReader(""))
	}
	if closer, ok := input.(io.ReadCloser); ok {
		return closer
	}
	return io.NopCloser(input)
}

// Containers lists the workspace's containers with the SDK's Ps filters and
// verifies that every returned container is owned by the registration.
func (t *Target) Containers(ctx context.Context, registration workspace.Registration, options api.PsOptions) ([]api.ContainerSummary, error) {
	service, err := t.service()
	if err != nil {
		return nil, &observationError{err}
	}
	containers, err := service.Ps(ctx, registration.ProjectName(), options)
	if err != nil {
		return nil, &observationError{err}
	}
	if err := ValidateOwnership(containers, registration); err != nil {
		return nil, err
	}
	return containers, nil
}

func ValidateOwnership(containers []api.ContainerSummary, registration workspace.Registration) error {
	for _, ctr := range containers {
		if ctr.Project != registration.ProjectName() || ctr.Labels[WorkspaceLabel] != registration.ID {
			return &ownershipError{kind: "container", name: ctr.ID}
		}
	}
	return nil
}

type ownershipError struct{ kind, name string }

func (e *ownershipError) Error() string {
	return fmt.Sprintf("%s %s has missing or conflicting workspace ownership", e.kind, e.name)
}

func (e *ownershipError) Help() string {
	return "inspect its Compose and workspace labels; automatic adoption is not supported"
}

type observationError struct{ cause error }

func (e *observationError) Error() string { return "cannot inspect workspace resources" }
func (e *observationError) Help() string {
	return "check that the selected Docker daemon is reachable, then retry"
}
func (e *observationError) Unwrap() error { return e.cause }
