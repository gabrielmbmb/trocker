//go:build integration

package compose_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/cli/cli/command"
	dockerconfig "github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/context/docker"
	"github.com/docker/cli/cli/context/store"

	"github.com/gabrielmbmb/trocker/internal/compose"
)

func TestTargetAliasesAndFailures(t *testing.T) {
	// Resolve the developer's selected daemon only to obtain a verified local
	// endpoint. All context metadata written below lives in a temporary directory.
	target, err := compose.ResolveTarget(t.Context())
	if err != nil {
		t.Fatal("integration tests require a reachable local Linux Docker daemon: ", err)
	}
	id, host := target.ID, target.Host
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	old := dockerconfig.Dir()
	dockerconfig.SetDir(directory)
	t.Cleanup(func() { dockerconfig.SetDir(old) })
	t.Setenv("DOCKER_CONFIG", directory)
	t.Setenv("DOCKER_HOST", "")
	contexts := store.New(filepath.Join(directory, "contexts"), command.DefaultContextStoreConfig())
	for _, name := range []string{"first", "alias"} {
		if err := contexts.CreateOrUpdate(store.Metadata{Name: name, Endpoints: map[string]any{"docker": docker.EndpointMeta{Host: host}}}); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DOCKER_CONTEXT", name)
		resolved, err := compose.ResolveTarget(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if resolved.ID != id || resolved.Host != host {
			t.Fatal("context alias changed target identity")
		}
		if err := resolved.Close(); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DOCKER_CONTEXT", "does-not-exist")
	if _, err := compose.ResolveTarget(t.Context()); err == nil {
		t.Fatal("missing context fell back to daemon")
	}
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_HOST", "unix:///nonexistent-trocker-target.sock")
	if _, err := compose.ResolveTarget(t.Context()); err == nil {
		t.Fatal("unreachable target fell back to daemon")
	}
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	if _, err := compose.ResolveTarget(t.Context()); err == nil {
		t.Fatal("unsupported TCP target accepted")
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 1 {
		t.Fatalf("unexpected Docker config writes: %v %v", entries, err)
	}
}
