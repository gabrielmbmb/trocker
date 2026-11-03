package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// HostAccessGrant is the host-level access the manifest allows one service.
// An empty grant is invalid; fields left unset deny the corresponding access.
type HostAccessGrant struct {
	PID          bool         `yaml:"pid"`
	IPC          bool         `yaml:"ipc"`
	Capabilities []string     `yaml:"capabilities"`
	Devices      []HostDevice `yaml:"devices"`
}

// HostDevice grants a host device source with the exact permissions Compose
// must request. The container target is not part of the grant.
type HostDevice struct {
	Source      string `yaml:"source"`
	Permissions string `yaml:"permissions"`
}

// HostAccessFor returns the configured grant, or the zero value when the
// service has none.
func (m *Manifest) HostAccessFor(service string) HostAccessGrant {
	return m.HostAccess[service]
}

// AllowsCapability reports whether the grant covers a Compose cap_add value.
// Capability names are case-insensitive and may carry a CAP_ prefix.
func (g HostAccessGrant) AllowsCapability(capability string) bool {
	wanted := NormalizeCapability(capability)
	if wanted == "" || wanted == "ALL" {
		return false
	}
	for _, granted := range g.Capabilities {
		if NormalizeCapability(granted) == wanted {
			return true
		}
	}
	return false
}

// AllowsDevice reports whether the grant covers a host device source with the
// requested permissions. Empty Compose permissions mean the platform default.
func (g HostAccessGrant) AllowsDevice(source, permissions string) bool {
	if !ValidDevicePermissions(permissions) {
		return false
	}
	wantedSource := filepath.Clean(source)
	wantedPermissions := normalizeDevicePermissions(permissions)
	for _, device := range g.Devices {
		if strings.TrimSpace(device.Source) == "" || strings.TrimSpace(device.Permissions) == "" || !ValidDevicePermissions(device.Permissions) {
			continue
		}
		if filepath.Clean(device.Source) == wantedSource && normalizeDevicePermissions(device.Permissions) == wantedPermissions {
			return true
		}
	}
	return false
}

// NormalizeCapability canonicalizes a Linux capability name for comparison.
func NormalizeCapability(capability string) string {
	return strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(capability)), "CAP_")
}

// EffectiveDevicePermissions returns the permissions Compose applies: the
// configured value, or rwm when omitted.
func EffectiveDevicePermissions(value string) string {
	if value == "" {
		return "rwm"
	}
	return value
}

// ValidDevicePermissions reports whether value is a permission string made of
// r, w, and m. Empty is valid because Compose and Docker default to rwm.
func ValidDevicePermissions(value string) bool {
	for _, char := range value {
		if char != 'r' && char != 'w' && char != 'm' {
			return false
		}
	}
	return true
}

func normalizeDevicePermissions(value string) string {
	value = EffectiveDevicePermissions(value)
	var read, write, mknod bool
	for _, char := range value {
		switch char {
		case 'r':
			read = true
		case 'w':
			write = true
		case 'm':
			mknod = true
		}
	}
	var normalized strings.Builder
	if read {
		normalized.WriteByte('r')
	}
	if write {
		normalized.WriteByte('w')
	}
	if mknod {
		normalized.WriteByte('m')
	}
	return normalized.String()
}

func (m *Manifest) validateHostAccess() error {
	for service, grant := range m.HostAccess {
		if strings.TrimSpace(service) == "" {
			return invalid("host_access", "expected a nonempty service name")
		}
		field := "host_access." + service
		if !grant.PID && !grant.IPC && len(grant.Capabilities) == 0 && len(grant.Devices) == 0 {
			return invalid(field, "expected pid, ipc, capabilities, or devices")
		}
		capabilities := make(map[string]bool, len(grant.Capabilities))
		for i, capability := range grant.Capabilities {
			normalized := NormalizeCapability(capability)
			capabilityField := fmt.Sprintf("%s.capabilities[%d]", field, i)
			if normalized == "" {
				return invalid(capabilityField, "expected a nonempty capability name")
			}
			if normalized == "ALL" {
				return invalid(capabilityField, "list individual capabilities instead of ALL")
			}
			if capabilities[normalized] {
				return invalid(field+".capabilities", "duplicate capability "+normalized)
			}
			capabilities[normalized] = true
		}
		devices := make(map[string]bool, len(grant.Devices))
		for i, device := range grant.Devices {
			deviceField := fmt.Sprintf("%s.devices[%d]", field, i)
			if strings.TrimSpace(device.Source) == "" {
				return invalid(deviceField+".source", "expected a nonempty device source")
			}
			if strings.TrimSpace(device.Permissions) == "" || !ValidDevicePermissions(device.Permissions) {
				return invalid(deviceField+".permissions", "expected permissions made of r, w, and m")
			}
			source := filepath.Clean(device.Source)
			if devices[source] {
				return invalid(field+".devices", "duplicate device "+source)
			}
			devices[source] = true
		}
	}
	return nil
}
