package plan

import (
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/gabrielmbmb/trocker/internal/config"
)

// ResolveFileValue uses the same selection, reachability and allocation rules as
// container environment bindings. File paths cannot occur in validated values.
func ResolveFileValue(reference, consumer string, manifest *config.Manifest, project *types.Project, id string, options Options) (string, error) {
	return resolve(reference, consumer, manifest, project, id, options)
}
