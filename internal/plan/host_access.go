package plan

import (
	"fmt"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/gabrielmbmb/trocker/internal/config"
)

func checkHostAccess(service *types.ServiceConfig, grant config.HostAccessGrant) error {
	switch service.Pid {
	case "":
	case "host":
		if !grant.PID {
			return newHostAccessError("pid: host is not declared in host_access", "add pid: true under host_access."+service.Name+".pid in trocker.yaml, or remove pid")
		}
	default:
		return newHostAccessError("only pid: host is supported", "use pid: host with a host_access grant, or remove pid")
	}

	switch service.Ipc {
	case "", "private":
	case "host":
		if !grant.IPC {
			return newHostAccessError("ipc: host is not declared in host_access", "add ipc: true under host_access."+service.Name+".ipc in trocker.yaml, or remove ipc")
		}
	default:
		return newHostAccessError("only ipc: host and ipc: private are supported", "use ipc: host with a host_access grant, or remove ipc")
	}

	for _, capability := range service.CapAdd {
		normalized := config.NormalizeCapability(capability)
		if normalized == "" {
			return newHostAccessError("cap_add contains an empty capability", "list individual capabilities and declare them under host_access."+service.Name+".capabilities")
		}
		if normalized == "ALL" {
			return newHostAccessError("cap_add ALL is not supported", "list individual capabilities and declare them under host_access."+service.Name+".capabilities")
		}
		if !grant.AllowsCapability(capability) {
			return newHostAccessError("cap_add "+capability+" is not declared in host_access", "add "+capability+" under host_access."+service.Name+".capabilities in trocker.yaml, or remove it")
		}
	}

	for _, device := range service.Devices {
		if device.Source == "" {
			return newHostAccessError("device source is empty", "declare a device source in Compose")
		}
		permissions := config.EffectiveDevicePermissions(device.Permissions)
		if !config.ValidDevicePermissions(device.Permissions) {
			return newHostAccessError("device "+device.Source+" has invalid permissions", "use only r, w, and m for device permissions")
		}
		if !grant.AllowsDevice(device.Source, device.Permissions) {
			return newHostAccessError(fmt.Sprintf("device %s (%s) is not declared in host_access", device.Source, permissions), "add the device under host_access."+service.Name+".devices in trocker.yaml, or remove it")
		}
	}

	return nil
}

func newHostAccessError(problem, help string) error {
	return &policyError{problem: problem, help: help, note: "host namespaces, capabilities, and devices are not workspace-isolated"}
}
