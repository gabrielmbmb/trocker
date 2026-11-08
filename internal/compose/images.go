package compose

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

// RemoveBuildImages untags the images this workspace built and returns how
// many tags it removed. A tag is removed only when its image carries the
// workspace label and the tag uses the workspace prefix, so a tag someone added
// to the same image, or an unrelated image with a similar name, stays.
func (t *Target) RemoveBuildImages(ctx context.Context, registration workspace.Registration) (int, error) {
	filter := client.Filters{}.Add("label", WorkspaceLabel+"="+registration.ID)
	images, err := t.Client.ImageList(ctx, client.ImageListOptions{Filters: filter})
	if err != nil {
		return 0, &observationError{err}
	}
	prefix := registration.ProjectName() + "-"
	removed := 0
	for _, image := range images.Items {
		for _, tag := range image.RepoTags {
			if !strings.HasPrefix(tag, prefix) {
				continue
			}
			_, err := t.Client.ImageRemove(ctx, tag, client.ImageRemoveOptions{PruneChildren: true})
			if errdefs.IsNotFound(err) {
				continue
			}
			if err != nil {
				return removed, &imageRemovalError{tag: tag, cause: err}
			}
			removed++
		}
	}
	return removed, nil
}

type imageRemovalError struct {
	tag   string
	cause error
}

func (e *imageRemovalError) Error() string {
	return fmt.Sprintf("cannot remove workspace image %s", e.tag)
}

func (*imageRemovalError) Help() string {
	return "remove the containers that use the image, then retry"
}
func (e *imageRemovalError) Unwrap() error { return e.cause }
