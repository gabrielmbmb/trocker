package config_test

import (
	"testing"

	"github.com/gabrielmbmb/trocker/internal/config"
)

func TestHostAccessGrantAllowsCapability(t *testing.T) {
	grant := config.HostAccessGrant{Capabilities: []string{"SYS_PTRACE"}}
	for _, test := range []struct {
		name       string
		capability string
		allowed    bool
	}{
		{name: "exact", capability: "SYS_PTRACE", allowed: true},
		{name: "case and CAP prefix", capability: "cap_sys_ptrace", allowed: true},
		{name: "different capability", capability: "SYS_ADMIN"},
		{name: "empty capability"},
		{name: "wildcard", capability: "ALL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := grant.AllowsCapability(test.capability); got != test.allowed {
				t.Fatalf("AllowsCapability(%q) = %v, want %v", test.capability, got, test.allowed)
			}
		})
	}
}

func TestHostAccessGrantAllowsDevice(t *testing.T) {
	grant := config.HostAccessGrant{Devices: []config.HostDevice{{Source: "/dev/kmsg", Permissions: "rwm"}}}
	for _, test := range []struct {
		name        string
		source      string
		permissions string
		allowed     bool
	}{
		{name: "exact", source: "/dev/kmsg", permissions: "rwm", allowed: true},
		{name: "reordered", source: "/dev/kmsg", permissions: "mwr", allowed: true},
		{name: "default permissions", source: "/dev/kmsg", permissions: "", allowed: true},
		{name: "missing permissions", source: "/dev/kmsg", permissions: "rw"},
		{name: "different source", source: "/dev/null", permissions: "rwm"},
		{name: "invalid permissions", source: "/dev/kmsg", permissions: "rwx"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := grant.AllowsDevice(test.source, test.permissions); got != test.allowed {
				t.Fatalf("AllowsDevice(%q, %q) = %v, want %v", test.source, test.permissions, got, test.allowed)
			}
		})
	}
}
