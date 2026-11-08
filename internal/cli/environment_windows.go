package cli

import (
	"errors"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func execInWrapper(*lifecycle.EnvironmentWrapper, []string) error {
	return errors.New("environment_wrapper is not supported on Windows")
}
