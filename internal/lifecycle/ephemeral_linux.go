package lifecycle

import (
	"os"
	"strconv"
	"strings"

	"github.com/gabrielmbmb/trocker/internal/config"
)

// linuxDefaultEphemeralRange is the kernel default for ip_local_port_range.
var linuxDefaultEphemeralRange = config.PortRange{First: 32768, Last: 60999}

func ephemeralPortRange() config.PortRange {
	content, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return linuxDefaultEphemeralRange
	}
	if ports, ok := parseLocalPortRange(string(content)); ok {
		return ports
	}
	return linuxDefaultEphemeralRange
}

// parseLocalPortRange reads ip_local_port_range, two whitespace-separated
// ports. It accepts any valid range because the kernel, not Trocker, sets it.
func parseLocalPortRange(content string) (config.PortRange, bool) {
	fields := strings.Fields(content)
	if len(fields) != 2 {
		return config.PortRange{}, false
	}
	first, firstErr := strconv.ParseUint(fields[0], 10, 16)
	last, lastErr := strconv.ParseUint(fields[1], 10, 16)
	if firstErr != nil || lastErr != nil || first > last {
		return config.PortRange{}, false
	}
	return config.PortRange{First: uint16(first), Last: uint16(last)}, true
}
