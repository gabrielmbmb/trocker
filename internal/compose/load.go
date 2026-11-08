// Package compose integrates the official Compose SDK with Trocker configuration.
package compose

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"

	composecli "github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/errdefs"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/tree"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/compose/v5/pkg/api"
	sdk "github.com/docker/compose/v5/pkg/compose"

	"github.com/gabrielmbmb/trocker/internal/config"
)

// Load validates a manifest and its active Compose project without initializing
// a Docker client. Profiles from the manifest are used unless the caller passes
// an explicit nonempty selection. Workspace project naming is applied at the
// mutation boundary.
func Load(ctx context.Context, path string, profiles []string) (*config.Manifest, *types.Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	manifest, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	selection, explicit := selectProfiles(manifest, profiles)
	project, err := loadProject(ctx, manifest.Directory, manifest.Compose, selection)
	if err != nil {
		return nil, nil, err
	}
	if err := validateLoadedProject(manifest, project, selection, explicit); err != nil {
		return nil, nil, err
	}
	return manifest, project, nil
}

// DiscoverFiles delegates default file selection to the SDK. It does not infer
// endpoint schemes or application bindings.
func DiscoverFiles(ctx context.Context, directory string) (*types.Project, error) {
	return loadProject(ctx, directory, nil, nil)
}

func loadProject(ctx context.Context, directory string, paths, profiles []string) (*types.Project, error) {
	return loadWithOptions(ctx, api.ProjectLoadOptions{ConfigPaths: paths, WorkingDir: directory, Profiles: profiles, Offline: true})
}

// selectProfiles applies Trocker's precedence: an explicit CLI selection wins,
// then the manifest, and only otherwise does the SDK fall back to
// COMPOSE_PROFILES from the environment or .env.
func selectProfiles(manifest *config.Manifest, override []string) ([]string, bool) {
	if len(override) > 0 {
		return override, true
	}
	if len(manifest.Profiles) > 0 {
		return manifest.Profiles, true
	}
	return nil, false
}

// LoadForPlan uses the SDK's pre-normalization checks: explicit resource names
// cannot be distinguished from generated names after normalization.
func LoadForPlan(ctx context.Context, path string, profiles []string) (*config.Manifest, *types.Project, error) {
	manifest, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	selection, explicit := selectProfiles(manifest, profiles)
	var unsupported []loader.UnsupportedAttribute
	var patterns []loader.UnsupportedAttributePattern
	for _, path := range []tree.Path{
		"networks.*.name", "volumes.*.name", "secrets", "configs", "models", "jobs",
		"services.*.privileged", "services.*.device_cgroup_rules", "services.*.uts", "services.*.cgroup", "services.*.volumes_from", "services.*.external_links", "services.*.provider", "services.*.models", "services.*.pre_start", "services.*.post_start", "services.*.pre_stop", "services.*.develop", "services.*.deploy", "services.*.secrets", "services.*.configs",
		"services.*.build.tags", "services.*.build.network", "services.*.build.privileged", "services.*.build.entitlements", "services.*.build.ssh", "services.*.build.secrets",
	} {
		patterns = append(patterns, loader.UnsupportedAttributePattern{Path: path})
	}
	project, err := loadWithOptions(ctx, api.ProjectLoadOptions{ConfigPaths: manifest.Compose, WorkingDir: manifest.Directory, Profiles: selection, Offline: true, ProjectOptionsFns: []composecli.ProjectOptionsFn{composecli.WithLoadOptions(loader.WithUnsupportedAttributesCheck(patterns, func(found []loader.UnsupportedAttribute) { unsupported = append(unsupported, found...) }))}})
	if err != nil {
		return nil, nil, err
	}
	if len(unsupported) > 0 {
		return nil, nil, &config.Error{Field: unsupported[0].Path.String(), Problem: "not supported by the initial isolation policy"}
	}
	if err := validateLoadedProject(manifest, project, selection, explicit); err != nil {
		return nil, nil, err
	}
	return manifest, project, nil
}

func validateLoadedProject(manifest *config.Manifest, project *types.Project, profiles []string, explicit bool) error {
	if explicit {
		if err := validateSelectedProfiles(project, profiles); err != nil {
			return err
		}
	}
	return manifest.ValidateProject(project)
}

// validateSelectedProfiles catches a typo in an explicit selection even when
// no service happens to use the profile. Environment-only selection keeps its
// historical permissive behavior.
func validateSelectedProfiles(project *types.Project, profiles []string) error {
	if slices.Contains(profiles, "*") {
		return nil
	}
	available := make(map[string]bool)
	for _, service := range project.AllServices() {
		for _, profile := range service.Profiles {
			available[profile] = true
		}
	}
	for _, job := range project.AllJobs() {
		for _, profile := range job.Profiles {
			available[profile] = true
		}
	}
	for _, profile := range profiles {
		if profile == "" {
			return &profileError{profile: profile}
		}
		if !available[profile] {
			names := make([]string, 0, len(available))
			for name := range available {
				names = append(names, name)
			}
			slices.Sort(names)
			return &profileError{profile: profile, available: names}
		}
	}
	return nil
}

type profileError struct {
	profile   string
	available []string
}

func (e *profileError) Error() string {
	if e.profile == "" {
		return "explicit profile selection contains an empty name"
	}
	return "profile " + e.profile + " is not declared in the Compose project"
}

func (e *profileError) Help() string {
	if e.profile == "" {
		return "remove the empty profile value or select a profile name"
	}
	if len(e.available) == 0 {
		return "declare the profile on a Compose service, or remove it from the explicit selection"
	}
	return "declare it on a Compose service, or choose one of: " + strings.Join(e.available, ", ")
}

func (e *profileError) Note() string {
	return "an explicit profile selection replaces COMPOSE_PROFILES"
}

func loadWithOptions(ctx context.Context, options api.ProjectLoadOptions) (*types.Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dockerCLI, err := command.NewDockerCli(command.WithOutputStream(io.Discard), command.WithErrorStream(io.Discard), command.WithInputStream(io.NopCloser(strings.NewReader(""))))
	if err != nil {
		return nil, &loadError{cause: err}
	}
	// Offline loading needs no Initialize call or remote Git/OCI loaders.
	service, err := sdk.NewComposeService(dockerCLI)
	if err != nil {
		return nil, &loadError{cause: err}
	}
	project, err := service.LoadProject(ctx, options)
	if err != nil {
		return nil, &loadError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return project, nil
}

// SDK validation errors may contain interpolated secrets. Preserve the cause
// for classification, but never include its raw message in a user diagnostic.
type loadError struct{ cause error }

func (e *loadError) Error() string {
	switch {
	case errors.Is(e.cause, errdefs.ErrNotFound):
		return "no Compose file found"
	case errors.Is(e.cause, os.ErrNotExist):
		return "a referenced Compose file does not exist"
	case errors.Is(e.cause, os.ErrPermission):
		return "permission denied reading Compose configuration"
	default:
		return "cannot load Compose configuration"
	}
}

func (e *loadError) Help() string {
	switch {
	case errors.Is(e.cause, errdefs.ErrNotFound):
		return "add compose.yaml to your checkout, then run trocker init"
	case errors.Is(e.cause, os.ErrNotExist):
		return "check Compose file paths, includes, and env_file entries"
	case errors.Is(e.cause, os.ErrPermission):
		return "check access to Compose files and their referenced files"
	default:
		return "check Compose syntax, interpolation inputs, and referenced files"
	}
}

func (e *loadError) Note() string {
	if errors.Is(e.cause, errdefs.ErrNotFound) {
		return "trocker init can generate trocker.yaml"
	}
	return ""
}

func (e *loadError) Unwrap() error { return e.cause }
