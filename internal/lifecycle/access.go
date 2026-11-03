package lifecycle

import (
	"slices"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/plan"
)

// WritableMount is a bind mount that lets containers modify a host path.
type WritableMount struct {
	Service string
	Source  string
	Target  string
}

// WritableMounts lists declared writable bind mounts in service order.
func (r PlanResult) WritableMounts() []WritableMount {
	var mounts []WritableMount
	for _, name := range r.Project.ServiceNames() {
		for _, mount := range r.Project.Services[name].Volumes {
			if mount.Type == types.VolumeTypeBind && !mount.ReadOnly && !plan.ExposesDockerSocket(mount.Source) {
				mounts = append(mounts, WritableMount{Service: name, Source: mount.Source, Target: mount.Target})
			}
		}
	}
	return mounts
}

// DockerAPIAccess is a service that can reach the Docker daemon. Source is the
// host socket path, or empty when Compose's use_api_socket adds the mount.
type DockerAPIAccess struct {
	Service string
	Source  string
}

// DockerAPIAccess lists Docker API access in service order.
func (r PlanResult) DockerAPIAccess() []DockerAPIAccess {
	var access []DockerAPIAccess
	for _, name := range r.Project.ServiceNames() {
		service := r.Project.Services[name]
		if service.UseAPISocket {
			access = append(access, DockerAPIAccess{Service: name})
		}
		for _, mount := range service.Volumes {
			if mount.Type == types.VolumeTypeBind && plan.ExposesDockerSocket(mount.Source) {
				access = append(access, DockerAPIAccess{Service: name, Source: mount.Source})
			}
		}
	}
	return access
}

// ServiceHostAccess is a service's actual host namespace, capability, or
// device access in the planned project.
type ServiceHostAccess struct {
	Service      string
	PID          bool
	IPC          bool
	Capabilities []string
	Devices      []DeviceAccess
}

// DeviceAccess is an actual host device mapping in the planned project.
type DeviceAccess struct {
	Source      string
	Permissions string
}

// HostAccess lists actual host-level access in service order.
func (r PlanResult) HostAccess() []ServiceHostAccess {
	var access []ServiceHostAccess
	for _, name := range r.Project.ServiceNames() {
		service := r.Project.Services[name]
		entry := ServiceHostAccess{Service: name, PID: service.Pid == "host", IPC: service.Ipc == "host", Capabilities: slices.Clone(service.CapAdd)}
		for _, device := range service.Devices {
			entry.Devices = append(entry.Devices, DeviceAccess{Source: device.Source, Permissions: config.EffectiveDevicePermissions(device.Permissions)})
		}
		if entry.PID || entry.IPC || len(entry.Capabilities) > 0 || len(entry.Devices) > 0 {
			access = append(access, entry)
		}
	}
	return access
}
