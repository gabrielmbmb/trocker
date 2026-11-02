package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/generated"
	"github.com/gabrielmbmb/trocker/internal/plan"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestContainerRenderFailurePublishesNoConsumers(t *testing.T) {
	directory := t.TempDir()
	store := state.Store{Directory: filepath.Join(directory, "state")}
	templates := make(map[string]*generated.Templates)
	manifest := &config.Manifest{Bindings: make(map[string]config.Bindings)}
	project := &types.Project{Services: make(types.Services)}
	for name, body := range map[string]string{"a": "{}", "z": "not JSON"} {
		filename := name + ".tmpl"
		if err := os.WriteFile(filepath.Join(directory, filename), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		files := map[string]config.GeneratedFile{"app": {Template: filename, Output: "app.json", Target: "/run/app.json", Format: "json"}}
		loaded, err := generated.Load(t.Context(), directory, files)
		if err != nil {
			t.Fatal(err)
		}
		templates[name] = loaded
		manifest.Bindings[name] = config.Bindings{Files: files}
		project.Services[name] = types.ServiceConfig{Name: name}
	}
	prepared := PlanResult{Registration: workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}, manifest: manifest}
	if err := prepareContainerFiles(t.Context(), store, prepared, "daemon", project, plan.Options{}, templates); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, err := os.Stat(store.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partially rendered consumers were published", err)
	}
	if len(project.Configs) != 0 {
		t.Fatal("partial configs were applied")
	}
}
