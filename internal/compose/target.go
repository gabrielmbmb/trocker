package compose

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/docker/cli/cli/command"
	dockerconfig "github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/flags"
	"github.com/moby/moby/client"
	"github.com/spf13/pflag"
)

// Target owns the single SDK client selected through Docker configuration.
// Context aliases are not identities: ID comes from the daemon's Info response.
type Target struct {
	ID     string
	Host   string
	Client client.APIClient
}

func ResolveTarget(ctx context.Context) (*Target, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options := flags.NewClientOptions()
	flagSet := pflag.NewFlagSet("docker", pflag.ContinueOnError)
	options.InstallFlags(flagSet)
	options.SetDefaultOptions(flagSet)
	cfg, err := dockerconfig.Load(options.ConfigDir)
	if err != nil {
		return nil, &targetError{err}
	}
	// This SDK entry point returns errors rather than DockerCli.Client's os.Exit.
	apiClient, err := command.NewAPIClientFromFlags(options, cfg)
	if err != nil {
		return nil, &targetError{err}
	}
	host := apiClient.DaemonHost()
	localPipe := strings.HasPrefix(strings.ToLower(host), "npipe:////./pipe/") || strings.HasPrefix(strings.ToLower(host), "npipe:////localhost/pipe/")
	if !strings.HasPrefix(host, "unix://") && !localPipe {
		return nil, errors.Join(fmt.Errorf("only local Unix-socket or named-pipe Docker targets are supported"), apiClient.Close())
	}
	info, err := apiClient.Info(ctx, client.InfoOptions{})
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, errors.Join(&targetError{err}, apiClient.Close())
	}
	if info.Info.ID == "" || info.Info.OSType != "linux" {
		return nil, errors.Join(fmt.Errorf("docker target must identify itself and run Linux containers"), apiClient.Close())
	}
	return &Target{ID: info.Info.ID, Host: host, Client: apiClient}, nil
}

func (t *Target) Close() error { return t.Client.Close() }

type targetError struct{ cause error }

func (e *targetError) Error() string {
	return "cannot resolve or reach the selected Docker target"
}

func (e *targetError) Help() string {
	return "check that Docker is running and verify your Docker context/configuration"
}
func (e *targetError) Note() string  { return "no fallback to another Docker target was attempted" }
func (e *targetError) Unwrap() error { return e.cause }
