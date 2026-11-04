package lifecycle

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
)

type EndpointResult struct {
	SchemaVersion int        `json:"schema_version"`
	WorkspaceID   string     `json:"workspace_id"`
	TargetID      string     `json:"target_id"`
	Endpoints     []Endpoint `json:"endpoints"`
}

type Endpoint struct {
	Name          string    `json:"name"`
	Service       string    `json:"service"`
	ContainerPort uint16    `json:"container_port"`
	Scheme        string    `json:"scheme"`
	State         string    `json:"state"`
	Addresses     Addresses `json:"addresses"`
}

type Addresses struct {
	Container Address  `json:"container"`
	Host      *Address `json:"host"`
}

type Address struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
	URL  string `json:"url,omitempty"`
}

// ResolveEndpoints requires ownership-validated summaries. A publication is an
// observation, not an application readiness or network reachability guarantee.
func ResolveEndpoints(definitions map[string]config.Endpoint, containers []api.ContainerSummary) ([]Endpoint, error) {
	endpoints := make([]Endpoint, 0, len(definitions))
	for name, definition := range definitions {
		endpoint := Endpoint{Name: name, Service: definition.Service, ContainerPort: definition.Port, Scheme: definition.Scheme, State: "missing", Addresses: Addresses{Container: address(definition.Service, definition.Port, definition.Scheme)}}
		matches := compose.ServiceContainers(containers, definition.Service, false)
		if len(matches) > 1 {
			return nil, fmt.Errorf("endpoint %s has multiple service containers; replicated endpoints are not supported", name)
		}
		if len(matches) == 1 {
			ctr := matches[0]
			if ctr.State != container.StateRunning {
				endpoint.State = "stopped"
			} else {
				host, err := publishedAddress(definition, ctr.Publishers)
				if err != nil {
					return nil, fmt.Errorf("endpoint %s: %w", name, err)
				}
				if host != nil {
					endpoint.State = "published"
					endpoint.Addresses.Host = host
				}
			}
		}
		endpoints = append(endpoints, endpoint)
	}
	slices.SortFunc(endpoints, func(a, b Endpoint) int { return strings.Compare(a.Name, b.Name) })
	return endpoints, nil
}

func publishedAddress(definition config.Endpoint, publishers api.PortPublishers) (*Address, error) {
	var result *Address
	for _, publisher := range publishers {
		if publisher.TargetPort != int(definition.Port) || publisher.Protocol != "tcp" || publisher.PublishedPort == 0 {
			continue
		}
		if result != nil {
			return nil, fmt.Errorf("multiple published mappings are ambiguous")
		}
		if publisher.PublishedPort < 1 || publisher.PublishedPort > 65535 {
			return nil, fmt.Errorf("invalid published port")
		}
		host := publisher.URL
		switch host {
		case "", "0.0.0.0":
			host = "127.0.0.1"
		case "::":
			host = "::1"
		}
		if net.ParseIP(host) == nil {
			return nil, fmt.Errorf("invalid published host address")
		}
		value := address(host, uint16(publisher.PublishedPort), definition.Scheme)
		result = &value
	}
	return result, nil
}

func address(host string, port uint16, scheme string) Address {
	result := Address{Host: host, Port: port}
	if scheme != "tcp" {
		result.URL = (&url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(int(port)))}).String()
	}
	return result
}
