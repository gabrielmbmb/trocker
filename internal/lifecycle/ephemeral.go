package lifecycle

import (
	"fmt"

	"github.com/gabrielmbmb/trocker/internal/config"
)

// checkStablePortRange rejects a stable range that overlaps the ports the OS
// hands to programs listening on port zero: once such a listener holds an
// assigned port, every later up fails until it exits.
func checkStablePortRange(manifest *config.Manifest, ephemeral config.PortRange) error {
	if !manifest.StablePorts {
		return nil
	}
	pool := manifest.StablePortPool()
	if !pool.Overlaps(ephemeral) {
		return nil
	}
	problem := fmt.Sprintf("%s overlaps this host's ephemeral port range %s", pool, ephemeral)
	if manifest.StablePortRange == "" {
		problem = fmt.Sprintf("the default range %s overlaps this host's ephemeral port range %s; declare a range outside it", pool, ephemeral)
	}
	return &config.Error{Field: "stable_port_range", Problem: problem}
}
