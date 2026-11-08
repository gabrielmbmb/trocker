package lifecycle

import (
	"context"
	"os"
	"path/filepath"

	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

// EnvironmentMarker tells a Trocker process that the environment wrapper
// started it. Its value is the wrapper's path, so a Trocker command for another
// checkout, started from inside this wrapper, still enters its own wrapper.
const EnvironmentMarker = "TROCKER_ENVIRONMENT_WRAPPER"

// EnvironmentWrapper is a checkout's environment_wrapper: a script that adds
// environment, such as secrets, and then runs Trocker.
type EnvironmentWrapper struct {
	// Command is the command as declared, relative to the manifest.
	Command []string
	Path    string
}

// Active reports whether this process was started by this wrapper.
func (w *EnvironmentWrapper) Active() bool {
	return w != nil && os.Getenv(EnvironmentMarker) == w.Path
}

// LoadEnvironmentWrapper returns the wrapper the checkout's manifest declares,
// or nil when there is none. A directory without a checkout or a valid manifest
// also returns nil: the command that runs next reports that problem itself.
func LoadEnvironmentWrapper(ctx context.Context, directory string) (*EnvironmentWrapper, error) {
	checkout, err := workspace.Discover(ctx, directory)
	if err != nil {
		return nil, nil //nolint:nilerr // The command reports a missing checkout.
	}
	manifest, err := config.Load(filepath.Join(checkout.Root, "trocker.yaml"))
	if err != nil {
		return nil, nil //nolint:nilerr // The command reports an invalid manifest.
	}
	return resolveEnvironmentWrapper(manifest)
}

func resolveEnvironmentWrapper(manifest *config.Manifest) (*EnvironmentWrapper, error) {
	if len(manifest.EnvironmentWrapper) == 0 {
		return nil, nil
	}
	path := manifest.EnvironmentWrapperPath()
	if _, err := os.Stat(path); err != nil {
		return nil, &wrapperScriptError{script: manifest.EnvironmentWrapper[0], cause: err}
	}
	return &EnvironmentWrapper{Command: manifest.EnvironmentWrapper, Path: path}, nil
}

type wrapperScriptError struct {
	script string
	cause  error
}

func (e *wrapperScriptError) Error() string {
	return "cannot read environment wrapper " + e.script
}

func (*wrapperScriptError) Help() string {
	return "check environment_wrapper in trocker.yaml"
}

func (e *wrapperScriptError) Unwrap() error { return e.cause }
