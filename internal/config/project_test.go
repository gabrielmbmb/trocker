package config_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/gabrielmbmb/trocker/internal/config"
)

func TestValidateProjectEndpointDeclarations(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name              string
		ports             []types.ServicePortConfig
		expose            []string
		missing, inactive bool
		field             string
	}{
		{name: "container port, not published port", ports: []types.ServicePortConfig{{Target: 8000, Published: "24000", Protocol: "tcp"}}},
		{name: "published port is not container port", ports: []types.ServicePortConfig{{Target: 9000, Published: "8000", Protocol: "tcp"}}, field: "endpoints.api.port"},
		{name: "TCP expose without publication", expose: []string{"8000"}},
		{name: "UDP publication does not satisfy TCP endpoint", ports: []types.ServicePortConfig{{Target: 8000, Protocol: "udp"}}, field: "endpoints.api.port"},
		{name: "UDP expose does not satisfy TCP endpoint", expose: []string{"8000/udp"}, field: "endpoints.api.port"},
		{name: "TCP declaration alongside UDP", ports: []types.ServicePortConfig{{Target: 8000, Protocol: "udp"}}, expose: []string{"8000/tcp"}},
		{name: "undeclared port", field: "endpoints.api.port"},
		{name: "missing service", missing: true, field: "endpoints.api.service"},
		{name: "inactive service", inactive: true, expose: []string{"8000"}, field: "endpoints.api.service"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			// Independent copies let us detect validation changing the SDK model.
			project := func() *types.Project {
				service := types.ServiceConfig{
					Name:   "backend",
					Ports:  slices.Clone(test.ports),
					Expose: slices.Clone(test.expose),
				}
				p := &types.Project{Services: types.Services{}, DisabledServices: types.Services{}}
				if test.inactive {
					p.DisabledServices["backend"] = service
				} else if !test.missing {
					p.Services["backend"] = service
				}
				return p
			}
			actual, before := project(), project()
			manifest := config.Manifest{Endpoints: map[string]config.Endpoint{"api": {Service: "backend", Port: 8000, Scheme: "http"}}}
			err := manifest.ValidateProject(actual)
			if test.field == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var validation *config.Error
				if !errors.As(err, &validation) || validation.Field != test.field {
					t.Fatalf("error = %v, want error at %s", err, test.field)
				}
			}
			if !reflect.DeepEqual(actual, before) {
				t.Fatal("validation changed the Compose project")
			}
			if manifest.Endpoints["api"] != (config.Endpoint{Service: "backend", Port: 8000, Scheme: "http"}) {
				t.Fatal("validation changed the endpoint")
			}
		})
	}
}

func TestValidateProjectDockerSocketServicesRequireActiveService(t *testing.T) {
	t.Parallel()
	project := func() *types.Project {
		return &types.Project{
			Services:         types.Services{"localstack": {Name: "localstack"}},
			DisabledServices: types.Services{"dozzle": {Name: "dozzle"}},
		}
	}
	for _, test := range []struct {
		name    string
		service string
		valid   bool
	}{
		{name: "active service", service: "localstack", valid: true},
		{name: "inactive service", service: "dozzle"},
		{name: "missing service", service: "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual, before := project(), project()
			manifest := config.Manifest{DockerSocketServices: []string{test.service}}
			err := manifest.ValidateProject(actual)
			if !reflect.DeepEqual(actual, before) {
				t.Fatal("validation changed the Compose project")
			}
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var validation *config.Error
			if !errors.As(err, &validation) || validation.Field != "docker_socket_services" {
				t.Fatalf("error = %v, want missing/inactive Docker socket service", err)
			}
		})
	}
}

func TestValidateProjectBindingsRequireActiveServiceWithoutEndpoints(t *testing.T) {
	t.Parallel()
	project := &types.Project{
		Services:         types.Services{"web": {Name: "web"}},
		DisabledServices: types.Services{"debug": {Name: "debug"}},
	}
	for _, service := range []string{"web", "debug", "missing"} {
		t.Run(service, func(t *testing.T) {
			t.Parallel()
			// An empty binding still refers to a service; no endpoints are necessary.
			manifest := config.Manifest{Bindings: map[string]config.Bindings{service: {}}}
			err := manifest.ValidateProject(project)
			if service == "web" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var validation *config.Error
				if !errors.As(err, &validation) || validation.Field != "bindings."+service {
					t.Fatalf("error = %v, want missing/inactive binding service", err)
				}
			}
		})
	}
}
