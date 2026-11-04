package config

import "github.com/compose-spec/compose-go/v2/types"

// ValidateProject checks Trocker references against the SDK's active model.
// Runtime reachability, identity injection and isolation policy belong to planning.
func (m *Manifest) ValidateProject(project *types.Project) error {
	for _, service := range m.DockerSocketServices {
		if _, exists := project.Services[service]; !exists {
			return inactiveServiceError("docker_socket_services", service, project)
		}
	}
	for service := range m.HostAccess {
		if _, exists := project.Services[service]; !exists {
			return inactiveServiceError("host_access", service, project)
		}
	}
	for name, endpoint := range m.Endpoints {
		service, exists := project.Services[endpoint.Service]
		if !exists {
			return inactiveServiceError("endpoints."+name+".service", endpoint.Service, project)
		}
		declared := false
		for _, port := range service.Ports {
			if port.Target == uint32(endpoint.Port) && (port.Protocol == "" || port.Protocol == "tcp") {
				declared = true
			}
		}
		for _, expose := range service.Expose {
			ports, err := types.ParsePortConfig(expose)
			if err != nil {
				return invalid("endpoints."+name+".port", "invalid Compose expose declaration")
			}
			for _, port := range ports {
				if port.Target == uint32(endpoint.Port) && (port.Protocol == "" || port.Protocol == "tcp") {
					declared = true
				}
			}
		}
		if !declared {
			return invalid("endpoints."+name+".port", "port is not declared as TCP by the service")
		}
	}
	for service, bindings := range m.Bindings {
		definition, exists := project.Services[service]
		if !exists {
			return inactiveServiceError("bindings."+service, service, project)
		}
		if err := validateProjectFiles(bindings.Files, definition); err != nil {
			return invalid("bindings."+service+".files", err.Error())
		}
	}
	return nil
}

// inactiveServiceError names the profiles a referenced service needs and the
// profiles that are already active, so a mismatch is not blamed on the
// manifest or the shell without explanation.
func inactiveServiceError(field, service string, project *types.Project) error {
	problem := "service " + service + " is missing or inactive"
	if definition, disabled := project.DisabledServices[service]; disabled && len(definition.Profiles) > 0 {
		problem += "; add " + ProfileSummary(definition.Profiles) + " to profiles in trocker.yaml"
	}
	problem += "; active profiles: " + ProfileSummary(project.Profiles)
	return invalid(field, problem)
}
