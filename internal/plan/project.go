// Package plan computes workspace isolation without I/O or resource mutations.
package plan

import (
	"cmp"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"

	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

const WorkspaceLabel = "io.trocker.workspace-id"

// Build returns a deep-copied SDK project. Neither the supplied configuration
// nor the loaded project is modified; callers must never print its environment.
func Build(manifest *config.Manifest, source *types.Project, registration workspace.Registration, options Options) (*types.Project, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if err := manifest.ValidateProject(source); err != nil {
		return nil, err
	}
	selected, err := source.WithSelectedServices(options.Services)
	if err != nil {
		return nil, fmt.Errorf("selected service is not active in the Compose project")
	}
	project, err := selected.WithServicesTransform(func(name string, service types.ServiceConfig) (types.ServiceConfig, error) {
		if err := isolateService(&service, registration, manifest); err != nil {
			return service, fmt.Errorf("service %s: %w", name, err)
		}
		if err := bindEnvironment(&service, manifest, selected, registration.ID, options); err != nil {
			return service, err
		}
		for _, file := range manifest.Bindings[name].Files {
			for _, value := range file.Values {
				if _, err := config.Expand(value, func(ref string) (string, error) {
					return resolve(ref, name, manifest, selected, registration.ID, options)
				}); err != nil {
					return service, err
				}
			}
		}
		publishEndpoints(&service, manifest.Endpoints)
		for i := range service.Ports {
			key := PortKey{Service: name, Port: uint16(service.Ports[i].Target)} //nolint:gosec // isolatedPorts and manifest validation enforce 1..65535.
			if port := options.Ports[key]; port != 0 {
				service.Ports[i].Published = strconv.Itoa(int(port))
			}
		}
		return service, nil
	})
	if err != nil {
		return nil, err
	}
	project.Name = registration.ProjectName()
	if err := isolateNetworks(project, source.Name, registration); err != nil {
		return nil, err
	}
	if err := isolateVolumes(project, source.Name, registration); err != nil {
		return nil, err
	}
	return project.WithoutUnnecessaryResources(), nil
}

func isolateNetworks(project *types.Project, originalName string, registration workspace.Registration) error {
	for name, network := range project.Networks {
		if network.External || network.Driver != "" && network.Driver != "bridge" || len(network.DriverOpts) > 0 || network.Ipam.Driver != "" || len(network.Ipam.Config) > 0 || len(network.Ipam.Options) > 0 {
			return fmt.Errorf("network %s: external, custom-driver, and custom-IPAM networks are not supported", name)
		}
		if network.Name != "" && network.Name != originalName+"_"+name {
			return fmt.Errorf("network %s: explicit names are not supported", name)
		}
		if err := checkLabels(network.Labels, registration.ID); err != nil {
			return err
		}
		network.Name = project.Name + "_" + name
		network.Labels = network.Labels.Add(WorkspaceLabel, registration.ID)
		project.Networks[name] = network
	}

	return nil
}

func isolateVolumes(project *types.Project, originalName string, registration workspace.Registration) error {
	for name, volume := range project.Volumes {
		if volume.External || volume.Driver != "" && volume.Driver != "local" || len(volume.DriverOpts) > 0 {
			return fmt.Errorf("volume %s: external and custom-driver volumes are not supported", name)
		}
		if volume.Name != "" && volume.Name != originalName+"_"+name {
			return fmt.Errorf("volume %s: explicit names are not supported", name)
		}
		if err := checkLabels(volume.Labels, registration.ID); err != nil {
			return err
		}
		volume.Name = project.Name + "_" + name
		volume.Labels = volume.Labels.Add(WorkspaceLabel, registration.ID)
		project.Volumes[name] = volume
	}

	return nil
}

func bindEnvironment(service *types.ServiceConfig, manifest *config.Manifest, project *types.Project, id string, options Options) error {
	name := service.Name
	for key, value := range manifest.Bindings[name].Environment {
		resolved, err := config.Expand(value, func(reference string) (string, error) {
			return resolve(reference, name, manifest, project, id, options)
		})
		if err != nil {
			return &config.Error{Field: "bindings." + name + ".environment." + key, Problem: err.Error()}
		}
		service.Environment[key] = &resolved
	}

	return nil
}

func publishEndpoints(service *types.ServiceConfig, endpoints map[string]config.Endpoint) {
	name := service.Name
	for _, endpoint := range endpoints {
		if endpoint.Service == name {
			found := false
			for _, port := range service.Ports {
				if port.Target == uint32(endpoint.Port) {
					found = true
				}
			}
			if !found {
				service.Ports = append(service.Ports, types.ServicePortConfig{Target: uint32(endpoint.Port), Published: "0", HostIP: "127.0.0.1", Protocol: "tcp", Mode: "ingress"})
			}
		}
	}
	slices.SortFunc(service.Ports, func(a, b types.ServicePortConfig) int { return cmp.Compare(a.Target, b.Target) })
}

func isolateService(service *types.ServiceConfig, registration workspace.Registration, manifest *config.Manifest) error {
	if service.NetworkMode != "" {
		return fmt.Errorf("network_mode is not supported; use Compose bridge networks")
	}
	if service.Scale != nil && *service.Scale != 1 {
		return fmt.Errorf("replicated services are not supported")
	}
	if err := checkHostAccess(service, manifest.HostAccessFor(service.Name)); err != nil {
		return err
	}
	if service.UseAPISocket && !manifest.AllowsDockerSocket(service.Name) {
		return newSocketError(service.Name, "")
	}
	for _, mount := range service.Volumes {
		switch mount.Type {
		case types.VolumeTypeBind:
			source := filepath.Clean(mount.Source)
			if ExposesDockerSocket(source) {
				if !manifest.AllowsDockerSocket(service.Name) {
					return newSocketError(service.Name, source)
				}
				continue
			}
			if !mount.ReadOnly && !manifest.AllowsWrite(source) {
				return newWriteError(manifest, source)
			}
		case types.VolumeTypeVolume:
			if mount.Source == "" {
				return fmt.Errorf("anonymous volumes are not supported; declare a named volume")
			}
		case types.VolumeTypeTmpfs:
		default:
			return fmt.Errorf("unsupported mount type")
		}
	}
	if err := checkLabels(service.Labels, registration.ID); err != nil {
		return err
	}
	for _, network := range service.Networks {
		if network != nil && (len(network.DriverOpts) > 0 || network.Ipv4Address != "" || network.Ipv6Address != "" || len(network.LinkLocalIPs) > 0 || network.MacAddress != "") {
			return fmt.Errorf("custom network addressing and driver options are not supported")
		}
	}
	if existing, ok := service.Environment["TROCKER_WORKSPACE_ID"]; ok && (existing == nil || *existing != registration.ID) {
		return fmt.Errorf("TROCKER_WORKSPACE_ID conflicts with registered identity")
	}
	if service.Environment == nil {
		service.Environment = types.MappingWithEquals{}
	}
	id := registration.ID
	service.Environment["TROCKER_WORKSPACE_ID"] = &id
	service.ContainerName = ""
	service.Labels = service.Labels.Add(WorkspaceLabel, id)
	service.CustomLabels = service.CustomLabels.Add(api.ProjectLabel, registration.ProjectName()).Add(api.ServiceLabel, service.Name).Add(api.OneoffLabel, "False")
	if service.Build != nil {
		if service.Name != strings.ToLower(service.Name) {
			return fmt.Errorf("built service names must be lowercase for workspace image tags")
		}
		if err := checkLabels(service.Build.Labels, registration.ID); err != nil {
			return err
		}
		service.Image = registration.ProjectName() + "-" + service.Name + ":latest"
		service.Build.Labels = service.Build.Labels.Add(WorkspaceLabel, id)
	}
	ports, err := isolatedPorts(service.Ports)
	if err != nil {
		return err
	}
	service.Ports = ports
	return nil
}

var dockerSocketPaths = []string{"/var/run/docker.sock", "/run/docker.sock"}

// ExposesDockerSocket reports whether a bind source is a Docker API socket or
// a directory containing one. A read-only mount does not prevent connecting to
// a socket, so both modes grant API access. Matching is lexical; symlinks are
// not resolved.
func ExposesDockerSocket(source string) bool {
	source = filepath.Clean(source)
	if filepath.Base(source) == "docker.sock" {
		return true
	}
	for _, socket := range dockerSocketPaths {
		if inside(source, socket) {
			return true
		}
	}
	return false
}

// inside reports whether path is root or a descendant of root.
func inside(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// policyError is a safe, actionable isolation-policy diagnostic.
type policyError struct {
	problem string
	help    string
	note    string
}

func (e *policyError) Error() string { return e.problem }
func (e *policyError) Help() string  { return e.help }
func (e *policyError) Note() string  { return e.note }

func newSocketError(service, source string) error {
	help := "add " + service + " under docker_socket_services in trocker.yaml, or remove the Docker socket access"
	note := "Docker API access is not limited to this workspace and can control other containers"
	if source == "" {
		return &policyError{problem: "use_api_socket is not declared in docker_socket_services", help: help, note: note}
	}
	return &policyError{problem: "Docker API access through " + source + " is not declared in docker_socket_services", help: help, note: note}
}

// newWriteError rejects a writable bind mount that no declared path allows.
// Host paths are user input and plans print them, so they are safe to report.
func newWriteError(manifest *config.Manifest, source string) error {
	if !manifest.Inside(source) {
		return &policyError{
			problem: "writable bind mount " + source + " is outside the checkout",
			help:    "only paths inside the checkout can be writable; remove the mount if it must stay outside",
		}
	}
	return &policyError{
		problem: "writable bind mount " + source + " is not declared in read_write_paths",
		help:    "add the path under read_write_paths in trocker.yaml, or mount it read-only",
	}
}

func checkLabels(labels types.Labels, id string) error {
	for key, value := range labels {
		if strings.HasPrefix(key, "com.docker.compose.") || key == WorkspaceLabel && value != id {
			return fmt.Errorf("explicit ownership labels conflict with managed identity")
		}
	}
	return nil
}

func isolatedPorts(source []types.ServicePortConfig) ([]types.ServicePortConfig, error) {
	ports := make([]types.ServicePortConfig, 0, len(source))
	seen := map[uint32]types.ServicePortConfig{}
	for _, port := range source {
		if port.Protocol != "" && port.Protocol != "tcp" {
			return nil, fmt.Errorf("only TCP publications are supported")
		}
		if port.Target == 0 || port.Target > 65535 {
			return nil, fmt.Errorf("invalid container port")
		}
		if previous, ok := seen[port.Target]; ok {
			if !reflect.DeepEqual(previous, port) {
				return nil, fmt.Errorf("conflicting publications for one container port")
			}
			continue
		}
		seen[port.Target] = port
		port.Published = "0"
		port.HostIP = "127.0.0.1"
		port.Protocol = "tcp"
		port.Mode = "ingress"
		ports = append(ports, port)
	}
	return ports, nil
}

func resolve(reference, consumer string, manifest *config.Manifest, project *types.Project, id string, options Options) (string, error) {
	if reference == "workspace.id" {
		return id, nil
	}
	parts := strings.Split(reference, ".")
	if parts[0] == "files" {
		return manifest.Bindings[consumer].Files[parts[1]].Target, nil
	}
	endpoint := manifest.Endpoints[parts[1]]
	if _, ok := project.Services[endpoint.Service]; !ok {
		return "", fmt.Errorf("endpoint service is not selected; include it or declare depends_on")
	}
	if parts[2] == "host" {
		return resolveHost(endpoint, parts[3], options)
	}
	shared := false
	for name := range project.Services[consumer].Networks {
		if _, ok := project.Services[endpoint.Service].Networks[name]; ok {
			shared = true
		}
	}
	if !shared {
		return "", fmt.Errorf("consumer and endpoint service must share a Compose network")
	}
	port := strconv.Itoa(int(endpoint.Port))
	switch parts[3] {
	case "host":
		return endpoint.Service, nil
	case "port":
		return port, nil
	default:
		return (&url.URL{Scheme: endpoint.Scheme, Host: net.JoinHostPort(endpoint.Service, port)}).String(), nil
	}
}
