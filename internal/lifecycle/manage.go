package lifecycle

import (
	"context"
	"errors"
	"io"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/state"
)

// ApplyLifecycle operates on registered, existing resources. Configuration may
// have changed or disappeared since up; no startup plan or binding rewrite is
// needed to start, stop, or remove those resources.
func ApplyLifecycle(ctx context.Context, directory string, store state.Store, options compose.LifecycleOptions, output io.Writer) (err error) {
	if err := options.Validate(); err != nil {
		return err
	}
	registration, err := registeredWorkspace(ctx, directory, store)
	if err != nil {
		return err
	}
	lock, err := store.LockWorkspace(ctx, registration.Checkout.GitDir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	registration, err = registeredWorkspace(ctx, directory, store)
	if err != nil {
		return err
	}
	target, err := registeredTarget(ctx, registration)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	return target.ApplyLifecycle(ctx, registration, options, output)
}
