package lifecycle

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/gabrielmbmb/trocker/internal/generated"
	"github.com/gabrielmbmb/trocker/internal/plan"
	"github.com/gabrielmbmb/trocker/internal/state"
)

func loadContainerTemplates(ctx context.Context, prepared PlanResult) (map[string]*generated.Templates, error) {
	templates := make(map[string]*generated.Templates)
	for _, name := range slices.Sorted(maps.Keys(prepared.Project.Services)) {
		files := prepared.manifest.Bindings[name].Files
		if len(files) == 0 {
			continue
		}
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			return nil, fmt.Errorf("generated container files currently require Linux or macOS host permissions")
		}
		loaded, err := generated.Load(ctx, prepared.manifest.Directory, files)
		if err != nil {
			return nil, err
		}
		templates[name] = loaded
	}
	return templates, nil
}

func prepareContainerFiles(ctx context.Context, store state.Store, prepared PlanResult, targetID string, project *types.Project, options plan.Options, templates map[string]*generated.Templates) error {
	// Finish all rendering before publishing anything or calling Compose.
	rendered := make(map[string]map[string][]byte)
	for _, name := range slices.Sorted(maps.Keys(templates)) {
		files, err := templates[name].Render(ctx, func(reference string) (string, error) {
			return plan.ResolveFileValue(reference, name, prepared.manifest, project, prepared.Registration.ID, options)
		})
		if err != nil {
			return err
		}
		rendered[name] = files
	}
	for _, name := range slices.Sorted(maps.Keys(rendered)) {
		paths, err := store.PublishContainerFiles(ctx, prepared.Registration.ID, targetID, name, rendered[name])
		if err != nil {
			return err
		}
		service := project.Services[name]
		if project.Configs == nil {
			project.Configs = make(types.Configs)
		}
		for _, alias := range slices.Sorted(maps.Keys(prepared.manifest.Bindings[name].Files)) {
			file := prepared.manifest.Bindings[name].Files[alias]
			source := paths[file.Output]
			// Config references participate in Compose service hashes. The opaque
			// generation changes on content changes, without publishing content digests.
			key := fmt.Sprintf("trocker-%s-%s-%s", name, filepath.Base(filepath.Dir(source)), alias)
			project.Configs[key] = types.ConfigObjConfig{File: source, Name: key}
			service.Configs = append(service.Configs, types.ServiceConfigObjConfig{Source: key, Target: file.Target})
		}
		project.Services[name] = service
	}
	return nil
}
