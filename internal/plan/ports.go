package plan

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/gabrielmbmb/trocker/internal/config"
)

// PortKey identifies one TCP publication; aliases of the same service port share it.
type PortKey struct {
	Service string
	Port    uint16
}

type Options struct {
	Services []string
	Ports    map[PortKey]uint16
	// Preview permits unresolved addresses for a read-only plan, never startup.
	Preview bool
}

// RequiredPorts includes only addresses needed by selected container bindings.
// Native bindings use observations later and need no pre-start allocation here.
func RequiredPorts(manifest *config.Manifest, project *types.Project) ([]PortKey, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	required := make(map[PortKey]bool)
	for name := range project.Services {
		values := make([]string, 0)
		for _, value := range manifest.Bindings[name].Environment {
			values = append(values, value)
		}
		for _, file := range manifest.Bindings[name].Files {
			for _, value := range file.Values {
				values = append(values, value)
			}
		}
		for _, value := range values {
			_, err := config.Expand(value, func(reference string) (string, error) {
				parts := strings.Split(reference, ".")
				if len(parts) == 4 && parts[2] == "host" && parts[3] != "host" {
					endpoint := manifest.Endpoints[parts[1]]
					required[PortKey{Service: endpoint.Service, Port: endpoint.Port}] = true
				}
				return "", nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	keys := make([]PortKey, 0, len(required))
	for key := range required {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b PortKey) int {
		if a.Service != b.Service {
			return strings.Compare(a.Service, b.Service)
		}
		return cmp.Compare(a.Port, b.Port)
	})
	return keys, nil
}

func resolveHost(endpoint config.Endpoint, component string, options Options) (string, error) {
	if component == "host" {
		return "127.0.0.1", nil
	}
	port := options.Ports[PortKey{Service: endpoint.Service, Port: endpoint.Port}]
	if port == 0 && !options.Preview {
		return "", fmt.Errorf("host binding requires a coordinated port allocation")
	}
	value := "<allocated-at-startup>"
	if port != 0 {
		value = strconv.Itoa(int(port))
	}
	switch component {
	case "port":
		return value, nil
	default:
		return endpoint.Scheme + "://127.0.0.1:" + value, nil
	}
}
