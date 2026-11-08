//go:build !linux && !darwin

package lifecycle

import "github.com/gabrielmbmb/trocker/internal/config"

// ephemeralPortRange returns IANA's dynamic range, the Windows default.
func ephemeralPortRange() config.PortRange { return config.PortRange{First: 49152, Last: 65535} }
