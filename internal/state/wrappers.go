package state

import (
	"context"
	"slices"
)

// ApproveWrapper records an environment wrapper fingerprint as approved.
func (s Store) ApproveWrapper(ctx context.Context, fingerprint string) error {
	return s.Update(ctx, func(registry *Registry) error {
		if !slices.Contains(registry.ApprovedWrappers, fingerprint) {
			registry.ApprovedWrappers = append(registry.ApprovedWrappers, fingerprint)
		}
		return nil
	})
}

// WrapperApproved reports whether init approved this wrapper fingerprint.
func (r Registry) WrapperApproved(fingerprint string) bool {
	return slices.Contains(r.ApprovedWrappers, fingerprint)
}
