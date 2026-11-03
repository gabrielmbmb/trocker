package lifecycle_test

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func TestPlanReportsDockerAPIAccessSeparately(t *testing.T) {
	project := &types.Project{Services: types.Services{
		"agent": {Name: "agent", UseAPISocket: true},
		"api": {Name: "api", Volumes: []types.ServiceVolumeConfig{
			{Type: types.VolumeTypeBind, Source: "/checkout/src", Target: "/app"},
		}},
		"localstack": {Name: "localstack", Volumes: []types.ServiceVolumeConfig{
			{Type: types.VolumeTypeBind, Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		}},
	}}
	result := lifecycle.PlanResult{Project: project}

	writable := result.WritableMounts()
	if len(writable) != 1 || writable[0].Service != "api" {
		t.Fatalf("writable mounts = %#v, want only api", writable)
	}
	access := result.DockerAPIAccess()
	if len(access) != 2 || access[0].Service != "agent" || access[0].Source != "" || access[1].Service != "localstack" || access[1].Source != "/var/run/docker.sock" {
		t.Fatalf("Docker API access = %#v", access)
	}
}

func TestPlanReportsHostAccess(t *testing.T) {
	project := &types.Project{Services: types.Services{
		"agent": {Name: "agent", Pid: "host", Ipc: "host", CapAdd: []string{"SYS_PTRACE"}, Devices: []types.DeviceMapping{
			{Source: "/dev/kmsg", Target: "/dev/kmsg", Permissions: "rw"},
			{Source: "/dev/null", Target: "/dev/null"},
		}},
		"api": {Name: "api"},
	}}
	result := lifecycle.PlanResult{Project: project}

	access := result.HostAccess()
	if len(access) != 1 {
		t.Fatalf("host access = %#v, want one service", access)
	}
	entry := access[0]
	if entry.Service != "agent" || !entry.PID || !entry.IPC || len(entry.Capabilities) != 1 || entry.Capabilities[0] != "SYS_PTRACE" {
		t.Fatalf("host access entry = %#v", entry)
	}
	if len(entry.Devices) != 2 || entry.Devices[0] != (lifecycle.DeviceAccess{Source: "/dev/kmsg", Permissions: "rw"}) || entry.Devices[1] != (lifecycle.DeviceAccess{Source: "/dev/null", Permissions: "rwm"}) {
		t.Fatalf("host devices = %#v", entry.Devices)
	}
}

func TestPlanReportsBuildContextsInOrder(t *testing.T) {
	project := &types.Project{Services: types.Services{
		"web": {Name: "web", Build: &types.BuildConfig{AdditionalContexts: types.Mapping{
			"shared":  "/checkout/packages/shared",
			"private": "/checkout/packages/private",
		}}},
		"api": {Name: "api"},
	}}
	result := lifecycle.PlanResult{Project: project}

	contexts := result.BuildContexts()
	if len(contexts) != 2 {
		t.Fatalf("build contexts = %#v", contexts)
	}
	if contexts[0] != (lifecycle.BuildContext{Service: "web", Name: "private", Source: "/checkout/packages/private"}) ||
		contexts[1] != (lifecycle.BuildContext{Service: "web", Name: "shared", Source: "/checkout/packages/shared"}) {
		t.Fatalf("build contexts = %#v", contexts)
	}
}
