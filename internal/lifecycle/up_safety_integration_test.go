//go:build integration

package lifecycle_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func TestUpRejectsForeignRetainedResources(t *testing.T) {
	for _, kind := range []string{"network", "volume"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			main, store, registration, target := upWorkspace(t, ctx)
			labels := map[string]string{"io.trocker.fixture-id": registration.ID}
			name := registration.ProjectName() + "_data"
			if kind == "network" {
				name = registration.ProjectName() + "_default"
				created, err := target.Client.NetworkCreate(ctx, name, client.NetworkCreateOptions{Labels: labels})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
					defer cancel()
					found, err := target.Client.NetworkInspect(cleanup, created.ID, client.NetworkInspectOptions{})
					if err != nil {
						t.Error(err)
						return
					}
					if found.Network.Labels["io.trocker.fixture-id"] != registration.ID || found.Network.Labels[compose.WorkspaceLabel] != "" {
						t.Error("foreign network was adopted or changed")
						return
					}
					if _, err := target.Client.NetworkRemove(cleanup, created.ID, client.NetworkRemoveOptions{}); err != nil {
						t.Error(err)
					}
				})
			} else {
				if _, err := target.Client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: labels}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
					defer cancel()
					found, err := target.Client.VolumeInspect(cleanup, name, client.VolumeInspectOptions{})
					if err != nil {
						t.Error(err)
						return
					}
					if found.Volume.Labels["io.trocker.fixture-id"] != registration.ID || found.Volume.Labels[compose.WorkspaceLabel] != "" {
						t.Error("foreign volume was adopted or changed")
						return
					}
					if _, err := target.Client.VolumeRemove(cleanup, name, client.VolumeRemoveOptions{}); err != nil {
						t.Error(err)
					}
				})
			}
			if _, err := lifecycle.Plan(ctx, main, store); err == nil || !strings.Contains(err.Error(), "ownership") {
				t.Fatalf("plan accepted foreign %s: %v", kind, err)
			}
			if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err == nil || !strings.Contains(err.Error(), "ownership") {
				t.Fatalf("up accepted foreign %s: %v", kind, err)
			}
			containers, err := target.Containers(ctx, registration)
			if err != nil || len(containers) != 0 {
				t.Fatalf("failed preflight created containers: %v", err)
			}
		})
	}
}

func TestCanceledUpPreservesResourcesAndCanRecover(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	writeSelfBinding(t, main)
	unhealthy := strings.Replace(upCompose, "[CMD, test, -f, /data/token]", "[CMD, 'false']", 1)
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte(unhealthy), 0o600); err != nil {
		t.Fatal(err)
	}
	startup, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		_, err := lifecycle.Up(startup, main, store, compose.UpOptions{Detach: true}, io.Discard)
		done <- err
	}()
	for {
		containers, err := target.Containers(ctx, registration)
		if err != nil {
			t.Fatal(err)
		}
		if len(containers) > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("startup ended before cancellation: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	containers, err := target.Containers(ctx, registration)
	if err != nil || len(containers) == 0 {
		t.Fatal("cancellation deleted partial resources")
	}
	if _, err := target.Client.VolumeInspect(ctx, registration.ProjectName()+"_data", client.VolumeInspectOptions{}); err != nil {
		t.Fatal("cancellation lost volume", err)
	}
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte(upCompose), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceID != registration.ID {
		t.Fatal("recovery changed workspace identity")
	}
	readEndpointToken(t, ctx, result)
}
