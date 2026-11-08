package lifecycle

import (
	"golang.org/x/sys/unix"

	"github.com/gabrielmbmb/trocker/internal/config"
)

func ephemeralPortRange() config.PortRange {
	first, firstErr := unix.SysctlUint32("net.inet.ip.portrange.first")
	last, lastErr := unix.SysctlUint32("net.inet.ip.portrange.last")
	if firstErr != nil || lastErr != nil || first > last || last > 65535 {
		return config.PortRange{First: 49152, Last: 65535}
	}
	return config.PortRange{First: uint16(first), Last: uint16(last)} //nolint:gosec // Both are checked against 65535 above.
}
