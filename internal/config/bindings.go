package config

import "strings"

func (m *Manifest) validateBindings() error {
	for service, bindings := range m.Bindings {
		if strings.TrimSpace(service) == "" {
			return invalid("bindings", "expected a nonempty service name")
		}
		field := "bindings." + service
		if err := m.validateFiles(bindings.Files, bindings.Environment, field, false); err != nil {
			return err
		}
		if err := m.validateEnvironment(bindings.Environment, field+".environment", false, bindings.Files); err != nil {
			return err
		}
		if err := m.validateEnvironment(bindings.Native.Environment, field+".native.environment", true, bindings.Native.Files); err != nil {
			return err
		}
		if err := m.validateFiles(bindings.Native.Files, bindings.Native.Environment, field+".native", true); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manifest) validateEnvironment(environment map[string]string, field string, native bool, files map[string]GeneratedFile) error {
	for name, value := range environment {
		if !environmentPattern.MatchString(name) {
			return invalid(field, "invalid environment name")
		}
		path := field + "." + name
		if name == "TROCKER_WORKSPACE_ID" && value != "{{ workspace.id }}" {
			return invalid(path, "reserved identity binding must reference workspace.id exactly")
		}
		if err := m.validateValue(value, path, native, files); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manifest) validateValue(value, field string, native bool, files map[string]GeneratedFile) error {
	for remaining := value; remaining != ""; {
		start := strings.Index(remaining, "{{")
		end := strings.Index(remaining, "}}")
		if start < 0 && end < 0 {
			break
		}
		if start < 0 || end < start {
			return invalid(field, "unmatched binding delimiter")
		}
		reference := strings.TrimSpace(remaining[start+2 : end])
		if err := m.validateReference(reference, native, files); err != nil {
			return invalid(field, err.Error())
		}
		remaining = remaining[end+2:]
	}
	return nil
}

func (m *Manifest) validateReference(reference string, native bool, files map[string]GeneratedFile) error {
	if reference == "workspace.id" {
		return nil
	}
	parts := strings.Split(reference, ".")
	if len(parts) == 3 && parts[0] == "files" && parts[2] == "path" {
		if files == nil {
			return invalid("reference", "file paths are allowed only in the declaring consumer environment")
		}
		if _, ok := files[parts[1]]; !ok {
			return invalid("reference", "unknown file in this consumer")
		}
		return nil
	}
	if len(parts) != 4 || parts[0] != "endpoints" {
		return invalid("reference", "unsupported binding reference")
	}
	endpoint, exists := m.Endpoints[parts[1]]
	if !exists {
		return invalid("reference", "unknown endpoint")
	}
	switch parts[2] {
	case "container":
		if native {
			return invalid("reference", "native bindings cannot use container addresses")
		}
	case "host":
	case "public":
		return invalid("reference", "public addresses are not supported; use host addresses instead")
	default:
		return invalid("reference", "unsupported address representation")
	}
	switch parts[3] {
	case "host", "port":
	case "url":
		if endpoint.Scheme == "tcp" {
			return invalid("reference", "TCP endpoints do not define URLs")
		}
	default:
		return invalid("reference", "unsupported address component")
	}
	return nil
}
