package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/generated"
	"github.com/gabrielmbmb/trocker/internal/native"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

// Run observes dependencies without starting them. File publication takes only
// a short consumer lock; no port claims or locks span the native child lifetime.
func Run(ctx context.Context, directory string, store state.Store, service string, args []string, streams native.Streams) error {
	if err := native.CheckSupport(); err != nil {
		return err
	}
	environment, err := prepareNative(ctx, directory, store, service)
	if err != nil {
		return err
	}
	return native.Run(ctx, native.Options{Args: args, Directory: directory, Environment: environment, Streams: streams})
}

func prepareNative(ctx context.Context, directory string, store state.Store, service string) (map[string]string, error) {
	registration, err := registeredWorkspace(ctx, directory, store)
	if err != nil {
		return nil, err
	}
	manifest, project, err := compose.Load(ctx, filepath.Join(registration.Checkout.Root, "trocker.yaml"))
	if err != nil {
		return nil, err
	}
	if _, ok := project.Services[service]; !ok {
		return nil, fmt.Errorf("native service is missing or inactive in the Compose project")
	}
	bindings := manifest.Bindings[service].Native
	templates, err := generated.Load(ctx, manifest.Directory, bindings.Files)
	if err != nil {
		return nil, err
	}
	definitions, err := nativeEndpointDefinitions(manifest, bindings)
	if err != nil {
		return nil, err
	}
	endpoints, targetID, err := observeNativeEndpoints(ctx, registration, definitions)
	if err != nil {
		return nil, err
	}
	values := nativeValues{id: registration.ID, addresses: make(map[string]*Address), files: make(map[string]string)}
	for _, endpoint := range endpoints {
		values.addresses[endpoint.Name] = endpoint.Addresses.Host
	}
	rendered, err := templates.Render(ctx, values.resolve)
	if err != nil {
		return nil, err
	}
	paths, err := store.PublishGenerated(ctx, registration.ID, targetID, service, rendered)
	if err != nil {
		return nil, err
	}
	for name, file := range bindings.Files {
		values.files[name] = paths[file.Output]
	}
	environment := make(map[string]string, len(bindings.Environment)+1)
	for key, value := range bindings.Environment {
		resolved, err := config.Expand(value, values.resolve)
		if err != nil {
			return nil, err
		}
		environment[key] = resolved
	}
	environment["TROCKER_WORKSPACE_ID"] = registration.ID
	return environment, nil
}

func nativeEndpointDefinitions(manifest *config.Manifest, bindings config.NativeBindings) (map[string]config.Endpoint, error) {
	definitions := make(map[string]config.Endpoint)
	collect := func(values map[string]string) error {
		for _, value := range values {
			_, err := config.Expand(value, func(reference string) (string, error) {
				if strings.HasPrefix(reference, "endpoints.") {
					alias := strings.Split(reference, ".")[1]
					definitions[alias] = manifest.Endpoints[alias]
				}
				return "", nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(bindings.Environment); err != nil {
		return nil, err
	}
	for _, file := range bindings.Files {
		if err := collect(file.Values); err != nil {
			return nil, err
		}
	}
	return definitions, nil
}

func observeNativeEndpoints(ctx context.Context, registration workspace.Registration, definitions map[string]config.Endpoint) ([]Endpoint, string, error) {
	if len(definitions) == 0 {
		return nil, "", nil
	}
	target, err := registeredTarget(ctx, registration)
	if err != nil {
		return nil, "", err
	}
	containers, err := target.Containers(ctx, registration)
	err = errors.Join(err, target.Close())
	if err != nil {
		return nil, "", err
	}
	endpoints, err := ResolveEndpoints(definitions, containers)
	if err != nil {
		return nil, "", err
	}
	for _, endpoint := range endpoints {
		if endpoint.Addresses.Host == nil {
			return nil, "", &nativeBindingError{endpoint: endpoint.Name}
		}
	}
	return endpoints, target.ID, nil
}

type nativeValues struct {
	id        string
	addresses map[string]*Address
	files     map[string]string
}

func (v nativeValues) resolve(reference string) (string, error) {
	if reference == "workspace.id" {
		return v.id, nil
	}
	parts := strings.Split(reference, ".")
	if parts[0] == "files" {
		path, ok := v.files[parts[1]]
		if !ok {
			return "", fmt.Errorf("native file path is unavailable")
		}
		return path, nil
	}
	address := v.addresses[parts[1]]
	if address == nil {
		return "", &nativeBindingError{endpoint: parts[1]}
	}
	switch parts[3] {
	case "host":
		return address.Host, nil
	case "port":
		return strconv.Itoa(int(address.Port)), nil
	default:
		return address.URL, nil
	}
}

type nativeBindingError struct{ endpoint string }

func (e *nativeBindingError) Error() string {
	return fmt.Sprintf("endpoint %s has no running host publication", e.endpoint)
}

func (*nativeBindingError) Help() string {
	return "start the dependency with trocker up --detach, then retry trocker run"
}
